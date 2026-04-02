<script setup lang="ts">
import { onMounted, reactive, ref, watch } from "vue";
import DeviceList from "./components/DeviceList.vue";
import DeviceDetail from "./components/DeviceDetail.vue";
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

const seedDevices: DeviceInfo[] = [
  { device_id: "demo-fingerprint-00000000", hostname: "Demo", os: "", updated_at: 0, online: false },
];
const devices = ref<DeviceInfo[]>(seedDevices);
const selectedDeviceId = ref<string>(seedDevices[0]?.device_id ?? "");
const userSelected = ref(false);

const currentDevice = reactive<CurrentDevice>({
  deviceId: selectedDeviceId.value,
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

const ws = useWebSocket((data: any) => {
  const incomingDeviceId =
    data?.device_id ?? data?.deviceId ?? data?.DeviceID ?? currentDevice.deviceId;
  if (!incomingDeviceId) return;

  // Register device id for the left panel so the user can switch.
  if (!devices.value.some((d) => d.device_id === incomingDeviceId)) {
    devices.value.push({ device_id: incomingDeviceId, hostname: "", os: "", updated_at: 0, online: false });
  }

  // If user hasn't manually selected and we're still on the seed device, auto-switch.
  if (incomingDeviceId !== currentDevice.deviceId) {
    const seed = seedDevices[0]?.device_id ?? "";
    const canAutoSwitch = !userSelected.value && currentDevice.deviceId === seed;
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

  // Extended dashboard fields (debug_server compatible)
  if ("host_metrics" in data) currentDevice.hostMetrics = normalizeObject(data.host_metrics);
  if ("hardware_details" in data) currentDevice.hardwareDetails = normalizeObject(data.hardware_details);
  if ("network_connections" in data) currentDevice.networkConnections = normalizeObject(data.network_connections);
  if ("security_snapshot" in data) currentDevice.securitySnapshot = normalizeObject(data.security_snapshot);
  if ("service_snapshot" in data) currentDevice.serviceSnapshot = normalizeObject(data.service_snapshot);
  if ("process_snapshot" in data) currentDevice.processSnapshot = normalizeObject(data.process_snapshot);
  if ("software_inventory" in data) currentDevice.softwareInventory = normalizeObject(data.software_inventory);

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
  if ("host_metrics" in data || "hardware_details" in data) {
    updateDeviceMetaFromPayload(incomingDeviceId, data);
  }
});

const connectionStatus = ws.status;

async function fetchSnapshot(deviceId: string, mySeq: number) {
  try {
    const resp = await request.get(`/api/v1/device/${encodeURIComponent(deviceId)}/snapshot`);
    if (mySeq !== seq) return;

    const processes = normalizeArray(resp.data?.processes ?? resp.data?.Processes);
    const softwareList = normalizeArray(resp.data?.software_list ?? resp.data?.softwareList);

    currentDevice.processes = processes;
    currentDevice.softwareList = softwareList;
    currentDevice.hostMetrics = normalizeObject(resp.data?.host_metrics);
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

// Initial load: fetch device ids from backend so LAN users don't start from an empty list.
onMounted(async () => {
  try {
    const resp = await request.get(`/api/v1/devices`);
    const list: DeviceInfo[] = resp.data?.devices ?? [];
    if (Array.isArray(list) && list.length > 0) {
      devices.value = list;
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

  // Fallback: try seed device id if exists.
  if (selectedDeviceId.value) switchDevice(selectedDeviceId.value);
});
</script>

<template>
  <div class="min-h-screen flex bg-slate-950 text-slate-100">
    <aside class="w-full lg:w-80">
      <DeviceList
        :devices="devices"
        :selected-device-id="selectedDeviceId"
        @select="handleUserSelect"
      />
    </aside>

    <main class="flex-1 h-screen overflow-hidden">
      <DeviceDetail :device="currentDevice" :ws-status="connectionStatus" />
    </main>
  </div>
</template>

