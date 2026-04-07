<script setup lang="ts">
import * as echarts from "echarts";
import { computed, nextTick, onMounted, onUnmounted, ref, watch } from "vue";
import { useRouter } from "vue-router";
import { request } from "../utils/request";

type DeviceInfo = {
  device_id: string;
  online?: boolean;
  has_critical_risk?: boolean;
  os?: string;
  ip?: string;
  agent_version?: string;
  first_seen_at?: number;
  agent_version_updated_at?: number;
};

type VersionCount = {
  version: string;
  device_count: number;
};

type SummaryItem = {
  name: string;
  device_count: number;
  versions?: VersionCount[];
};

type FindRow = {
  device_id: string;
  version?: string;
};

const props = defineProps<{ devices: DeviceInfo[] }>();
const router = useRouter();

const loading = ref(true);
const searching = ref(false);
const summary = ref<SummaryItem[]>([]);
const selectedName = ref("");
const searchQ = ref("");
const findResults = ref<FindRow[]>([]);
const selectedNotify = ref<Set<string>>(new Set());
const fixingRows = ref<Set<string>>(new Set());
const lastFixError = ref("");
const summaryCached = ref(false);
const findCached = ref(false);
const summaryFetchedAt = ref(0);
const findFetchedAt = ref(0);
const vulnerableAffectedCount = ref(0);
const nowMs = ref(Date.now());

const chartRef = ref<HTMLElement | null>(null);
let chart: echarts.ECharts | null = null;
let tick: number | null = null;

const osChartRef = ref<HTMLElement | null>(null);
let osChart: echarts.ECharts | null = null;

const VULNERABILITY_RULES = [
  { name: "OpenSSL", version_lt: "3.0.7" },
  { name: "log4j", version_lt: "2.17.1" },
  { name: "Git", version_eq: "2.53.0" },
];

const LATEST_AGENT_VERSION = "v1.0.1";

const onlineDeviceCount = computed(() => (props.devices || []).filter((d) => d.online === true).length);
const exposedPublicPortCount = computed(
  () => (props.devices || []).filter((d) => d.online === true && d.has_critical_risk === true).length
);
const selectedSoftware = computed(() => summary.value.find((it) => it.name === selectedName.value) || null);

const fleetTotalCount = computed(() => (props.devices || []).length);
const latestVersionCount = computed(
  () => (props.devices || []).filter((d) => String(d.agent_version || "").trim() === LATEST_AGENT_VERSION).length
);
const latestVersionPct = computed(() => {
  const total = fleetTotalCount.value || 0;
  if (total <= 0) return 0;
  return Math.max(0, Math.min(100, Math.round((latestVersionCount.value / total) * 100)));
});

const newDevices5mCount = computed(() => {
  const nowSec = Math.floor(nowMs.value / 1000);
  return (props.devices || []).filter((d) => {
    const t = Number(d.first_seen_at ?? 0);
    return t > 0 && nowSec - t >= 0 && nowSec - t <= 300;
  }).length;
});

const deployAnomalyCount = computed(() => {
  const nowSec = Math.floor(nowMs.value / 1000);
  return (props.devices || []).filter((d) => {
    const t = Number(d.agent_version_updated_at ?? 0);
    if (!t || t <= 0) return false;
    const age = nowSec - t;
    return d.online === false && age >= 600;
  }).length;
});

function normOSName(os?: string): "windows" | "linux" | "other" {
  const s = String(os || "").toLowerCase();
  if (s.includes("windows")) return "windows";
  if (s.includes("linux")) return "linux";
  return "other";
}

const osCounts = computed(() => {
  let windows = 0;
  let linux = 0;
  let other = 0;
  for (const d of props.devices || []) {
    const t = normOSName(d.os);
    if (t === "windows") windows++;
    else if (t === "linux") linux++;
    else other++;
  }
  return { windows, linux, other };
});

const deployStartAtMs = ref(0);
const expectedDeployTargets = computed(() => {
  const raw = String((import.meta as any)?.env?.VITE_DEPLOY_TARGETS || "").trim();
  if (!raw) return [] as string[];
  const parts = raw
    .split(/[,\n;]/g)
    .map((x) => String(x || "").trim())
    .filter(Boolean);
  // unique
  return Array.from(new Set(parts));
});

