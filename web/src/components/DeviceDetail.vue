<script setup lang="ts">
import { computed, ref } from "vue";
import type { WSConnectionStatus } from "../composables/useWebSocket";

type Device = {
  deviceId: string;
  processes: any[];
  softwareList: any[];
  hostMetrics: any | null;
  hardwareDetails: any | null;
  networkConnections: any | null;
  securitySnapshot: any | null;
  serviceSnapshot: any | null;
  processSnapshot: any | null;
  softwareInventory: any | null;
  updatedAtSec: number;
  snapshotFound: boolean;
  wsReceived: boolean;
};

type Props = {
  device: Device;
  wsStatus: WSConnectionStatus;
};

const props = defineProps<Props>();

const statusText = computed(() => {
  if (props.wsStatus === "online") return "在线";
  if (props.wsStatus === "reconnecting") return "重连中";
  return "离线";
});

const statusClass = computed(() => {
  if (props.wsStatus === "online") return "bg-emerald-500/90";
  if (props.wsStatus === "reconnecting") return "bg-amber-500/90";
  return "bg-red-500/90";
});

const showEmptyWaiting = computed(() => {
  const pEmpty = !props.device.processes || props.device.processes.length === 0;
  const sEmpty = !props.device.softwareList || props.device.softwareList.length === 0;
  const hasAnyPanel =
    !!props.device.hostMetrics ||
    !!props.device.hardwareDetails ||
    !!props.device.networkConnections ||
    !!props.device.securitySnapshot ||
    !!props.device.serviceSnapshot ||
    !!props.device.processSnapshot ||
    !!props.device.softwareInventory;
  return pEmpty && sEmpty && !hasAnyPanel && !props.device.wsReceived;
});

const procQuery = ref("");
const swQuery = ref("");
const activeTab = ref<"overview" | "processes" | "network" | "software">("overview");

function procPid(p: any) {
  return p?.PID ?? p?.pid ?? 0;
}
function procName(p: any) {
  return p?.Name ?? p?.name ?? "";
}
function procExecPath(p: any) {
  return p?.ExecPath ?? p?.exec_path ?? p?.execPath ?? "";
}
function procMemoryBytes(p: any) {
  return p?.MemoryBytes ?? p?.memory_bytes ?? p?.MemoryUsage ?? p?.memory_used_bytes ?? 0;
}

function shorten(s: string, maxLen: number) {
  const v = String(s ?? "");
  if (!v) return "";
  if (v.length <= maxLen) return v;
  return v.slice(0, maxLen - 1) + "…";
}

function formatBytes(b: any) {
  const n = Number(b || 0);
  if (!n || Number.isNaN(n)) return "0 B";
  const units = ["B", "KB", "MB", "GB", "TB"];
  let i = 0;
  let v = n;
  while (v >= 1024 && i < units.length - 1) {
    v /= 1024;
    i++;
  }
  return `${v.toFixed(1)} ${units[i]}`;
}

function percent(v: any) {
  const n = Number(v ?? 0);
  if (Number.isNaN(n)) return "0.0";
  return n.toFixed(1);
}

const host = computed(() => props.device.hostMetrics || null);
const hw = computed(() => props.device.hardwareDetails || null);

function asArray(v: any): any[] {
  if (!v) return [];
  if (Array.isArray(v)) return v;
  // some payloads may wrap items
  if (Array.isArray(v.items)) return v.items;
  if (Array.isArray(v.Items)) return v.Items;
  return [];
}

const hwDisks = computed(() => asArray(hw.value?.physical_disks ?? hw.value?.PhysicalDisks));
const hwMemorySlots = computed(() => asArray(hw.value?.memory_slots ?? hw.value?.MemorySlots));
const singleMemorySlot = computed(() => (hwMemorySlots.value.length === 1 ? hwMemorySlots.value[0] : null));
const hwGPUs = computed(() => asArray(hw.value?.gpus ?? hw.value?.GPUs));
const hwIfaces = computed(() => asArray(hw.value?.network_ifaces ?? hw.value?.NetworkIfaces));
const hwMainboard = computed(() => hw.value?.mainboard ?? hw.value?.Mainboard ?? null);

