<script setup lang="ts">
import { computed, ref, watch } from "vue";
import { request } from "../utils/request";

type Props = {
  devices: Array<{
    device_id: string;
    hostname?: string;
    os?: string;
    ip?: string;
    mac?: string;
    iface_type?: string;
    cpu_percent?: number;
    mem_used_percent?: number;
    updated_at?: number;
    online?: boolean;
    has_critical_risk?: boolean;
    had_critical_risk?: boolean;
    last_critical_at?: number;
  }>;
  selectedDeviceId: string;
  updateTrigger?: number; // force recompute on external signals
};

const props = defineProps<Props>();
const emit = defineEmits<{
  (e: "select", deviceId: string): void;
}>();

const q = ref("");
const onlineOnly = ref(true);
const riskOnly = ref(false);
const softMatchedDeviceIds = ref<Set<string> | null>(null);
const softSearchLoading = ref(false);
let softSearchTimer: number | null = null;

type Row = Props["devices"][number];

function parseSoftQuery(raw: string): string {
  const s = String(raw || "").trim();
  if (!s.toLowerCase().startsWith("soft:")) return "";
  return s.slice(5).trim();
}

const softKeyword = computed(() => parseSoftQuery(q.value));
const softMatchedCount = computed(() => {
  if (!softKeyword.value) return 0;
  return softMatchedDeviceIds.value?.size ?? 0;
});

watch(
  () => q.value,
  (next) => {
    const keyword = parseSoftQuery(next);
    if (softSearchTimer != null) {
      window.clearTimeout(softSearchTimer);
      softSearchTimer = null;
    }
    if (!keyword) {
      softMatchedDeviceIds.value = null;
      softSearchLoading.value = false;
      return;
    }
    softSearchTimer = window.setTimeout(async () => {
      try {
        softSearchLoading.value = true;
        const resp = await request.get("/api/v1/assets/search", { params: { q: keyword } });
        const rows = Array.isArray(resp?.data?.results) ? resp.data.results : [];
        const ids = new Set<string>();
        for (const r of rows) {
          const id = String(r?.device_id || "").trim();
          if (id) ids.add(id);
        }
        softMatchedDeviceIds.value = ids;
      } catch {
        softMatchedDeviceIds.value = new Set<string>();
      } finally {
        softSearchLoading.value = false;
      }
    }, 220);
  },
  { immediate: true }
);

const onlineCriticalCount = computed(() => {
  const list = props.devices ?? [];
  return list.filter((d) => d.online === true && d.has_critical_risk === true).length;
});

function normOS(os?: string) {
  const v = (os ?? "").trim();
  return v || "OS 未知";
}

function clampPct(v: any) {
  const n = Number(v ?? 0);
  if (!Number.isFinite(n)) return 0;
  return Math.min(100, Math.max(0, n));
}

function ifaceTypeLabel(v?: string) {
  if (v === "physical") return "有线";
  if (v === "wireless") return "无线";
  return "未知";
}

const filtered = computed<Row[]>(() => {
  // Depend on updateTrigger for forced recompute
  void props.updateTrigger;
  const query = q.value.trim().toLowerCase();
  const list = [...(props.devices ?? [])].sort(
    (a, b) => (b.updated_at ?? 0) - (a.updated_at ?? 0)
  );
  let list2 = onlineOnly.value ? list.filter((d) => d.online === true) : list;
  if (riskOnly.value) {
    // Show current critical devices; also include offline devices that were critical before going offline.
    list2 = list2.filter((d) => d.has_critical_risk === true || (d.online === false && d.had_critical_risk === true));
  }
  const softQ = parseSoftQuery(query);
  if (softQ) {
    const ids = softMatchedDeviceIds.value;
    if (!ids) return list2; // waiting first search result
    return list2.filter((d) => ids.has(String(d.device_id || "")));
  }
  if (!query) return list2;
  return list2.filter((d) => {
    const id = (d.device_id ?? "").toLowerCase();
    const hn = (d.hostname ?? "").toLowerCase();
    const os = (d.os ?? "").toLowerCase();
    const ip = (d.ip ?? "").toLowerCase();
    const mac = (d.mac ?? "").toLowerCase();
    return id.includes(query) || hn.includes(query) || os.includes(query) || ip.includes(query) || mac.includes(query);
  });
});

const grouped = computed(() => {
  const groups = new Map<string, Row[]>();
  for (const d of filtered.value) {
    const key = normOS(d.os);
    if (!groups.has(key)) groups.set(key, []);
    groups.get(key)!.push(d);
  }
  return Array.from(groups.entries()).sort((a, b) => a[0].localeCompare(b[0]));
});

function timeAgo(sec?: number) {
  if (!sec || sec <= 0) return "未知";
  const diff = Math.max(0, Math.floor(Date.now() / 1000 - sec));
  if (diff < 10) return "刚刚";
  if (diff < 60) return `${diff}s`;
  if (diff < 3600) return `${Math.floor(diff / 60)}m`;
  if (diff < 86400) return `${Math.floor(diff / 3600)}h`;
  return `${Math.floor(diff / 86400)}d`;
}
</script>