const missingTargets = computed(() => {
  const start = deployStartAtMs.value || 0;
  if (!start) return [] as string[];
  const elapsedSec = Math.floor((nowMs.value - start) / 1000);
  if (elapsedSec < 600) return [] as string[];
  const targets = expectedDeployTargets.value;
  if (targets.length === 0) return [] as string[];
  const ips = new Set((props.devices || []).map((d) => String(d.ip || "").trim()).filter(Boolean));
  return targets.filter((t) => !ips.has(String(t).trim()));
});

const summaryAgoSec = computed(() =>
  summaryFetchedAt.value > 0 ? Math.max(0, Math.floor((nowMs.value - summaryFetchedAt.value) / 1000)) : 0
);
const findAgoSec = computed(() =>
  findFetchedAt.value > 0 ? Math.max(0, Math.floor((nowMs.value - findFetchedAt.value) / 1000)) : 0
);

function normalizeVersion(v?: string) {
  return String(v || "").trim() || "unknown";
}

function parseVersion(ver?: string): number[] {
  const src = normalizeVersion(ver);
  const parts = src.match(/\d+/g) || [];
  return parts.map((x) => Number(x));
}

function isLess(a?: string, b?: string): boolean {
  const aa = parseVersion(a);
  const bb = parseVersion(b);
  const n = Math.max(aa.length, bb.length);
  for (let i = 0; i < n; i++) {
    const av = aa[i] ?? 0;
    const bv = bb[i] ?? 0;
    if (av !== bv) return av < bv;
  }
  return false;
}

function isEqual(a?: string, b?: string): boolean {
  return normalizeVersion(a).toLowerCase() === normalizeVersion(b).toLowerCase();
}

function isVulnerable(name?: string, version?: string): boolean {
  const n = String(name || "").toLowerCase();
  for (const rule of VULNERABILITY_RULES) {
    if (!n.includes(rule.name.toLowerCase())) continue;
    if (rule.version_lt && isLess(version, rule.version_lt)) return true;
    if (rule.version_eq && isEqual(version, rule.version_eq)) return true;
  }
  return false;
}

async function fetchSummary() {
  const resp = await request.get("/api/v1/assets/software/summary");
  const rows = Array.isArray(resp?.data?.results) ? resp.data.results : [];
  summary.value = rows;
  selectedName.value = rows[0]?.name || "";
  summaryCached.value = !!resp?.data?.cached;
  summaryFetchedAt.value = Date.now();
}

async function fetchVulnerableAffectedCount() {
  const hit = new Set<string>();
  for (const rule of VULNERABILITY_RULES) {
    try {
      const resp = await request.get("/api/v1/assets/software/find", { params: { q: rule.name } });
      const rows = Array.isArray(resp?.data?.results) ? resp.data.results : [];
      for (const r of rows) {
        const id = String(r?.device_id || "").trim();
        const version = String(r?.version || "");
        if (id && isVulnerable(rule.name, version)) hit.add(id);
      }
    } catch {
      // ignore single rule errors
    }
  }
  vulnerableAffectedCount.value = hit.size;
}

async function refreshAll() {
  loading.value = true;
  try {
    await Promise.all([fetchSummary(), fetchVulnerableAffectedCount()]);
  } finally {
    loading.value = false;
  }
}

async function runSearch() {
  const q = searchQ.value.trim();
  if (!q) {
    findResults.value = [];
    return;
  }
  searching.value = true;
  try {
    const resp = await request.get("/api/v1/assets/software/find", { params: { q } });
    findResults.value = Array.isArray(resp?.data?.results) ? resp.data.results : [];
    findCached.value = !!resp?.data?.cached;
    findFetchedAt.value = Date.now();
  } catch {
    findResults.value = [];
  } finally {
    searching.value = false;
  }
}

async function oneClickFix(row: FindRow) {
  const key = `${String(row.device_id || "")}-${String(row.version || "")}`;
  if (fixingRows.value.has(key)) return;
  const next = new Set(fixingRows.value);
  next.add(key);
  fixingRows.value = next;
  lastFixError.value = "";
  const q = searchQ.value.trim();
  const isRisk = isVulnerable(q, row.version);
  const payload = isRisk
    ? { command: "custom_script", script: "clean_cache", args: [q] }
    : { command: "restart_service", service: "go-agent" };
  try {
    await request.post(`/api/v1/control/${encodeURIComponent(String(row.device_id || ""))}`, payload, {
      timeout: 10000,
    });
  } catch (err: any) {
    lastFixError.value = String(err?.response?.data?.error || err?.message || "修复指令执行失败");
  } finally {
    const done = new Set(fixingRows.value);
    done.delete(key);
    fixingRows.value = done;
  }
}