const hostHostname = computed(() => host.value?.hostname ?? host.value?.Hostname ?? "");
const hostFingerprint = computed(() => host.value?.fingerprint ?? host.value?.Fingerprint ?? hw.value?.fingerprint ?? "");
const hostCollectedAt = computed(() => host.value?.CollectedAt ?? host.value?.collectedAt ?? "");

const cpuTotal = computed(() => host.value?.CPU?.Total ?? host.value?.cpu_total_percent ?? 0);
const memUsedPercent = computed(() => host.value?.Memory?.UsedPercent ?? host.value?.memory_used_percent ?? 0);
const memTotal = computed(() => host.value?.Memory?.Total ?? host.value?.memory_total_bytes ?? 0);
const memUsed = computed(() => host.value?.Memory?.Used ?? host.value?.memory_used_bytes ?? 0);
const diskUsedPercent = computed(() => host.value?.Disk?.UsedPercent ?? host.value?.disk_used_percent ?? 0);
const netSent = computed(() => host.value?.Network?.BytesSent ?? host.value?.network_bytes_sent ?? 0);
const netRecv = computed(() => host.value?.Network?.BytesRecv ?? host.value?.network_bytes_recv ?? 0);

const connections = computed(() => {
  const nc = props.device.networkConnections;
  if (!nc) return [];
  const list = (Array.isArray(nc?.connections) ? nc.connections : nc) ?? [];
  return Array.isArray(list) ? list : [];
});

const filteredProcesses = computed(() => {
  const list = props.device.processes ?? [];
  const q = (procQuery.value || "").trim().toLowerCase();
  if (!q) return list;
  return list.filter((p) => {
    const pid = String(procPid(p));
    const name = String(procName(p)).toLowerCase();
    const exec = String(procExecPath(p)).toLowerCase();
    return pid.includes(q) || name.includes(q) || exec.includes(q);
  });
});

const filteredSoftware = computed(() => {
  const primary = props.device.softwareList ?? [];
  const inv = props.device.softwareInventory;
  const invItems = Array.isArray(inv?.items) ? inv.items : Array.isArray(inv?.Items) ? inv.Items : Array.isArray(inv) ? inv : [];
  const list = primary.length > 0 ? primary : invItems;
  const q = (swQuery.value || "").trim().toLowerCase();
  if (!q) return list;
  return list.filter((s) => {
    const name = String(swName(s)).toLowerCase();
    const version = String(swVersion(s)).toLowerCase();
    const publisher = String(swPublisher(s)).toLowerCase();
    return name.includes(q) || version.includes(q) || publisher.includes(q);
  });
});

const softwareSource = computed(() => {
  const primary = props.device.softwareList ?? [];
  const inv = props.device.softwareInventory;
  const invItems = Array.isArray(inv?.items) ? inv.items : Array.isArray(inv?.Items) ? inv.Items : Array.isArray(inv) ? inv : [];
  return primary.length > 0 ? primary : invItems;
});

const isWindowsDevice = computed(() => {
  const osA = String(hw.value?.os ?? "").toLowerCase();
  const osB = String(hw.value?.OS ?? "").toLowerCase();
  return osA.includes("windows") || osB.includes("windows");
});

function isWindowsPatch(s: any) {
  const name = String(swName(s) || "").toLowerCase();
  const version = String(swVersion(s) || "").toLowerCase();
  const publisher = String(swPublisher(s) || "").toLowerCase();
  if (/\bkb\d{4,}\b/i.test(name) || /\bkb\d{4,}\b/i.test(version)) return true;
  if (name.includes("hotfix")) return true;
  if (name.includes("security update") || name.includes("cumulative update")) return true;
  if (name.includes("更新") && publisher.includes("microsoft")) return true;
  return false;
}

const filteredWindowsPatches = computed(() => {
  if (!isWindowsDevice.value) return [];
  return filteredSoftware.value.filter((s) => isWindowsPatch(s));
});

const filteredNormalSoftware = computed(() => {
  if (!isWindowsDevice.value) return filteredSoftware.value;
  return filteredSoftware.value.filter((s) => !isWindowsPatch(s));
});