<template>
  <div class="h-full min-h-0 flex flex-col">
    <div class="h-14 shrink-0 flex items-center px-4 border-b border-slate-800 text-slate-400">
      设备列表
    </div>
    <div class="p-3 border-b border-slate-800 shrink-0">
      <!-- 全网风险概览 -->
      <div class="card-base card-padding mb-3">
        <div class="flex items-center justify-between">
          <div class="text-xs text-slate-300 font-semibold">全网风险概览</div>
          <div class="text-[11px] text-slate-500">在线设备</div>
        </div>
        <div class="mt-2 flex items-end justify-between">
          <div>
            <div class="text-[11px] text-slate-500">CRITICAL 风险设备</div>
            <div class="text-2xl font-bold text-red-200 font-mono">{{ onlineCriticalCount }}</div>
          </div>
          <div class="text-[11px] text-slate-500">
            仅统计在线且存在公网高危端口暴露
          </div>
        </div>
      </div>

      <div class="flex items-center justify-between">
        <label class="flex items-center gap-2 text-xs text-slate-300 select-none">
          <input v-model="onlineOnly" type="checkbox" class="accent-cyan-400" />
          只看在线
        </label>
        <label class="flex items-center gap-2 text-xs text-slate-300 select-none">
          <input v-model="riskOnly" type="checkbox" class="accent-red-400" />
          风险设备
        </label>
      </div>
      <div class="mt-2">
        <input
          v-model="q"
          class="w-full rounded border border-slate-800 bg-slate-900/60 px-2 py-1 text-sm outline-none focus:ring-2 focus:ring-cyan-500"
          placeholder="搜索：hostname / OS / IP，或 soft:nginx"
        />
        <div v-if="softSearchLoading" class="mt-1 text-[11px] text-slate-500">正在全网检索软件资产...</div>
        <div v-else-if="softKeyword" class="mt-1 text-[11px] text-slate-500">
          软件检索：<span class="text-slate-300 font-mono">{{ softKeyword }}</span>，命中设备
          <span class="text-cyan-300 font-mono">{{ softMatchedCount }}</span> 台
        </div>
      </div>
    </div>

    <div class="p-2 flex-1 min-h-0 scroll-area scroll-dark">
      <div v-if="grouped.length === 0" class="text-sm text-slate-500 px-2 py-6 text-center">
        {{ softKeyword ? `未检索到安装 ${softKeyword} 的设备` : "暂无设备" }}
      </div>

      <div v-for="[osName, list] in grouped" :key="osName" class="mb-3">
        <div class="px-2 py-1 text-[11px] text-slate-400 flex items-center justify-between">
          <span class="uppercase tracking-wider">{{ osName }}</span>
          <span class="text-slate-500">{{ list.length }}</span>
        </div>

        <button
          v-for="d in list"
          :key="d.device_id"
          class="w-full text-left sidebar-item"
          :class="d.device_id === selectedDeviceId ? 'sidebar-item-active' : ''"
          @click="emit('select', d.device_id)"
        >
          <div class="flex items-start justify-between gap-2">
            <div class="min-w-0 flex items-start gap-2">
              <span
                class="status-dot shrink-0 mt-1.5"
                :class="[d.online ? 'status-success animate-pulse' : 'status-danger']"
                :title="d.online ? '在线' : '离线'"
              ></span>
              <div class="min-w-0">
                <div class="text-sm text-slate-100 truncate">
                  {{ d.hostname || "未命名主机" }}
                </div>
                <div class="text-[11px] text-slate-400 truncate">
                  {{ d.ip || "IP 未知" }} · {{ timeAgo(d.updated_at) }}
                </div>
                <div class="mt-1 flex items-center gap-2">
                  <span
                    v-if="d.online && d.has_critical_risk"
                    class="text-[10px] px-2 py-0.5 rounded border border-red-700 bg-red-500/10 text-red-300 font-mono"
                    title="存在公网高危端口暴露（CRITICAL）"
                  >
                    CRITICAL
                  </span>
                  <span
                    v-else-if="!d.online && d.had_critical_risk"
                    class="text-[10px] px-2 py-0.5 rounded border border-amber-700 bg-amber-500/10 text-amber-300 font-mono"
                    title="离线前存在风险（最后一次安全快照检测到 CRITICAL）"
                  >
                    离线前存在风险
                  </span>
                </div>
                <div class="text-[11px] text-slate-500 truncate mt-0.5">
                  {{ ifaceTypeLabel(d.iface_type) }} · MAC: {{ d.mac || "未知" }}
                </div>

                <div class="mt-2 grid grid-cols-2 gap-2">
                  <div>
                    <div class="flex items-center justify-between text-[10px] text-slate-400 mb-1">
                      <span>CPU</span><span class="font-mono">{{ clampPct(d.cpu_percent).toFixed(1) }}%</span>
                    </div>
                    <div class="progress-track h-1.5">
                      <div class="progress-bar-emerald" :style="{ width: clampPct(d.cpu_percent) + '%' }"></div>
                    </div>
                  </div>
                  <div>
                    <div class="flex items-center justify-between text-[10px] text-slate-400 mb-1">
                      <span>Mem</span><span class="font-mono">{{ clampPct(d.mem_used_percent).toFixed(1) }}%</span>
                    </div>
                    <div class="progress-track h-1.5">
                      <div class="progress-bar-sky" :style="{ width: clampPct(d.mem_used_percent) + '%' }"></div>
                    </div>
                  </div>
                </div>
              </div>
            </div>
          </div>
        </button>
      </div>
    </div>
  </div>
</template>