function togglePick(deviceID: string, checked: boolean) {
  const id = String(deviceID || "").trim();
  if (!id) return;
  const next = new Set(selectedNotify.value);
  if (checked) next.add(id);
  else next.delete(id);
  selectedNotify.value = next;
}

function onPickChange(deviceID: string, e: Event) {
  const checked = !!(e.target as HTMLInputElement | null)?.checked;
  togglePick(deviceID, checked);
}

async function batchNotify() {
  const ids = [...selectedNotify.value];
  if (ids.length === 0) return;
  await request.post("/api/v1/control/notify", {
    device_ids: ids,
    message: "检测到风险资产，请尽快核查处置",
  });
}

function jumpToDevice(id?: string) {
  const deviceId = String(id || "").trim();
  if (!deviceId) return;
  void router.push({ path: "/", query: { device: deviceId } });
}

function ensureChart() {
  if (!chartRef.value) return;
  if (!chart) chart = echarts.init(chartRef.value, undefined, { renderer: "canvas" });
}

function ensureOSChart() {
  if (!osChartRef.value) return;
  if (!osChart) osChart = echarts.init(osChartRef.value, undefined, { renderer: "canvas" });
}

function renderChart() {
  ensureChart();
  if (!chart) return;
  const item = selectedSoftware.value;
  if (!item) {
    chart.clear();
    return;
  }
  const versions = [...(item.versions || [])];
  versions.sort((a, b) => b.device_count - a.device_count);
  const yData = versions.map((v) => normalizeVersion(v.version));
  const seriesData = versions.map((v) => ({
    value: v.device_count,
    itemStyle: {
      color: isVulnerable(item.name, v.version) ? "#ef4444" : "#22d3ee",
    },
  }));

  chart.setOption({
    backgroundColor: "transparent",
    grid: { left: 100, right: 24, top: 24, bottom: 24 },
    xAxis: { type: "value", axisLabel: { color: "#94a3b8" }, splitLine: { lineStyle: { color: "#334155" } } },
    yAxis: { type: "category", data: yData, axisLabel: { color: "#cbd5e1" } },
    tooltip: { trigger: "axis", axisPointer: { type: "shadow" } },
    series: [{ type: "bar", data: seriesData, barWidth: 18 }],
  });
}

function renderOSChart() {
  ensureOSChart();
  if (!osChart) return;
  const { windows, linux, other } = osCounts.value;
  const data = [
    { name: "Windows", value: windows, itemStyle: { color: "#60a5fa" } },
    { name: "Linux", value: linux, itemStyle: { color: "#22d3ee" } },
  ];
  if (other > 0) {
    data.push({ name: "Other", value: other, itemStyle: { color: "#a78bfa" } });
  }
  osChart.setOption({
    backgroundColor: "transparent",
    tooltip: { trigger: "item" },
    legend: { top: 8, left: 8, textStyle: { color: "#94a3b8" } },
    series: [
      {
        type: "pie",
        radius: ["45%", "70%"],
        center: ["50%", "58%"],
        avoidLabelOverlap: true,
        label: { color: "#cbd5e1", formatter: "{b}: {d}%" },
        labelLine: { lineStyle: { color: "#475569" } },
        data,
      },
    ],
  });
}

watch(selectedSoftware, () => nextTick(() => renderChart()));
watch(
  () => summary.value,
  () => nextTick(() => renderChart()),
  { deep: true }
);
watch(osCounts, () => nextTick(() => renderOSChart()), { deep: true });

onMounted(() => {
  void refreshAll();
  deployStartAtMs.value = Date.now();
  tick = window.setInterval(() => {
    nowMs.value = Date.now();
  }, 1000);
  window.addEventListener("resize", renderChart);
  window.addEventListener("resize", renderOSChart);
  nextTick(() => renderOSChart());
});

onUnmounted(() => {
  if (tick != null) window.clearInterval(tick);
  window.removeEventListener("resize", renderChart);
  window.removeEventListener("resize", renderOSChart);
  chart?.dispose();
  chart = null;
  osChart?.dispose();
  osChart = null;
});
</script>

