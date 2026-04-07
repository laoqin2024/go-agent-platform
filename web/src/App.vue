<script setup lang="ts">
import { computed, onMounted, onUnmounted, reactive, ref, watch } from "vue";
import { useRoute, useRouter } from "vue-router";
import DeviceList from "./components/DeviceList.vue";
import DeviceDetail from "./components/DeviceDetail.vue";
import AssetsHub from "./views/AssetsHub.vue";
import { request } from "./utils/request";
import { useWebSocket, type WSConnectionStatus } from "./composables/useWebSocket";

type DeviceInfo = {
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
  agent_version?: string;
  first_seen_at?: number;
  agent_version_updated_at?: number;
};

type CurrentDevice = {
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

const devices = ref<DeviceInfo[]>([]);
const route = useRoute();
const router = useRouter();
const isAssetsView = computed(() => route.path.startsWith("/assets"));
const selectedDeviceId = ref<string>("");
const userSelected = ref(false);

// Active device reference (required by detail pane)
const activeId = selectedDeviceId;
const activeDevice = computed(() => devices.value.find((d) => d.device_id === activeId.value));

const currentDevice = reactive<CurrentDevice>({
  deviceId: selectedDeviceId.value || "",
  processes: [],
  softwareList: [],
  hostMetrics: null,
  hardwareDetails: null,
  networkConnections: null,
  securitySnapshot: null,
  serviceSnapshot: null,
  processSnapshot: null,
  softwareInventory: null,
  updatedAtSec: 0,
  snapshotFound: false,
  wsReceived: false,
});

let seq = 0;
let metricsTick: number | null = null;
let deviceOnlineTick: number | null = null;
const lastHostMetricsAtMs = ref<number>(0);
const DEVICE_ONLINE_DOT_TIMEOUT_SEC = 60; // 心跳超过此阈值：左侧列表绿色点变为离线
let wsGlobal: WebSocket | null = null;
const lastUpdateTrigger = ref(0);

function syncDeviceOnlineDots() {
  const nowSec = Math.floor(Date.now() / 1000);
  for (const d of devices.value) {
    const t = typeof d.updated_at === "number" ? d.updated_at : 0;
    if (!t || t <= 0) {
      d.online = false;
      continue;
    }
    const ageSec = nowSec - t;
    d.online = ageSec >= 0 && ageSec < DEVICE_ONLINE_DOT_TIMEOUT_SEC;
  }
}

function firstProcFeature(arr: any[]) {
  const p = arr?.[0];
  if (!p) return "";
  const pid = p?.PID ?? p?.pid ?? "";
  const name = p?.Name ?? p?.name ?? "";
  return `${pid}|${name}`;
}

function firstSoftwareFeature(arr: any[]) {
  const s = arr?.[0];
  if (!s) return "";
  const name = s?.Name ?? s?.name ?? "";
  const version = s?.Version ?? s?.version ?? "";
  return `${name}|${version}`;
}

function normalizeArray(v: any): any[] {
  if (!v) return [];
  if (Array.isArray(v)) return v;
  // Some backends might send JSON string.
  if (typeof v === "string") {
    try {
      const parsed = JSON.parse(v);
      if (Array.isArray(parsed)) return parsed;
    } catch {
      // ignore
    }
  }
  return [];
}

function normalizeObject(v: any): any | null {
  if (!v) return null;
  if (typeof v === "string") {
    try {
      return JSON.parse(v);
    } catch {
      return null;
    }
  }
  if (typeof v === "object") return v;
  return null;
}

function toNum(v: any): number {
  const n = Number(v ?? 0);
  return Number.isFinite(n) ? n : 0;
}

function pickFirstIP(hostMetricsObj: any): string {
  if (!hostMetricsObj || typeof hostMetricsObj !== "object") return "";
  const direct = hostMetricsObj.ip ?? hostMetricsObj.local_ip ?? hostMetricsObj.localIp ?? hostMetricsObj.IP;
  if (typeof direct === "string" && direct.trim()) return direct.trim();
  const ips = hostMetricsObj.ips;
  if (Array.isArray(ips) && typeof ips[0] === "string") return String(ips[0]).trim();
  const net = hostMetricsObj.Network;
  if (net && typeof net === "object" && Array.isArray(net.LocalIPs) && typeof net.LocalIPs[0] === "string") {
    return String(net.LocalIPs[0]).trim();
  }
  return "";
}

function updateDeviceMetaFromPayload(deviceId: string, payload: any) {
  const idx = devices.value.findIndex((d) => d.device_id === deviceId);
  if (idx < 0) return;

  const next = { ...devices.value[idx] };
  if (payload?.host_metrics) {
    const hm = normalizeObject(payload.host_metrics);
    if (hm) {
      if (typeof hm.hostname === "string" && hm.hostname.trim()) next.hostname = hm.hostname.trim();
      if (typeof hm.Hostname === "string" && hm.Hostname.trim() && !next.hostname) next.hostname = hm.Hostname.trim();
      const ip = pickFirstIP(hm);
      if (ip) next.ip = ip;
      next.cpu_percent = toNum(hm?.cpu_total_percent ?? hm?.CPU?.Total);
      next.mem_used_percent = toNum(hm?.memory_used_percent ?? hm?.Memory?.UsedPercent);
      if (typeof hm?.agent_version === "string" && hm.agent_version.trim()) next.agent_version = hm.agent_version.trim();
      if (typeof hm?.AgentVersion === "string" && hm.AgentVersion.trim() && !next.agent_version) next.agent_version = hm.AgentVersion.trim();
    }
  }
  if (payload?.hardware_details) {
    const hd = normalizeObject(payload.hardware_details);
    if (hd) {
      if (typeof hd.os === "string" && hd.os.trim()) next.os = hd.os.trim();
      if (typeof hd.OS === "string" && hd.OS.trim() && !next.os) next.os = hd.OS.trim();
    }
  }

  devices.value[idx] = next;
}

function hasCriticalFromSecuritySnapshotObj(ss: any): boolean {
  if (!ss || typeof ss !== "object") return false;
  const arr = Array.isArray(ss?.listening) ? ss.listening : Array.isArray(ss?.Listening) ? ss.Listening : [];
  for (const it of arr) {
    const scope = String(it?.scope ?? it?.Scope ?? "").toLowerCase();
    if (scope !== "public") continue;
    const isHigh = !!(it?.is_high_risk ?? it?.IsHighRisk);
    if (isHigh) return true;
    const port = Number(it?.port ?? it?.Port ?? 0);
    if ([22, 3389, 445, 3306, 6379].includes(port)) return true;
  }
  return false;
}

const ws = useWebSocket((data: any, raw: any) => {
  // Raw payload helps verify whether WS proxy/route is working and messages are arriving.
  // eslint-disable-next-line no-console
  console.log("WebSocket 收到原始数据:", raw);
  const incomingDeviceId =
    data?.device_id ?? data?.deviceId ?? data?.DeviceID ?? currentDevice.deviceId;
  if (!incomingDeviceId) return;

  // Register device id for the left panel so the user can switch.
  if (!devices.value.some((d) => d.device_id === incomingDeviceId)) {
    devices.value.push({ device_id: incomingDeviceId, hostname: "", os: "", updated_at: 0, online: false });
  }

  // If user hasn't manually selected and no device selected yet, auto-switch.
  if (incomingDeviceId !== currentDevice.deviceId) {
    const canAutoSwitch = !userSelected.value && !currentDevice.deviceId;
    if (canAutoSwitch) {
      switchDevice(incomingDeviceId);
    }
    if (incomingDeviceId !== currentDevice.deviceId) return;
  }

  const hasProcessesField = "processes" in data || "Processes" in data;
  const hasSoftwareField = "software_list" in data || "softwareList" in data;

  // Update only processes/software_list arrays for performance.
  const nextProcesses = hasProcessesField
    ? normalizeArray(data?.processes ?? data?.Processes)
    : currentDevice.processes || [];
  const nextSoftware = hasSoftwareField
    ? normalizeArray(data?.software_list ?? data?.softwareList)
    : currentDevice.softwareList || [];

  const prevProcesses = currentDevice.processes || [];
  const prevSoftware = currentDevice.softwareList || [];

  if (hasProcessesField) {
    const prevLen = prevProcesses.length;
    const nextLen = nextProcesses.length;
    const sameLenAndFirst =
      prevLen === nextLen && firstProcFeature(prevProcesses) === firstProcFeature(nextProcesses);
    if (!sameLenAndFirst) currentDevice.processes = nextProcesses;
  }

  if (hasSoftwareField) {
    const prevLen = prevSoftware.length;
    const nextLen = nextSoftware.length;
    const sameLenAndFirst =
      prevLen === nextLen &&
      firstSoftwareFeature(prevSoftware) === firstSoftwareFeature(nextSoftware);
    if (!sameLenAndFirst) currentDevice.softwareList = nextSoftware;
  }

  // Mark WS received for this device, even if only one side updated.
  currentDevice.wsReceived = true;

  // Extended dashboard fields (accept snake_case and camelCase)
  if ("host_metrics" in data || "hostMetrics" in data) {
    currentDevice.hostMetrics = normalizeObject((data as any).host_metrics ?? (data as any).hostMetrics);
    lastHostMetricsAtMs.value = Date.now();
  }
  if ("hardware_details" in data || "hardwareDetails" in data) {
    currentDevice.hardwareDetails = normalizeObject((data as any).hardware_details ?? (data as any).hardwareDetails);
  }
  if ("network_connections" in data || "networkConnections" in data) {
    currentDevice.networkConnections = normalizeObject((data as any).network_connections ?? (data as any).networkConnections);
  }
  if ("security_snapshot" in data || "securitySnapshot" in data) {
    currentDevice.securitySnapshot = normalizeObject((data as any).security_snapshot ?? (data as any).securitySnapshot);
  }
  if ("service_snapshot" in data || "serviceSnapshot" in data) {
    currentDevice.serviceSnapshot = normalizeObject((data as any).service_snapshot ?? (data as any).serviceSnapshot);
  }
  if ("process_snapshot" in data || "processSnapshot" in data) {
    currentDevice.processSnapshot = normalizeObject((data as any).process_snapshot ?? (data as any).processSnapshot);
  }
  if ("software_inventory" in data || "softwareInventory" in data) {
    currentDevice.softwareInventory = normalizeObject((data as any).software_inventory ?? (data as any).softwareInventory);
  }

  const updatedAt =
    data?.updated_at ?? data?.updatedAt ?? data?.UpdatedAtSec ?? 0;
  if (typeof updatedAt === "number" && updatedAt > 0) {
    currentDevice.updatedAtSec = updatedAt;
    const idx = devices.value.findIndex((d) => d.device_id === incomingDeviceId);
    if (idx >= 0) {
      devices.value[idx] = { ...devices.value[idx], updated_at: updatedAt, online: true };
    }
  }

  // Keep device list meta fresh if agent pushes host_metrics/hardware_details via WS.
  if ("host_metrics" in data || "hardware_details" in data || "hostMetrics" in data || "hardwareDetails" in data) {
    updateDeviceMetaFromPayload(incomingDeviceId, data);
  }
  // Keep risk flags fresh if agent pushes security_snapshot via WS.
  if ("security_snapshot" in data || "securitySnapshot" in data) {
    const idx = devices.value.findIndex((d) => d.device_id === incomingDeviceId);
    if (idx >= 0) {
      const ss = normalizeObject((data as any).security_snapshot ?? (data as any).securitySnapshot);
      const hasCritical = hasCriticalFromSecuritySnapshotObj(ss);
      const prev = devices.value[idx];
      const next = { ...prev };
      next.has_critical_risk = hasCritical;
      next.had_critical_risk = !!prev.had_critical_risk || hasCritical;
      if (hasCritical) next.last_critical_at = Math.floor(Date.now() / 1000);
      devices.value[idx] = next;
      lastUpdateTrigger.value++;
    }
  }
});

const connectionStatus = ws.status;

function connectGlobal() {
  try {
    // eslint-disable-next-line no-console
    console.error("DEBUG: Starting Global WS connection process...");
    const url = `ws://${window.location.hostname}:8080/global_status`;
    // eslint-disable-next-line no-console
    console.log("Attempting Global WS connection to:", url);
    wsGlobal = new WebSocket(url);
    wsGlobal.onopen = () => {
      // eslint-disable-next-line no-console
      console.log("[ws-global] open");
    };
    wsGlobal.onmessage = (ev) => {
      try {
        const msg = typeof ev.data === "string" ? JSON.parse(ev.data) : ev.data;
        // eslint-disable-next-line no-console
        console.log("[GlobalWS] Received:", msg);
        if (msg?.type === "device_update" && typeof msg?.device_id === "string") {
          const id = msg.device_id as string;
          const online = !!msg.online;
          const idx = devices.value.findIndex((d) => d.device_id === id);
          if (idx >= 0) {
            const next = { ...devices.value[idx], online, updated_at: online ? Math.floor(Date.now() / 1000) : devices.value[idx].updated_at };
            devices.value[idx] = next;
            devices.value = [...devices.value];
          } else {
            const added = { device_id: id, hostname: "", os: "", updated_at: online ? Math.floor(Date.now() / 1000) : 0, online };
            devices.value = [...devices.value, added];
          }
          lastUpdateTrigger.value++;
        } else if (msg?.type === "device_snapshot" && Array.isArray(msg?.devices)) {
          // 强制整体替换，触发列表重绘
          // eslint-disable-next-line no-console
          console.log("Processing snapshot for", (msg.devices as any[]).length, "devices");
          devices.value = [...(msg.devices as any[])];
          // 额外保险：强制触发计算属性
          lastUpdateTrigger.value++;
          // 调试：确认当前列表长度
          // eslint-disable-next-line no-console
          console.log("Current devices in list:", devices.value.length);
        }
      } catch {
        // ignore
      }
    };
    wsGlobal.onclose = () => {
      // eslint-disable-next-line no-console
      console.log("[ws-global] close");
      wsGlobal = null;
    };
    wsGlobal.onerror = () => {
      try { wsGlobal?.close(); } catch {}
    };
  } catch {
    // ignore
  }
}

async function fetchSnapshot(deviceId: string, mySeq: number) {
  try {
    const resp = await request.get(`/api/v1/device/${encodeURIComponent(deviceId)}/snapshot`);
    if (mySeq !== seq) return;

    const processes = normalizeArray(resp.data?.processes ?? resp.data?.Processes);
    const softwareList = normalizeArray(resp.data?.software_list ?? resp.data?.softwareList);

    currentDevice.processes = processes;
    currentDevice.softwareList = softwareList;
    currentDevice.hostMetrics = normalizeObject(resp.data?.host_metrics);
    if (currentDevice.hostMetrics) {
      lastHostMetricsAtMs.value = Date.now();
    }
    currentDevice.hardwareDetails = normalizeObject(resp.data?.hardware_details);
    currentDevice.networkConnections = normalizeObject(resp.data?.network_connections);
    currentDevice.securitySnapshot = normalizeObject(resp.data?.security_snapshot);
    currentDevice.serviceSnapshot = normalizeObject(resp.data?.service_snapshot);
    currentDevice.processSnapshot = normalizeObject(resp.data?.process_snapshot);
    currentDevice.softwareInventory = normalizeObject(resp.data?.software_inventory);
    currentDevice.snapshotFound = true;
  } catch (err: any) {
    const status = err?.response?.status;
    if (status === 404) {
      // Snapshot not found: wait for first WS push.
      currentDevice.processes = [];
      currentDevice.softwareList = [];
      currentDevice.snapshotFound = false;
      return;
    }
    // Other errors: keep UI stable, don't throw.
    currentDevice.snapshotFound = false;
  }
}

function connectWebSocket(deviceId: string) {
  ws.disconnect();
  ws.connect(deviceId);
}

function retryWS() {
  if (!currentDevice.deviceId) return;
  // Manual reconnect: reconnect WS + immediately re-fetch snapshot so UI updates faster.
  connectWebSocket(currentDevice.deviceId);
  seq++;
  const mySeq = seq;
  void fetchSnapshot(currentDevice.deviceId, mySeq);
}

function switchDevice(deviceId: string) {
  if (!deviceId) return;
  if (deviceId === currentDevice.deviceId) return;

  selectedDeviceId.value = deviceId;
  currentDevice.deviceId = deviceId;
  currentDevice.updatedAtSec = 0;
  currentDevice.processes = [];
  currentDevice.softwareList = [];
  currentDevice.hostMetrics = null;
  currentDevice.hardwareDetails = null;
  currentDevice.networkConnections = null;
  currentDevice.securitySnapshot = null;
  currentDevice.serviceSnapshot = null;
  currentDevice.processSnapshot = null;
  currentDevice.softwareInventory = null;
  currentDevice.wsReceived = false;
  currentDevice.snapshotFound = false;

  seq++;
  const mySeq = seq;

  // Dual insurance flow:
  // 1) start fetchSnapshot immediately
  void fetchSnapshot(deviceId, mySeq);
  // 2) immediately switch WS subscription
  connectWebSocket(deviceId);
}

function handleUserSelect(deviceId: string) {
  if (!deviceId) return;
  userSelected.value = true;
  switchDevice(deviceId);
}

function goMonitor() {
  void router.push({ path: "/" });
}

function goAssets() {
  void router.push({ path: "/assets" });
}

// If we reconnect after backend restart and WS hasn't pushed yet, re-fetch once.
watch(
  connectionStatus,
  (s: WSConnectionStatus) => {
    if (s === "online" && currentDevice.deviceId && (!currentDevice.wsReceived || !currentDevice.snapshotFound)) {
      seq++;
      const mySeq = seq;
      void fetchSnapshot(currentDevice.deviceId, mySeq);
    }
  }
);

watch(
  () => route.query.device,
  (v) => {
    const deviceId = String(v || "").trim();
    if (!deviceId) return;
    if (isAssetsView.value) return;
    handleUserSelect(deviceId);
  },
  { immediate: true }
);

// Lightweight auto-retry: if WS在线但长时间未收到 host_metrics，定时通过 HTTP 再拉一次快照补齐。
function ensureMetricsPolling() {
  if (metricsTick != null) {
    window.clearInterval(metricsTick);
    metricsTick = null;
  }
  metricsTick = window.setInterval(() => {
    if (!currentDevice.deviceId) return;
    // If no metrics ever, or超过10秒未更新，则补拉一次。
    const stale = Date.now() - (lastHostMetricsAtMs.value || 0);
    if (stale > 10_000) {
      seq++;
      const mySeq = seq;
      void fetchSnapshot(currentDevice.deviceId, mySeq);
    }
  }, 7000);
}

onMounted(() => {
  // Connect global WS ASAP
  connectGlobal();

  ensureMetricsPolling();

  // Keep the left device list "online" dot in sync with last snapshot time.
  deviceOnlineTick = window.setInterval(() => {
    syncDeviceOnlineDots();
  }, 3000);
});

onUnmounted(() => {
  if (metricsTick != null) {
    window.clearInterval(metricsTick);
    metricsTick = null;
  }
  if (deviceOnlineTick != null) {
    window.clearInterval(deviceOnlineTick);
    deviceOnlineTick = null;
  }
  try { wsGlobal?.close(); } catch {}
  wsGlobal = null;
});

// Initial load: fetch device ids from backend so LAN users don't start from an empty list.
onMounted(async () => {
  try {
    const resp = await request.get(`/api/v1/devices`);
    const list: DeviceInfo[] = resp.data?.devices ?? [];
    if (Array.isArray(list) && list.length > 0) {
      devices.value = list;
      syncDeviceOnlineDots();
      const firstId = list[0]?.device_id ?? "";
      if (firstId) {
        selectedDeviceId.value = firstId;
        if (!userSelected.value) switchDevice(firstId);
      }
      return;
    }
  } catch {
    // Ignore; fallback to the seed device.
  }

  // No devices yet: keep empty selection; wait for WS/global snapshot.
});
</script>

<template>
  <div class="h-screen w-full flex flex-col text-slate-200 overflow-hidden bg-slate-950">
    <header class="h-12 shrink-0 border-b border-slate-800 px-4 flex items-center justify-between bg-slate-950/95">
      <div class="text-sm tracking-wide text-slate-300">Go-Agent 控制台</div>
      <div class="flex items-center gap-2">
        <button class="px-3 py-1.5 text-xs rounded border"
          :class="!isAssetsView ? 'border-cyan-600 bg-cyan-500/10 text-cyan-300' : 'border-slate-700 text-slate-300 hover:bg-slate-800/50'"
          @click="goMonitor">实时监控</button>
        <button class="px-3 py-1.5 text-xs rounded border"
          :class="isAssetsView ? 'border-cyan-600 bg-cyan-500/10 text-cyan-300' : 'border-slate-700 text-slate-300 hover:bg-slate-800/50'"
          @click="goAssets">资产中心</button>
      </div>
    </header>

    <AssetsHub v-if="isAssetsView" :devices="devices" />

    <div v-else class="flex-1 w-full flex overflow-hidden">
      <aside class="sidebar">
        <DeviceList
          :devices="devices"
          :selected-device-id="selectedDeviceId"
          :update-trigger="lastUpdateTrigger"
          @select="handleUserSelect"
        />
      </aside>

      <main class="flex-1 h-full overflow-hidden bg-slate-950/60 relative">
        <div
          v-if="connectionStatus !== 'online'"
          class="toast-banner"
          :class="{
            'banner-warning': connectionStatus === 'reconnecting',
            'banner-danger': connectionStatus === 'offline'
          }"
        >
          <div class="flex items-center gap-2">
            <span class="w-2 h-2 rounded-full"
                  :class="{
                    'bg-state-warning': connectionStatus === 'reconnecting',
                    'bg-state-danger' : connectionStatus === 'offline'
                  }"></span>
            <span class="font-medium">
              {{ connectionStatus === 'reconnecting' ? '正在重连 WebSocket' : 'WebSocket 已断开' }}
            </span>
            <button
              class="ml-3 text-[11px] px-2 py-0.5 rounded border border-slate-700 bg-slate-900/40 hover:bg-slate-900/70"
              @click="retryWS"
            >立即重试</button>
          </div>
        </div>

        <DeviceDetail
          :device="currentDevice"
          :ws-status="connectionStatus"
          @retry-ws="retryWS"
        />
      </main>
    </div>
  </div>
</template>