function swName(s: any) {
  return s?.Name ?? s?.name ?? "";
}
function swVersion(s: any) {
  return s?.Version ?? s?.version ?? "";
}
function swPublisher(s: any) {
  return s?.Publisher ?? s?.publisher ?? "";
}

function memorySpeedText(v: any) {
  if (v == null) return "未采集";
  const s = String(v).trim();
  if (!s || s === "-" || s === "0") return "未采集";
  const n = Number(s);
  if (Number.isFinite(n) && n > 0) return `${n} MHz`;
  if (/mhz/i.test(s)) return s;
  return s;
}
</script>

<template>
  <div class="flex flex-col h-full">
    <div class="p-4 border-b border-slate-800 flex items-center justify-between">
      <div class="min-w-0">
        <div class="text-lg font-semibold truncate">
          设备详情：{{ device.deviceId || "未选择设备" }}
        </div>
        <div class="text-xs text-slate-400 mt-1">
          {{ device.updatedAtSec ? `更新时间：${device.updatedAtSec}` : "" }}
        </div>
      </div>

      <div class="flex items-center gap-2">
        <div class="w-2 h-2 rounded-full" :class="statusClass"></div>
        <div class="text-sm text-slate-200">{{ statusText }}</div>
      </div>
    </div>

    <div v-if="showEmptyWaiting" class="flex-1 flex items-center justify-center text-slate-500 text-sm">
      等待 Agent 首次上报...
    </div>

    <div v-else class="flex-1 overflow-y-auto p-4 space-y-4">
      <!-- Tabs -->
      <div class="flex items-center gap-2 text-xs">
        <button
          class="px-3 py-1.5 rounded border border-slate-800"
          :class="activeTab==='overview' ? 'bg-cyan-500/20 border-cyan-500/60 text-cyan-200' : 'bg-slate-900/40 text-slate-300'"
          @click="activeTab='overview'"
        >概览</button>
        <button
          class="px-3 py-1.5 rounded border border-slate-800"
          :class="activeTab==='processes' ? 'bg-cyan-500/20 border-cyan-500/60 text-cyan-200' : 'bg-slate-900/40 text-slate-300'"
          @click="activeTab='processes'"
        >进程</button>
        <button
          class="px-3 py-1.5 rounded border border-slate-800"
          :class="activeTab==='network' ? 'bg-cyan-500/20 border-cyan-500/60 text-cyan-200' : 'bg-slate-900/40 text-slate-300'"
          @click="activeTab='network'"
        >网络</button>
        <button
          class="px-3 py-1.5 rounded border border-slate-800"
          :class="activeTab==='software' ? 'bg-cyan-500/20 border-cyan-500/60 text-cyan-200' : 'bg-slate-900/40 text-slate-300'"
          @click="activeTab='software'"
        >软件</button>
      </div>

      <!-- Overview panels (debug_server-like) -->
      <section v-if="activeTab==='overview'" class="grid grid-cols-1 lg:grid-cols-3 gap-4">
        <!-- Device overview -->
        <div class="bg-slate-900/50 rounded-lg border border-slate-800 p-4">
          <div class="text-sm font-semibold mb-3">设备概览</div>
          <div class="text-xs text-slate-300 space-y-2">
            <div><span class="text-slate-500">主机名：</span><span class="font-mono">{{ hostHostname || '未知' }}</span></div>
            <div><span class="text-slate-500">硬件指纹：</span><span class="font-mono break-all">{{ hostFingerprint || device.deviceId }}</span></div>
            <div><span class="text-slate-500">采集时间：</span><span class="font-mono break-all">{{ hostCollectedAt || hw?.collected_at || '未知' }}</span></div>
          </div>
        </div>

        <!-- Realtime metrics -->
        <div class="bg-slate-900/50 rounded-lg border border-slate-800 p-4 lg:col-span-2">
          <div class="text-sm font-semibold mb-3">实时指标</div>
          <div class="space-y-3">
            <div>
              <div class="flex justify-between text-xs text-slate-300 mb-1">
                <span>CPU 占用率</span><span class="font-mono">{{ percent(cpuTotal) }}%</span>
              </div>
              <div class="h-2 bg-slate-800 rounded-full overflow-hidden">
                <div class="h-full bg-emerald-400" :style="{ width: percent(cpuTotal) + '%' }"></div>
              </div>
            </div>
            <div>
              <div class="flex justify-between text-xs text-slate-300 mb-1">
                <span>内存使用率</span><span class="font-mono">{{ percent(memUsedPercent) }}%</span>
              </div>
              <div class="h-2 bg-slate-800 rounded-full overflow-hidden">
                <div class="h-full bg-sky-400" :style="{ width: percent(memUsedPercent) + '%' }"></div>
              </div>
            </div>
            <div class="grid grid-cols-2 gap-4 text-xs text-slate-300 pt-2">
              <div><div class="text-slate-500 mb-1">总内存</div><div class="font-mono">{{ formatBytes(memTotal) }}</div></div>
              <div><div class="text-slate-500 mb-1">已用内存</div><div class="font-mono">{{ formatBytes(memUsed) }}</div></div>
              <div><div class="text-slate-500 mb-1">磁盘使用率</div><div class="font-mono">{{ percent(diskUsedPercent) }}%</div></div>
              <div><div class="text-slate-500 mb-1">网络累计收发</div><div class="font-mono">↑ {{ formatBytes(netSent) }} / ↓ {{ formatBytes(netRecv) }}</div></div>
            </div>
          </div>
        </div>

        <!-- Hardware details summary -->
        <div class="bg-slate-900/50 rounded-lg border border-slate-800 p-4 lg:col-span-3">
          <div class="flex items-center justify-between mb-3">
            <div class="text-sm font-semibold">硬件详情</div>
            <div class="text-xs text-slate-500">hardware_details</div>
          </div>
          <div v-if="hw" class="grid grid-cols-1 md:grid-cols-3 gap-4 text-xs text-slate-300">
            <div><div class="text-slate-500 mb-1">操作系统</div><div class="font-mono">{{ hw.os || '未知' }}</div></div>
            <div><div class="text-slate-500 mb-1">内核版本</div><div class="font-mono">{{ hw.kernel_version || '未知' }}</div></div>
            <div><div class="text-slate-500 mb-1">架构</div><div class="font-mono">{{ hw.kernel_arch || '未知' }}</div></div>
            <div><div class="text-slate-500 mb-1">CPU 型号</div><div class="font-mono">{{ hw.cpu_model || '未知' }}</div></div>
            <div><div class="text-slate-500 mb-1">核心数</div><div class="font-mono">{{ hw.core_count || 0 }}</div></div>
            <div><div class="text-slate-500 mb-1">采集时间</div><div class="font-mono break-all">{{ hw.collected_at || '未知' }}</div></div>
          </div>
          <div v-if="hw" class="mt-4 grid grid-cols-1 lg:grid-cols-2 gap-4">
            <div class="bg-slate-950/30 rounded border border-slate-800 p-3">
              <div class="flex items-center justify-between mb-2">
                <div class="text-xs font-semibold text-slate-200">磁盘</div>
                <div class="text-[11px] text-slate-500">共 {{ hwDisks.length }} 块</div>
              </div>
              <div v-if="hwDisks.length" class="overflow-x-auto">
                <table class="min-w-full text-xs">
                  <thead class="bg-slate-800 text-slate-300">
                    <tr>
                      <th class="px-2 py-1 text-left">型号</th>
                      <th class="px-2 py-1 text-left">类型</th>
                      <th class="px-2 py-1 text-left">总大小</th>
                      <th class="px-2 py-1 text-left">序列号</th>
                    </tr>
                  </thead>
                  <tbody class="bg-slate-900/20">
                    <tr v-for="(d, i) in hwDisks" :key="i" class="border-t border-slate-800/60">
                      <td class="px-2 py-1 font-mono text-slate-200 break-all">{{ d.model || d.Model || '-' }}</td>
                      <td class="px-2 py-1 font-mono text-slate-400">{{ (d.drive_type || d.DriveType || '-') + ' / ' + (d.bus_type || d.BusType || '-') }}</td>
                      <td class="px-2 py-1 font-mono text-slate-300">{{ formatBytes(d.size_bytes || d.SizeBytes || 0) }}</td>
                      <td class="px-2 py-1 font-mono text-slate-500 break-all">{{ d.serial || d.Serial || '-' }}</td>
                    </tr>
                  </tbody>
                </table>
              </div>
              <div v-else class="text-slate-500 text-xs py-4 text-center">
                未采集到磁盘信息（可能权限不足/平台不支持）
              </div>
            </div>

            <div class="bg-slate-950/30 rounded border border-slate-800 p-3">
              <div class="flex items-center justify-between mb-2">
                <div class="text-xs font-semibold text-slate-200">内存条</div>
                <div class="text-[11px] text-slate-500">共 {{ hwMemorySlots.length }} 条</div>
              </div>
              <div v-if="singleMemorySlot" class="rounded border border-slate-800 bg-slate-900/20 p-3">
                <div class="grid grid-cols-2 gap-3 text-xs">
                  <div>
                    <div class="text-slate-500 mb-1">槽位</div>
                    <div class="font-mono text-slate-200 break-all">{{ singleMemorySlot.slot || singleMemorySlot.Slot || "-" }}</div>
                  </div>
                  <div>
                    <div class="text-slate-500 mb-1">容量</div>
                    <div class="font-mono text-slate-300">{{ formatBytes(singleMemorySlot.size_bytes || singleMemorySlot.SizeBytes || 0) }}</div>
                  </div>
                  <div>
                    <div class="text-slate-500 mb-1">频率</div>
                    <div class="font-mono text-slate-400">{{ memorySpeedText(singleMemorySlot.speed_mhz ?? singleMemorySlot.SpeedMHz) }}</div>
                  </div>
                  <div>
                    <div class="text-slate-500 mb-1">厂商 / 类型</div>
                    <div class="font-mono text-slate-400 break-all">
                      {{ singleMemorySlot.manufacturer || singleMemorySlot.Manufacturer || "-" }}
                      ·
                      {{ singleMemorySlot.type || singleMemorySlot.Type || "-" }}
                    </div>
                  </div>
                </div>
              </div>
              <div v-else-if="hwMemorySlots.length" class="overflow-x-auto">
                <table class="min-w-full text-xs">
                  <thead class="bg-slate-800 text-slate-300">
                    <tr>
                      <th class="px-2 py-1 text-left">槽位</th>
                      <th class="px-2 py-1 text-left">容量</th>
                      <th class="px-2 py-1 text-left">频率</th>
                      <th class="px-2 py-1 text-left">厂商</th>
                      <th class="px-2 py-1 text-left">类型</th>
                    </tr>
                  </thead>
                  <tbody class="bg-slate-900/20">
                    <tr v-for="(m, i) in hwMemorySlots" :key="i" class="border-t border-slate-800/60">
                      <td class="px-2 py-1 font-mono text-slate-200 break-all">{{ m.slot || m.Slot || '-' }}</td>
                      <td class="px-2 py-1 font-mono text-slate-300">{{ formatBytes(m.size_bytes || m.SizeBytes || 0) }}</td>
                      <td class="px-2 py-1 font-mono text-slate-400">{{ (m.speed_mhz || m.SpeedMHz || '-') }} MHz</td>
                      <td class="px-2 py-1 font-mono text-slate-500 break-all">{{ m.manufacturer || m.Manufacturer || '-' }}</td>
                      <td class="px-2 py-1 font-mono text-slate-400">{{ m.type || m.Type || '-' }}</td>
                    </tr>
                  </tbody>
                </table>
              </div>
              <div v-else class="text-slate-500 text-xs py-4 text-center">
                未采集到内存条信息（可能权限不足/平台不支持）
              </div>
            </div>

            <div class="bg-slate-950/30 rounded border border-slate-800 p-3">
              <div class="flex items-center justify-between mb-2">
                <div class="text-xs font-semibold text-slate-200">网卡</div>
                <div class="text-[11px] text-slate-500">共 {{ hwIfaces.length }} 个</div>
              </div>
              <div v-if="hwIfaces.length" class="overflow-x-auto">
                <table class="min-w-full text-xs">
                  <thead class="bg-slate-800 text-slate-300">
                    <tr>
                      <th class="px-2 py-1 text-left">名称</th>
                      <th class="px-2 py-1 text-left">MAC</th>
                      <th class="px-2 py-1 text-left">IPv4</th>
                      <th class="px-2 py-1 text-left">状态</th>
                    </tr>
                  </thead>
                  <tbody class="bg-slate-900/20">
                    <tr v-for="(n, i) in hwIfaces" :key="i" class="border-t border-slate-800/60">
                      <td class="px-2 py-1 font-mono text-slate-200 break-all">{{ n.name || n.Name || '-' }}</td>
                      <td class="px-2 py-1 font-mono text-slate-400 break-all">{{ n.mac || n.MAC || '-' }}</td>
                      <td class="px-2 py-1 font-mono text-slate-300 break-all">{{ (n.ipv4 || n.IPv4 || []).join(', ') || '-' }}</td>
                      <td class="px-2 py-1 text-[11px]" :class="(n.is_up ?? n.IsUp) ? 'text-emerald-300' : 'text-slate-500'">
                        {{ (n.is_up ?? n.IsUp) ? 'UP' : 'DOWN' }}
                      </td>
                    </tr>
                  </tbody>
                </table>
              </div>
              <div v-else class="text-slate-500 text-xs py-4 text-center">
                未采集到网卡信息（可能权限不足/平台不支持）
              </div>
            </div>

            <div class="bg-slate-950/30 rounded border border-slate-800 p-3">
              <div class="flex items-center justify-between mb-2">
                <div class="text-xs font-semibold text-slate-200">主板 / GPU</div>
                <div class="text-[11px] text-slate-500">GPU {{ hwGPUs.length }} 张</div>
              </div>
              <div class="grid grid-cols-1 gap-3 text-xs">
                <div class="border border-slate-800 rounded p-2">
                  <div class="text-[11px] text-slate-500 mb-1">主板</div>
                  <div v-if="hwMainboard" class="text-slate-200 font-mono break-all">
                    {{ (hwMainboard.manufacturer || hwMainboard.Manufacturer || '-') }}
                    {{ (hwMainboard.model || hwMainboard.Model || '-') }}
                    <span class="text-slate-500">({{ hwMainboard.serial || hwMainboard.Serial || '-' }})</span>
                  </div>
                  <div v-else class="text-slate-500">未采集</div>
                </div>
                <div class="border border-slate-800 rounded p-2">
                  <div class="text-[11px] text-slate-500 mb-1">GPU</div>
                  <div v-if="hwGPUs.length" class="space-y-1">
                    <div v-for="(g, i) in hwGPUs" :key="i" class="text-slate-200 font-mono break-all">
                      {{ g.model || g.Model || '-' }}
                      <span class="text-slate-500">VRAM {{ g.vram_mb || g.VRAMMB || 0 }} MB</span>
                    </div>
                  </div>
                  <div v-else class="text-slate-500">未采集</div>
                </div>
              </div>
            </div>
          </div>
          <div v-else class="text-slate-500 text-sm py-6 text-center">等待 Agent 上报 hardware_details...</div>
        </div>
      </section>

      <!-- Processes -->
      <div v-if="activeTab==='processes'" class="bg-slate-900/50 rounded-lg border border-slate-800 p-3 h-[70vh] flex flex-col">
        <div class="flex items-center justify-between mb-2">
          <div class="text-sm font-semibold">进程列表</div>
          <div class="text-xs text-slate-500">
            共 {{ device.processes?.length ?? 0 }} 条
          </div>
        </div>

        <div class="mb-2">
          <input
            v-model="procQuery"
            class="w-full bg-slate-950/60 border border-slate-800 rounded-md px-2 py-1 text-xs outline-none focus:ring-2 focus:ring-cyan-500"
            placeholder="搜索 PID / 名称 / 路径"
          />
        </div>

        <div v-if="filteredProcesses && filteredProcesses.length" class="flex-1 min-h-0 overflow-auto">
          <table class="min-w-full text-xs">
            <thead class="bg-slate-800 text-slate-300">
              <tr>
                <th class="px-3 py-2 text-left">PID</th>
                <th class="px-3 py-2 text-left">名称</th>
                <th class="px-3 py-2 text-left">路径</th>
                <th class="px-3 py-2 text-left">内存</th>
              </tr>
            </thead>
            <tbody class="bg-slate-900/30">
              <tr
                v-for="p in filteredProcesses"
                :key="String(procPid(p)) + ':' + String(procName(p))"
                class="border-t border-slate-800/60"
              >
                <td class="px-3 py-2 font-mono text-slate-100">{{ procPid(p) }}</td>
                <td class="px-3 py-2 font-mono text-slate-200 break-all">
                  {{ procName(p) || "-" }}
                </td>
                <td class="px-3 py-2 font-mono text-slate-400 break-all">
                  {{ procExecPath(p) ? shorten(procExecPath(p), 60) : "未采集" }}
                </td>
                <td class="px-3 py-2 font-mono text-slate-500 break-all">
                  {{ formatBytes(procMemoryBytes(p)) }}
                </td>
              </tr>
            </tbody>
          </table>
        </div>
        <div v-else class="flex-1 min-h-0 flex items-center justify-center text-slate-500 text-sm">
          暂无进程数据
        </div>
      </div>

      <!-- Software -->
      <div v-if="activeTab==='software'" class="bg-slate-900/50 rounded-lg border border-slate-800 p-3 h-[70vh] flex flex-col">
        <div class="flex items-center justify-between mb-2">
          <div class="text-sm font-semibold">软件列表</div>
          <div class="text-xs text-slate-500">
            共 {{ filteredSoftware?.length ?? 0 }} 条
          </div>
        </div>

        <div class="mb-2">
          <input
            v-model="swQuery"
            class="w-full bg-slate-950/60 border border-slate-800 rounded-md px-2 py-1 text-xs outline-none focus:ring-2 focus:ring-cyan-500"
            placeholder="搜索 名称 / 版本 / 发布者"
          />
        </div>

        <div v-if="isWindowsDevice && (filteredNormalSoftware.length || filteredWindowsPatches.length)" class="flex-1 min-h-0 overflow-auto space-y-3">
          <div class="rounded border border-slate-800/70">
            <div class="px-3 py-2 text-xs text-slate-300 bg-slate-800/60 flex items-center justify-between">
              <span>普通软件</span>
              <span class="text-slate-500">共 {{ filteredNormalSoftware.length }} 条</span>
            </div>
            <div class="max-h-[30vh] overflow-auto">
              <table class="min-w-full text-xs">
                <thead class="bg-slate-800 text-slate-300">
                  <tr>
                    <th class="px-3 py-2 text-left">名称</th>
                    <th class="px-3 py-2 text-left">版本</th>
                    <th class="px-3 py-2 text-left">发布者</th>
                  </tr>
                </thead>
                <tbody class="bg-slate-900/30">
                  <tr
                    v-for="s in filteredNormalSoftware"
                    :key="String(swName(s)) + ':' + String(swVersion(s)) + ':normal'"
                    class="border-t border-slate-800/60"
                  >
                    <td class="px-3 py-2 font-mono text-slate-200 break-all">{{ swName(s) || "未采集" }}</td>
                    <td class="px-3 py-2 font-mono text-slate-400 break-all">{{ swVersion(s) || "-" }}</td>
                    <td class="px-3 py-2 font-mono text-slate-500 break-all">{{ swPublisher(s) || "-" }}</td>
                  </tr>
                </tbody>
              </table>
            </div>
          </div>

          <div class="rounded border border-slate-800/70">
            <div class="px-3 py-2 text-xs text-slate-300 bg-slate-800/60 flex items-center justify-between">
              <span>系统补丁（Windows Update / Hotfix）</span>
              <span class="text-slate-500">共 {{ filteredWindowsPatches.length }} 条</span>
            </div>
            <div v-if="filteredWindowsPatches.length" class="max-h-[30vh] overflow-auto">
              <table class="min-w-full text-xs">
                <thead class="bg-slate-800 text-slate-300">
                  <tr>
                    <th class="px-3 py-2 text-left">名称</th>
                    <th class="px-3 py-2 text-left">版本</th>
                    <th class="px-3 py-2 text-left">发布者</th>
                  </tr>
                </thead>
                <tbody class="bg-slate-900/30">
                  <tr
                    v-for="s in filteredWindowsPatches"
                    :key="String(swName(s)) + ':' + String(swVersion(s)) + ':patch'"
                    class="border-t border-slate-800/60"
                  >
                    <td class="px-3 py-2 font-mono text-slate-200 break-all">{{ swName(s) || "未采集" }}</td>
                    <td class="px-3 py-2 font-mono text-slate-400 break-all">{{ swVersion(s) || "-" }}</td>
                    <td class="px-3 py-2 font-mono text-slate-500 break-all">{{ swPublisher(s) || "-" }}</td>
                  </tr>
                </tbody>
              </table>
            </div>
            <div v-else class="text-slate-500 text-sm py-4 text-center">未识别到系统补丁</div>
          </div>
        </div>

        <div v-else-if="filteredSoftware && filteredSoftware.length" class="flex-1 min-h-0 overflow-auto">
          <table class="min-w-full text-xs">
            <thead class="bg-slate-800 text-slate-300">
              <tr>
                <th class="px-3 py-2 text-left">名称</th>
                <th class="px-3 py-2 text-left">版本</th>
                <th class="px-3 py-2 text-left">发布者</th>
              </tr>
            </thead>
            <tbody class="bg-slate-900/30">
              <tr
                v-for="s in filteredSoftware"
                :key="String(swName(s)) + ':' + String(swVersion(s))"
                class="border-t border-slate-800/60"
              >
                <td class="px-3 py-2 font-mono text-slate-200 break-all">
                  {{ swName(s) || "未采集" }}
                </td>
                <td class="px-3 py-2 font-mono text-slate-400 break-all">
                  {{ swVersion(s) || "-" }}
                </td>
                <td class="px-3 py-2 font-mono text-slate-500 break-all">
                  {{ swPublisher(s) || "-" }}
                </td>
              </tr>
            </tbody>
          </table>
        </div>
        <div v-else class="flex-1 min-h-0 flex items-center justify-center text-slate-500 text-sm">
          无软件数据
        </div>
      </div>

      <!-- Network Connections -->
      <div v-if="activeTab==='network'" class="bg-slate-900/50 rounded-lg border border-slate-800 p-3 h-[70vh] flex flex-col">
        <div class="flex items-center justify-between mb-2">
          <div class="text-sm font-semibold">网络连接</div>
          <div class="text-xs text-slate-500">共 {{ connections.length }} 条</div>
        </div>
        <div v-if="connections.length" class="flex-1 min-h-0 overflow-auto">
          <table class="min-w-full text-xs">
            <thead class="bg-slate-800 text-slate-300">
              <tr>
                <th class="px-3 py-2 text-left">协议</th>
                <th class="px-3 py-2 text-left">状态</th>
                <th class="px-3 py-2 text-left">本地</th>
                <th class="px-3 py-2 text-left">远程</th>
                <th class="px-3 py-2 text-left">PID</th>
              </tr>
            </thead>
            <tbody class="bg-slate-900/30">
              <tr v-for="(c, i) in connections.slice(0, 200)" :key="i" class="border-t border-slate-800/60">
                <td class="px-3 py-2 font-mono text-slate-100">{{ c.protocol || c.Protocol || '-' }}</td>
                <td class="px-3 py-2 font-mono text-slate-200">{{ c.state || c.State || '-' }}</td>
                <td class="px-3 py-2 font-mono text-slate-400 break-all">
                  {{ (c.local_addr || c.LocalAddr || '-') + ':' + (c.local_port || c.LocalPort || '-') }}
                </td>
                <td class="px-3 py-2 font-mono text-slate-400 break-all">
                  {{ (c.remote_addr || c.RemoteAddr || '-') + ':' + (c.remote_port || c.RemotePort || '-') }}
                </td>
                <td class="px-3 py-2 font-mono text-slate-500">{{ c.pid || c.PID || '-' }}</td>
              </tr>
            </tbody>
          </table>
          <div v-if="connections.length > 200" class="text-xs text-slate-500 mt-2">仅展示前 200 条（避免卡顿）</div>
        </div>
        <div v-else class="flex-1 min-h-0 flex items-center justify-center text-slate-500 text-sm">等待 Agent 上报 network_connections...</div>
      </div>
    </div>
  </div>
</template>