<template>
  <div class="h-full w-full bg-slate-950 text-slate-200 relative">
    <div v-if="loading" class="absolute inset-0 z-20 bg-slate-950/90 flex items-center justify-center">
      <div class="text-sm text-slate-400">资产中心加载中...</div>
    </div>

    <div class="h-full p-4 flex flex-col gap-4">
      <div class="grid grid-cols-1 md:grid-cols-4 gap-3">
        <div class="card-base card-padding">
          <div class="text-xs text-slate-400">在线设备总数</div>
          <div class="text-2xl font-mono text-cyan-300 mt-1">{{ onlineDeviceCount }}</div>
        </div>
        <div class="card-base card-padding">
          <div class="text-xs text-slate-400">受软件漏洞影响设备数</div>
          <div class="text-2xl font-mono text-red-300 mt-1">{{ vulnerableAffectedCount }}</div>
        </div>
        <div class="card-base card-padding">
          <div class="text-xs text-slate-400">暴露公网端口数</div>
          <div class="text-2xl font-mono text-amber-300 mt-1">{{ exposedPublicPortCount }}</div>
        </div>
        <div class="card-base card-padding">
          <div class="flex items-center justify-between">
            <div class="text-xs text-slate-400">部署实时看板</div>
            <div class="text-[11px] text-slate-500 font-mono">{{ LATEST_AGENT_VERSION }}</div>
          </div>

          <div class="mt-2">
            <div class="flex items-center justify-between text-[11px] text-slate-400">
              <span>版本进度</span>
              <span class="font-mono text-slate-300">{{ latestVersionCount }}/{{ fleetTotalCount }}（{{ latestVersionPct }}%）</span>
            </div>
            <div class="mt-1 h-2 rounded bg-slate-900/70 border border-slate-800 overflow-hidden">
              <div
                class="h-full bg-emerald-400/80"
                :style="{ width: latestVersionPct + '%' }"
              ></div>
            </div>
          </div>

          <div class="mt-2 grid grid-cols-2 gap-2">
            <div class="rounded border border-slate-800 bg-slate-900/30 px-2 py-1">
              <div class="text-[10px] text-slate-500">过去 5 分钟新注册</div>
              <div class="text-lg font-mono text-sky-300">{{ newDevices5mCount }}</div>
            </div>
            <div class="rounded border border-slate-800 bg-slate-900/30 px-2 py-1">
              <div class="text-[10px] text-slate-500">部署异常（离线 ≥10m）</div>
              <div class="text-lg font-mono" :class="deployAnomalyCount > 0 ? 'text-fuchsia-200' : 'text-slate-300'">
                {{ deployAnomalyCount }}
              </div>
            </div>
          </div>
        </div>
      </div>

      <div class="grid grid-cols-1 xl:grid-cols-5 gap-4">
        <div class="xl:col-span-2 card-base card-padding min-h-0 flex flex-col">
          <div class="flex items-center justify-between mb-2">
            <div class="text-sm text-slate-300">系统类型统计</div>
            <div class="text-[11px] text-slate-500 font-mono">
              Win {{ osCounts.windows }} · Linux {{ osCounts.linux }}<span v-if="osCounts.other"> · Other {{ osCounts.other }}</span>
            </div>
          </div>
          <div ref="osChartRef" class="flex-1 min-h-[220px]"></div>
        </div>

        <div class="xl:col-span-3 card-base card-padding min-h-0">
          <div class="flex items-center justify-between">
            <div class="text-sm text-slate-300">异常提醒</div>
            <div class="text-[11px] text-slate-500">
              <span v-if="expectedDeployTargets.length === 0">未配置 VITE_DEPLOY_TARGETS</span>
              <span v-else>Inventory {{ expectedDeployTargets.length }} 台</span>
            </div>
          </div>
          <div class="mt-1 text-[11px] text-slate-500">
            规则：清单内目标在部署开始后 10 分钟内未在后端注册（按 IP 匹配 devices.ip）则列为异常
          </div>
          <div class="mt-2 border border-slate-800 rounded bg-slate-900/20 max-h-32 overflow-auto scroll-dark">
            <div v-if="missingTargets.length === 0" class="px-3 py-3 text-sm text-slate-500">
              暂无异常目标
            </div>
            <div v-else class="divide-y divide-slate-800/70">
              <div v-for="ip in missingTargets" :key="ip" class="px-3 py-2 flex items-center justify-between">
                <div class="font-mono text-fuchsia-200">{{ ip }}</div>
                <div class="text-[11px] text-slate-500">未注册 ≥10m</div>
              </div>
            </div>
          </div>
        </div>
      </div>

      <div class="card-base card-padding">
        <div class="flex items-center gap-2">
          <input
            v-model="searchQ"
            class="flex-1 rounded border border-slate-800 bg-slate-900/60 px-3 py-2 text-sm outline-none focus:ring-2 focus:ring-cyan-500"
            placeholder="全网软件/端口搜索（优先软件名）"
            @keyup.enter="runSearch"
          />
          <button class="px-3 py-2 text-sm rounded border border-cyan-700 bg-cyan-500/10 hover:bg-cyan-500/20" @click="runSearch">
            搜索
          </button>
        </div>
        <div class="mt-2 text-[11px] text-slate-500">
          <span v-if="searching">搜索中...</span>
          <span v-else-if="findFetchedAt">检索{{ findCached ? "缓存" : "实时" }}数据，更新于 {{ findAgoSec }} 秒前</span>
        </div>
        <div v-if="lastFixError" class="mt-1 text-[11px] text-red-300">{{ lastFixError }}</div>
        <div class="mt-2 flex items-center justify-end">
          <button
            class="px-3 py-1 text-xs rounded border border-amber-700 bg-amber-500/10 hover:bg-amber-500/20 disabled:opacity-50"
            :disabled="selectedNotify.size===0"
            @click="batchNotify"
          >
            批量通知（{{ selectedNotify.size }}）
          </button>
        </div>
        <div class="mt-2 max-h-44 overflow-auto scroll-dark border border-slate-800 rounded">
          <table class="w-full text-xs">
            <thead class="bg-slate-900/80 text-slate-400">
              <tr>
                <th class="text-left px-2 py-1 w-10">选</th>
                <th class="text-left px-2 py-1">Device ID</th>
                <th class="text-left px-2 py-1">Version</th>
                <th class="text-left px-2 py-1 w-24">处置</th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="r in findResults" :key="`${r.device_id}-${r.version || ''}`" class="border-t border-slate-800/70">
                <td class="px-2 py-1">
                  <input type="checkbox" class="accent-amber-400" :checked="selectedNotify.has(r.device_id)"
                    @change="onPickChange(r.device_id, $event)" />
                </td>
                <td class="px-2 py-1">
                  <button class="text-cyan-300 hover:underline" @click="jumpToDevice(r.device_id)">{{ r.device_id }}</button>
                </td>
                <td class="px-2 py-1 text-slate-300" :class="isVulnerable(searchQ, r.version) ? 'text-red-300' : ''">
                  {{ r.version || "-" }}
                </td>
                <td class="px-2 py-1">
                  <button
                    class="px-2 py-0.5 rounded border border-emerald-700 bg-emerald-500/10 hover:bg-emerald-500/20 disabled:opacity-50"
                    :disabled="fixingRows.has(`${r.device_id}-${r.version || ''}`)"
                    @click="oneClickFix(r)"
                  >{{ fixingRows.has(`${r.device_id}-${r.version || ''}`) ? "处理中..." : "一键修复" }}</button>
                </td>
              </tr>
              <tr v-if="!searching && findResults.length === 0">
                <td colspan="4" class="px-2 py-4 text-center text-slate-500">暂无检索结果</td>
              </tr>
            </tbody>
          </table>
        </div>
      </div>

      <div class="grid grid-cols-1 xl:grid-cols-5 gap-4 flex-1 min-h-0">
        <div class="xl:col-span-2 card-base card-padding min-h-0 flex flex-col">
          <div class="flex items-center justify-between mb-2">
            <div class="text-sm text-slate-300">Top 20 软件热度</div>
            <div class="text-[11px] text-slate-500">更新于 {{ summaryAgoSec }} 秒前</div>
          </div>
          <div class="text-[11px] text-slate-500 mb-2">{{ summaryCached ? "缓存结果" : "实时结果" }}</div>
          <div class="flex-1 overflow-auto scroll-dark">
            <button
              v-for="item in summary"
              :key="item.name"
              class="w-full text-left px-2 py-2 rounded border mb-1"
              :class="selectedName===item.name ? 'border-cyan-600 bg-cyan-500/10' : 'border-slate-800 hover:border-slate-700 bg-slate-900/20'"
              @click="selectedName = item.name"
            >
              <div class="flex items-center justify-between">
                <span class="text-slate-200 truncate pr-2">{{ item.name }}</span>
                <span class="text-cyan-300 font-mono">{{ item.device_count }}</span>
              </div>
            </button>
          </div>
        </div>
        <div class="xl:col-span-3 card-base card-padding min-h-0 flex flex-col">
          <div class="text-sm text-slate-300 mb-2">版本分布：{{ selectedSoftware?.name || "-" }}</div>
          <div ref="chartRef" class="flex-1 min-h-[280px]"></div>
          <div class="mt-2 text-[11px] text-slate-500">红色条表示命中漏洞规则版本</div>
        </div>
      </div>
    </div>
  </div>
</template>

