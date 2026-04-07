<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref, watch } from "vue";
import type { WSConnectionStatus } from "../composables/useWebSocket";
import { formatSpeed } from "../utils/format";

type Device = {
  deviceId: string;
  device_id?: string;
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

const emit = defineEmits<{
  (e: "retry-ws"): void;
}>();

const manualRetrying = ref(false);

function onRetryWs() {
  // Immediate UI feedback; actual reconnect is handled by App.vue.
  manualRetrying.value = true;
  emit("retry-ws");
}

const HEARTBEAT_RECONNECT_TIMEOUT_SEC = 60; // 心跳超过此阈值：即使 WS 仍在线，也认为需要重连
const HEARTBEAT_OFFLINE_TIMEOUT_SEC = 120; // 超过此阈值：认为离线

const statusText = computed(() => {
  if (effectiveWsStatus.value === "online") return "在线";
  if (effectiveWsStatus.value === "reconnecting") return manualRetrying.value ? "重连中（手动重试）" : "重连中（心跳超时）";
  return manualRetrying.value ? "离线（手动重试中）" : "离线（心跳超时）";
});

const statusClass = computed(() => {
  if (effectiveWsStatus.value === "online") return "bg-emerald-500/90";
  if (effectiveWsStatus.value === "reconnecting") return "bg-amber-500/90";
  return "bg-red-500/90";
});

const selectedDeviceId = computed(() => props.device?.deviceId || (props.device as any)?.device_id || "");

const showEmptyWaiting = computed(() => {
  if (!selectedDeviceId.value) return false;
  const pEmpty = !(props.device as any).processes || (props.device as any).processes.length === 0;
  const sEmpty = !props.device.softwareList || props.device.softwareList.length === 0;
  const hasAnyPanel =
    !!props.device.hostMetrics ||
    !!(props.device as any).host_metrics ||
    !!props.device.hardwareDetails ||
    !!props.device.networkConnections ||
    !!props.device.securitySnapshot ||
    !!props.device.serviceSnapshot ||
    !!props.device.processSnapshot ||
    !!props.device.softwareInventory;
  return pEmpty && sEmpty && !hasAnyPanel && !props.device.wsReceived;
});

const hasSelectedDevice = computed(() => !!selectedDeviceId.value);
const isWaitingRealtime = computed(() => {
  if (!hasSelectedDevice.value) return false;
  // Only used for lightweight in-panel hint; do not block the whole page.
  const cpu = Number(cpuTotal.value ?? 0);
  const mem = Number(memUsedPercent.value ?? 0);
  const hasAnyHost = !!host.value && typeof host.value === "object";
  const hasSomeMetrics = hasAnyHost && (cpu > 0 || mem > 0);
  return !hasSomeMetrics && hasAnyHost;
});

watch(
  () => props.device,
  (newVal: any) => {
    // eslint-disable-next-line no-console
    console.log(
      "DeviceDetail received new data:",
      newVal?.deviceId,
      newVal?.services?.length ?? newVal?.hostMetrics?.services?.length ?? 0
    );
  },
  { immediate: true, deep: true }
);

const procQuery = ref("");
const swQuery = ref("");
const swRiskOnly = ref(false);
const activeTab = ref<"overview" | "network" | "hardware" | "compute" | "security" | "software">("overview");

const VULNERABILITY_RULES: Array<{ name: string; version_lt?: string; version_eq?: string }> = [
  { name: "OpenSSL", version_lt: "3.0.7" },
  { name: "log4j", version_lt: "2.17.1" },
  // Temporary test rule for validating warning flow in UI.
  { name: "Git", version_eq: "2.53.0" },
];

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
function procUsername(p: any) {
  return p?.Username ?? p?.username ?? "";
}
function procListenPorts(p: any): number[] {
  const v = p?.ListenPorts ?? p?.listening_ports ?? p?.listeningPorts ?? [];
  return Array.isArray(v) ? (v as any[]).map((x) => Number(x)).filter((n) => Number.isFinite(n) && n > 0) as number[] : [];
}
function procServiceName(p: any) {
  return p?.ServiceName ?? p?.service_name ?? "";
}
function procRemark(p: any) {
  const svc = String(procServiceName(p) || "").trim();
  if (svc) return svc;
  const exe = String(procExecPath(p) || "").trim();
  if (!exe) return "-";
  const parts = exe.split(/[\\/]/).filter(Boolean);
  const base = parts.length ? parts[parts.length - 1] : exe;
  return base || shorten(exe, 28);
}
function isWebPort(port: number) {
  return [80, 443, 8080, 8443, 3000, 5000, 5173].includes(Number(port));
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

// Backward-compat shim: delegate to global formatter for consistent UI
function formatRateBytesPerSec(bps: any) {
  return formatSpeed(bps);
}

function percent(v: any) {
  const n = Number(v ?? 0);
  if (Number.isNaN(n)) return "0.0";
  return n.toFixed(1);
}

function roundInt(v: any) {
  const n = Number(v);
  if (!Number.isFinite(n)) return 0;
  return Math.round(n);
}

function formatPercent2(v: any) {
  const n = Number(v);
  if (!Number.isFinite(n)) return "-";
  return `${n.toFixed(2)}%`;
}

const hostMetrics = computed(() => {
  const d: any = props.device as any;
  if (!d) return null;
  // Auto-adapt nesting and key style.
  return d?.host_metrics ?? d?.hostMetrics ?? (d?.cpu_percent !== undefined ? d : null);
});
const host = hostMetrics;
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

const cpuTotal = computed(
  () => host.value?.CPU?.Total ?? host.value?.cpu_total_percent ?? host.value?.cpu_percent ?? 0
);
const memUsedPercent = computed(() => host.value?.Memory?.UsedPercent ?? host.value?.memory_used_percent ?? 0);
const memTotal = computed(() => host.value?.Memory?.Total ?? host.value?.memory_total_bytes ?? 0);
const memUsed = computed(() => host.value?.Memory?.Used ?? host.value?.memory_used_bytes ?? 0);
const diskUsedPercent = computed(() => host.value?.Disk?.UsedPercent ?? host.value?.disk_used_percent ?? 0);
const netSent = computed(() => host.value?.Network?.BytesSent ?? host.value?.network_bytes_sent ?? 0);
const netRecv = computed(() => host.value?.Network?.BytesRecv ?? host.value?.network_bytes_recv ?? 0);

// Per-interface network counters (host_metrics.network_interfaces)
const netInterfaces = computed(() =>
  asArray(
    host.value?.network_interfaces ??
      host.value?.NetworkInterfaces ??
      host.value?.networkInterfaces ??
      host.value?.network_interfaces_list ??
      []
  )
);

// Fixed ordering + smart filtering of interfaces (macOS/Windows/Linux)
const filteredSortedIfaces = computed(() => {
  const base = netInterfaces.value as any[];
  if (!Array.isArray(base) || base.length === 0) return [];
  const isVirtual = (name: string) => {
    const n = (name || "").toLowerCase();
    // virtual/loopback/common tunnels on macOS/Windows
    return (
      n.startsWith("lo") ||
      n.startsWith("utun") ||
      n.startsWith("gif") ||
      n.startsWith("stf") ||
      n.startsWith("llw") ||
      n.startsWith("awdl") ||
      n.includes("vbox") ||
      n.includes("vmnet")
    );
  };
  const isPreferredName = (name: string) => {
    const n = (name || "").toLowerCase();
    return n.startsWith("en") || n.startsWith("eth") || n.includes("wlan") || n.includes("ethernet");
  };
  const counters = (it: any) => {
    const sent = Number(it?.bytes_sent ?? it?.BytesSent ?? it?.bytesSent ?? 0);
    const recv = Number(it?.bytes_recv ?? it?.BytesRecv ?? it?.bytesRecv ?? 0);
    return { sent: Number.isFinite(sent) ? sent : 0, recv: Number.isFinite(recv) ? recv : 0 };
  };

  // 1) drop virtuals
  let arr = base.filter((it) => {
    const name = String(it?.name ?? it?.Name ?? "");
    if (!name) return false;
    return !isVirtual(name);
  });

  // 2) keep if has traffic or preferred physical/wireless naming
  const filtered = arr.filter((it) => {
    const name = String(it?.name ?? it?.Name ?? "");
    const { sent, recv } = counters(it);
    const hasTraffic = sent + recv > 0;
    return hasTraffic || isPreferredName(name);
  });

  let out = filtered;
  // 3) fallback: if nothing left, keep the interface with max total bytes
  if (out.length === 0) {
    let best: any | null = null;
    let bestTotal = -1;
    for (const it of arr.length ? arr : base) {
      const { sent, recv } = counters(it);
      const total = sent + recv;
      if (total > bestTotal) {
        bestTotal = total;
        best = it;
      }
    }
    if (best) out = [best];
  }

  // 4) stable sort by name A-Z
  out.sort((a: any, b: any) => {
    const an = String(a?.name ?? a?.Name ?? "");
    const bn = String(b?.name ?? b?.Name ?? "");
    return an.localeCompare(bn, "en");
  });
  return out;
});

const pingLatencyMs = computed(() => {
  const v =
    host.value?.ping_latency_ms ??
    host.value?.PingLatencyMs ??
    host.value?.pingLatencyMs ??
    host.value?.pingLatencyMsMs ??
    0;
  const n = Number(v || 0);
  return Number.isFinite(n) && n > 0 ? n : 0;
});

type NetIfaceCountersSample = {
  t: number;
  sent: number;
  recv: number;
  packetsSent: number;
  packetsRecv: number;
  dropIn: number;
  dropOut: number;
};

type NetIfaceRateUI = {
  txRateBps: number; // bytes/s
  rxRateBps: number; // bytes/s
  dropDelta: number; // packets lost since last sample
  dropRatePct: number; // drop / packets delta
};

const ifaceLastSample = ref<Record<string, NetIfaceCountersSample>>({});
const ifaceRates = ref<Record<string, NetIfaceRateUI>>({});
const overallDropRatePct = ref(0);
// Security audit panel derived caches (cleared on device switch)
const securityListeningRowsCache = ref<any[]>([]);
const securityPanelLoading = ref(true);
const showSecurityContextMenu = ref(false);
const securityContextMenuPos = ref<{ x: number; y: number }>({ x: 0, y: 0 });
const securityContextRow = ref<any | null>(null);

// Software assets panel derived caches (cleared on device switch)
const softwareListCache = ref<any[]>([]);
const softwarePanelLoading = ref(true);

// Per-NIC short history for baseline (5分钟)估计
type IfaceRatePoint = { t: number; tx: number; rx: number };
const ifaceRateHistory = ref<Record<string, IfaceRatePoint[]>>({});

const NET_BASELINE_MS = 5 * 60 * 1000;
const instantTotalRateBps = computed(() => Number(txRate.value || 0) + Number(rxRate.value || 0));
const avgTotalRateBps5m = computed(() => {
  const now = nowMs.value || Date.now();
  const list = rateHistory.value.filter((r) => r.t >= now - NET_BASELINE_MS);
  if (!list.length) return 0;
  const sum = list.reduce((acc, r) => acc + Number(r.tx || 0) + Number(r.rx || 0), 0);
  return sum / list.length;
});
const throughputSpike = computed(() => {
  const avg = avgTotalRateBps5m.value;
  if (!Number.isFinite(avg) || avg <= 0) return false;
  return instantTotalRateBps.value > avg * 3;
});
const networkDropAlarm = computed(() => overallDropRatePct.value > 1);
const networkAlertText = computed(() => {
  if (!throughputSpike.value && !networkDropAlarm.value) return "";
  if (throughputSpike.value && networkDropAlarm.value) {
    return `网络告警：吞吐突增（>3x） + 丢包率 ${overallDropRatePct.value.toFixed(2)}%`;
  }
  if (throughputSpike.value) return "网络告警：吞吐突增（>3x）";
  return `网络告警：丢包率 ${overallDropRatePct.value.toFixed(2)}%`;
});

// Realtime throughput estimation using successive hostMetrics snapshots
const lastSample = ref<{ t: number; sent: number; recv: number } | null>(null);
const txRate = ref(0); // bytes/s
const rxRate = ref(0);
const prevTxRate = ref(0);
const prevRxRate = ref(0);

type NetSample = { t: number; sent: number; recv: number };
const netHistory = ref<NetSample[]>([]);
const rateHistory = ref<Array<{ t: number; tx: number; rx: number }>>([]);

const ONE_HOUR_MS = 60 * 60 * 1000;
const TWENTY_FOUR_HOUR_MS = 24 * ONE_HOUR_MS;

function findBaseline(history: NetSample[], cutoffMs: number): NetSample | null {
  // Find the newest sample with t <= cutoffMs
  for (let i = history.length - 1; i >= 0; i--) {
    if (history[i].t <= cutoffMs) return history[i];
  }
  return null;
}

// When switching devices, reset rate calculation state to avoid mixing counters
// between the previous device and the new one.
watch(
  () => props.device.deviceId,
  () => {
    lastSample.value = null;
    txRate.value = 0;
    rxRate.value = 0;
    netHistory.value = [];
    rateHistory.value = [];
    ifaceLastSample.value = {};
    ifaceRates.value = {};
    ifaceRateHistory.value = {};
    overallDropRatePct.value = 0;
    // Reset security snapshot derived state to avoid mixing devices.
    securityListeningRowsCache.value = [];
    securityPanelLoading.value = true;

    // Reset software assets cache to avoid mixing devices.
    softwareListCache.value = [];
    softwarePanelLoading.value = true;
    swQuery.value = "";
    swRiskOnly.value = false;
  }
);

function hasOwn(obj: any, key: string) {
  return !!obj && typeof obj === "object" && Object.prototype.hasOwnProperty.call(obj, key);
}

const softwareUpdateList = computed(() => {
  const hv: any = host.value;
  // Differential reporting: software_list field may be omitted when unchanged.
  if (hv && typeof hv === "object") {
    const hasField =
      hasOwn(hv, "software_list") ||
      hasOwn(hv, "SoftwareList") ||
      hasOwn(hv, "softwareList") ||
      hasOwn(hv, "software_list_items");
    if (hasField) {
      return asArray(hv?.software_list ?? hv?.SoftwareList ?? hv?.softwareList ?? hv?.software_list_items ?? []);
    }
  }

  // Fallback: some builds may still send software via device.softwareInventory.
  const inv: any = props.device.softwareInventory;
  if (inv) {
    return asArray(inv?.items ?? inv?.Items ?? inv);
  }

  return null;
});

watch(
  softwareUpdateList,
  (list) => {
    if (list !== null) {
      softwareListCache.value = Array.isArray(list) ? list : [];
      softwarePanelLoading.value = false;
      return;
    }

    // No update in this frame: keep existing cache.
    if ((softwareListCache.value?.length ?? 0) > 0) {
      softwarePanelLoading.value = false;
      return;
    }

    // If we already have realtime/snapshot data but software isn't provided, show empty state (not loading).
    if (props.device.wsReceived || props.device.snapshotFound || !!host.value) {
      softwarePanelLoading.value = false;
    }
  },
  { immediate: true, deep: true }
);
watch(
  () => [netSent.value, netRecv.value], // react when counters change
  () => {
    const now = Date.now();
    const sent = Number(netSent.value || 0);
    const recv = Number(netRecv.value || 0);

    const prev = lastSample.value;
    if (!prev) {
      lastSample.value = { t: now, sent, recv };
      netHistory.value = [{ t: now, sent, recv }];
      rateHistory.value = [];
      txRate.value = 0;
      rxRate.value = 0;
      return;
    }

    const dSentRaw = sent - prev.sent;
    const dRecvRaw = recv - prev.recv;

    // If counters reset/wrap, re-baseline to avoid bogus spikes.
    if (dSentRaw < 0 || dRecvRaw < 0) {
      lastSample.value = { t: now, sent, recv };
      netHistory.value = [{ t: now, sent, recv }];
      rateHistory.value = [];
      txRate.value = 0;
      rxRate.value = 0;
      return;
    }

    const changed = dSentRaw !== 0 || dRecvRaw !== 0;
    if (!changed) {
      // Counters didn't move: keep lastSample unchanged, but reflect 0 instantaneous rates.
      txRate.value = 0;
      rxRate.value = 0;
      return;
    } // Important: don't advance lastSample if counters didn't move.

    const dt = (now - prev.t) / 1000; // seconds
    if (dt <= 0) return;

    const dSent = dSentRaw;
    const dRecv = dRecvRaw;

    const MAX_BPS = 10 * 1024 * 1024 * 1024; // 10 GB/s safety cap
    const nextTx = dSent > 0 ? dSent / dt : 0;
    const nextRx = dRecv > 0 ? dRecv / dt : 0;
    txRate.value = nextTx > MAX_BPS ? (prevTxRate.value || 0) : nextTx;
    rxRate.value = nextRx > MAX_BPS ? (prevRxRate.value || 0) : nextRx;

    netHistory.value.push({ t: now, sent, recv });

    const netCutoff = now - (TWENTY_FOUR_HOUR_MS + ONE_HOUR_MS);
    netHistory.value = netHistory.value.filter((s) => s.t >= netCutoff);

    rateHistory.value.push({ t: now, tx: txRate.value, rx: rxRate.value });
    const rateCutoff = now - ONE_HOUR_MS;
    rateHistory.value = rateHistory.value.filter((r) => r.t >= rateCutoff);

    lastSample.value = { t: now, sent, recv };
    prevTxRate.value = txRate.value;
    prevRxRate.value = rxRate.value;
  },
  { immediate: true }
);

// Per-interface rate/drop calculations.
watch(
  () => filteredSortedIfaces.value,
  (list) => {
    const now = Date.now();
    if (!Array.isArray(list) || list.length === 0) {
      ifaceLastSample.value = {};
      ifaceRates.value = {};
      ifaceRateHistory.value = {};
      overallDropRatePct.value = 0;
      return;
    }

    const nextRates: Record<string, NetIfaceRateUI> = {};
    let overallDeltaDrop = 0;
    let overallDeltaPackets = 0;
    let movedAny = false;

    for (const iface of list as any[]) {
      const name = String(iface?.name ?? iface?.Name ?? iface?.iface_name ?? "");
      if (!name) continue;

      const curSent = Number(iface?.bytes_sent ?? iface?.BytesSent ?? iface?.bytesSent ?? 0);
      const curRecv = Number(iface?.bytes_recv ?? iface?.BytesRecv ?? iface?.bytesRecv ?? 0);
      const curPacketsSent = Number(iface?.packets_sent ?? iface?.PacketsSent ?? iface?.packetsSent ?? 0);
      const curPacketsRecv = Number(iface?.packets_recv ?? iface?.PacketsRecv ?? iface?.packetsRecv ?? 0);
      const curDropIn = Number(iface?.drop_in ?? iface?.DropIn ?? iface?.dropIn ?? 0);
      const curDropOut = Number(iface?.drop_out ?? iface?.DropOut ?? iface?.dropOut ?? 0);

      const prev = ifaceLastSample.value[name];
      if (!prev) {
        ifaceLastSample.value[name] = {
          t: now,
          sent: curSent,
          recv: curRecv,
          packetsSent: curPacketsSent,
          packetsRecv: curPacketsRecv,
          dropIn: curDropIn,
          dropOut: curDropOut,
        };
        continue;
      }

      const dt = (now - prev.t) / 1000;
      if (dt <= 0) continue;

      const dSentRaw = curSent - prev.sent;
      const dRecvRaw = curRecv - prev.recv;
      const dPacketsSentRaw = curPacketsSent - prev.packetsSent;
      const dPacketsRecvRaw = curPacketsRecv - prev.packetsRecv;
      const dDropInRaw = curDropIn - prev.dropIn;
      const dDropOutRaw = curDropOut - prev.dropOut;

      // Counters reset/wrap: re-baseline and don't compute this interval.
      if (
        dSentRaw < 0 ||
        dRecvRaw < 0 ||
        dPacketsSentRaw < 0 ||
        dPacketsRecvRaw < 0 ||
        dDropInRaw < 0 ||
        dDropOutRaw < 0
      ) {
        ifaceLastSample.value[name] = {
          t: now,
          sent: curSent,
          recv: curRecv,
          packetsSent: curPacketsSent,
          packetsRecv: curPacketsRecv,
          dropIn: curDropIn,
          dropOut: curDropOut,
        };
        ifaceRateHistory.value[name] = [{ t: now, tx: 0, rx: 0 }];
        continue;
      }

      const moved =
        dSentRaw !== 0 ||
        dRecvRaw !== 0 ||
        dPacketsSentRaw !== 0 ||
        dPacketsRecvRaw !== 0 ||
        dDropInRaw !== 0 ||
        dDropOutRaw !== 0;

      // If counters didn't move, keep last baseline but show 0 current rates.
      if (!moved) {
        nextRates[name] = { txRateBps: 0, rxRateBps: 0, dropDelta: 0, dropRatePct: 0 };
        continue;
      }

      movedAny = true;

      const dSent = dSentRaw;
      const dRecv = dRecvRaw;
      const dPacketsTotal = dPacketsSentRaw + dPacketsRecvRaw;
      const dDropTotal = dDropInRaw + dDropOutRaw;

      const MAX_BPS = 10 * 1024 * 1024 * 1024; // 10 GB/s safety cap
      const nextTx = dSent > 0 ? dSent / dt : 0;
      const nextRx = dRecv > 0 ? dRecv / dt : 0;
      const txRateBps = nextTx > MAX_BPS ? 0 : nextTx;
      const rxRateBps = nextRx > MAX_BPS ? 0 : nextRx;
      const dropDelta = dDropTotal > 0 ? dDropTotal : 0;
      const dropRatePct = dPacketsTotal > 0 ? (dropDelta / dPacketsTotal) * 100 : 0;

      nextRates[name] = { txRateBps, rxRateBps, dropDelta, dropRatePct };

      // update baseline only when counters moved
      ifaceLastSample.value[name] = {
        t: now,
        sent: curSent,
        recv: curRecv,
        packetsSent: curPacketsSent,
        packetsRecv: curPacketsRecv,
        dropIn: curDropIn,
        dropOut: curDropOut,
      };

      // push to per-NIC history and keep 5分钟窗口
      const hist = ifaceRateHistory.value[name] || [];
      hist.push({ t: now, tx: txRateBps, rx: rxRateBps });
      const cutoff = now - NET_BASELINE_MS;
      ifaceRateHistory.value[name] = hist.filter((p) => p.t >= cutoff);

      overallDeltaDrop += dropDelta;
      overallDeltaPackets += Math.max(0, dPacketsTotal);
    }

    ifaceRates.value = nextRates;
    if (movedAny && overallDeltaPackets > 0) {
      overallDropRatePct.value = (overallDeltaDrop / overallDeltaPackets) * 100;
    } else {
      overallDropRatePct.value = 0;
    }
  },
  { deep: true, immediate: true }
);

function ifaceAvgRate(name: string): { tx: number; rx: number } {
  const list = ifaceRateHistory.value[name] || [];
  if (!list.length) return { tx: 0, rx: 0 };
  let sumTx = 0;
  let sumRx = 0;
  for (const p of list) {
    sumTx += Number(p.tx || 0);
    sumRx += Number(p.rx || 0);
  }
  const n = list.length;
  return { tx: sumTx / n, rx: sumRx / n };
}

function ifaceSpike(name: string): boolean {
  const cur = ifaceRates.value[name];
  if (!cur) return false;
  const avg = ifaceAvgRate(name);
  const curTotal = Number(cur.txRateBps || 0) + Number(cur.rxRateBps || 0);
  const avgTotal = Number(avg.tx || 0) + Number(avg.rx || 0);
  if (!Number.isFinite(avgTotal) || avgTotal <= 0) return false;
  return curTotal > avgTotal * 3;
}

const netIfaceRows = computed(() => {
  return filteredSortedIfaces.value
    .map((i: any) => {
      const name = String(i?.name ?? i?.Name ?? "");
      if (!name) return null;
      const r = ifaceRates.value[name];
      return {
        name,
        txRateBps: r?.txRateBps ?? null,
        rxRateBps: r?.rxRateBps ?? null,
        dropDelta: r?.dropDelta ?? null,
        dropRatePct: r?.dropRatePct ?? null,
      };
    })
    .filter(Boolean) as Array<{
    name: string;
    txRateBps: number | null;
    rxRateBps: number | null;
    dropDelta: number | null;
    dropRatePct: number | null;
  }>;
});

const currentSentBytes = computed(() => Number(netSent.value || 0));
const currentRecvBytes = computed(() => Number(netRecv.value || 0));

const dailyTxBytes = computed(() => {
  const now = nowMs.value || Date.now();
  const base = findBaseline(netHistory.value, now - TWENTY_FOUR_HOUR_MS);
  if (!base) return null;
  const d = currentSentBytes.value - base.sent;
  if (!Number.isFinite(d) || d < 0) return null;
  return d;
});
const dailyRxBytes = computed(() => {
  const now = nowMs.value || Date.now();
  const base = findBaseline(netHistory.value, now - TWENTY_FOUR_HOUR_MS);
  if (!base) return null;
  const d = currentRecvBytes.value - base.recv;
  if (!Number.isFinite(d) || d < 0) return null;
  return d;
});

const hourlyTxBytes = computed(() => {
  const now = nowMs.value || Date.now();
  const base = findBaseline(netHistory.value, now - ONE_HOUR_MS);
  if (!base) return null;
  const d = currentSentBytes.value - base.sent;
  if (!Number.isFinite(d) || d < 0) return null;
  return d;
});
const hourlyRxBytes = computed(() => {
  const now = nowMs.value || Date.now();
  const base = findBaseline(netHistory.value, now - ONE_HOUR_MS);
  if (!base) return null;
  const d = currentRecvBytes.value - base.recv;
  if (!Number.isFinite(d) || d < 0) return null;
  return d;
});

const maxTxRateInHour = computed(() => {
  const now = nowMs.value || Date.now();
  const list = rateHistory.value.filter((r) => r.t >= now - ONE_HOUR_MS && Number.isFinite(r.tx));
  if (!list.length) return null;
  return Math.max(...list.map((r) => r.tx));
});
const maxRxRateInHour = computed(() => {
  const now = nowMs.value || Date.now();
  const list = rateHistory.value.filter((r) => r.t >= now - ONE_HOUR_MS && Number.isFinite(r.rx));
  if (!list.length) return null;
  return Math.max(...list.map((r) => r.rx));
});

// 5-minute total throughput sparkline (tx+rx)
function rateSparkPointsTotal(history: Array<{ t: number; tx: number; rx: number }>, width = 200, height = 40, pad = 2): string {
  if (!history.length) return "";
  const tMin = history[0].t;
  const tMax = history[history.length - 1].t;
  const innerW = Math.max(1, width - pad * 2);
  const innerH = Math.max(1, height - pad * 2);
  const totals = history.map((r) => Math.max(0, Number(r.tx || 0) + Number(r.rx || 0)));
  const maxV = Math.max(1, ...totals);
  // Cap to keep readable, up to 10 GBytes/s equivalent scale in bytes/s
  const cap = Math.max(1, Math.min(10 * 1024 * 1024 * 1024, maxV));
  return history
    .map((s) => {
      const x = pad + ((s.t - tMin) / (tMax - tMin || 1)) * innerW;
      const v = Math.max(0, Math.min(cap, Number(s.tx || 0) + Number(s.rx || 0)));
      const y = pad + (1 - v / cap) * innerH;
      return `${x.toFixed(1)},${y.toFixed(1)}`;
    })
    .join(" ");
}
const rateSpark5m = computed(() => {
  const now = nowMs.value || Date.now();
  const list = rateHistory.value.filter((r) => r.t >= now - FIVE_MIN_MS);
  return rateSparkPointsTotal(list);
});

const connections = computed(() => {
  const nc = props.device?.networkConnections;
  if (!nc) return [];
  const list = (Array.isArray(nc?.connections) ? nc.connections : nc) ?? [];
  return Array.isArray(list) ? list : [];
});

const services = computed(() => {
  // Services should live under host_metrics.services; keep legacy fallbacks too.
  const hv: any = host.value;
  const v =
    hv?.services ??
    hv?.Services ??
    (props.device as any)?.services ??
    (props.device as any)?.Services ??
    [];
  return Array.isArray(v) ? v : [];
});
const ignoredServiceSet = ref<Set<string>>(new Set());
const IGNORE_KEY = "go-agent:ignored_failed_services";
function loadIgnoredServices() {
  try {
    const raw = localStorage.getItem(IGNORE_KEY);
    if (!raw) return;
    const arr = JSON.parse(raw);
    if (Array.isArray(arr)) ignoredServiceSet.value = new Set(arr.map((x) => String(x)));
  } catch {}
}
function saveIgnoredServices() {
  try {
    localStorage.setItem(IGNORE_KEY, JSON.stringify(Array.from(ignoredServiceSet.value)));
  } catch {}
}
onMounted(() => {
  loadIgnoredServices();
});
const failedServices = computed(() => {
  return (services.value || []).filter((s: any) => {
    const name = String(svcName(s));
    if (ignoredServiceSet.value.has(name)) return false;
    const status = String(s?.status ?? s?.Status ?? "").toLowerCase();
    const exit = Number(s?.exit_code ?? s?.ExitCode ?? 0);
    return status.includes("fail") || exit !== 0;
  });
});
const keyServices = computed(() => {
  // Best-effort: show all services we received (already filtered on backend: failed + whitelist).
  return services.value || [];
});
function svcName(s: any) {
  return s?.name ?? s?.Name ?? "";
}
function svcStatus(s: any) {
  return s?.status ?? s?.Status ?? "";
}
function svcExitCode(s: any) {
  return Number(s?.exit_code ?? s?.ExitCode ?? 0) || 0;
}
function svcUptimeSec(s: any) {
  return Number(s?.uptime_sec ?? s?.UptimeSec ?? 0) || 0;
}
function formatUptime(sec: number) {
  sec = Math.max(0, Math.floor(Number(sec) || 0));
  const d = Math.floor(sec / 86400);
  const h = Math.floor((sec % 86400) / 3600);
  const m = Math.floor((sec % 3600) / 60);
  if (d > 0) return `${d}d ${h}h ${m}m`;
  if (h > 0) return `${h}h ${m}m`;
  return `${m}m`;
}
function ignoreCurrentFailed() {
  for (const s of failedServices.value) {
    ignoredServiceSet.value.add(String(svcName(s)));
  }
  saveIgnoredServices();
}
function jumpToService(s: any) {
  const name = String(svcName(s));
  if (!name) return;
  procQuery.value = name;
  // try jump to first matching process in current filtered list
  const idx = (filteredProcesses.value || []).findIndex((p: any) => String(procServiceName(p)) === name);
  if (idx >= 0) {
    procPage.value = Math.floor(idx / 50) + 1;
    // highlight the target row briefly for visual guidance
    const target = (filteredProcesses.value || [])[idx];
    const pid = Number(procPid(target));
    if (Number.isFinite(pid) && pid > 0) {
      highlightPid.value = pid;
      // scroll to row after DOM updates
      requestAnimationFrame(() => {
        const el = document.getElementById(`proc-${pid}`);
        if (el) el.scrollIntoView({ block: "center", behavior: "smooth" });
      });
      window.setTimeout(() => {
        if (highlightPid.value === pid) highlightPid.value = null;
      }, 1600);
    }
  } else {
    procPage.value = 1;
  }
}

function jumpToPort(rowOrPort: any) {
  const row = rowOrPort && typeof rowOrPort === "object" ? rowOrPort : null;
  const portRaw = row ? row.port : rowOrPort;
  const n = Number(portRaw);
  if (!Number.isFinite(n) || n <= 0) return;

  activeTab.value = "compute";

  // Prefer PID (most precise), fallback to process name, final fallback to port number.
  const pid = Number(row?.pid ?? 0);
  const procNameHint = String(row?.processName ?? row?.process_name ?? "").trim();
  if (Number.isFinite(pid) && pid > 0) {
    procQuery.value = String(pid);
  } else if (procNameHint) {
    procQuery.value = procNameHint;
  } else {
    procQuery.value = String(n);
  }

  // Seek to the first process that exposes this port (for highlight)
  const idx = (filteredProcesses.value || []).findIndex((p: any) => {
    const ports = procListenPorts(p);
    return Array.isArray(ports) && ports.includes(n);
  });
  if (idx >= 0) {
    procPage.value = Math.floor(idx / procPageSize) + 1;
    const target = (filteredProcesses.value || [])[idx];
    const targetPid = Number(procPid(target));
    if (Number.isFinite(targetPid) && targetPid > 0) {
      highlightPid.value = targetPid;
      requestAnimationFrame(() => {
        const el = document.getElementById(`proc-${targetPid}`);
        if (el) el.scrollIntoView({ block: "center", behavior: "smooth" });
      });
      window.setTimeout(() => {
        if (highlightPid.value === targetPid) highlightPid.value = null;
      }, 1600);
    }
  } else {
    procPage.value = 1;
  }
}

const filteredProcesses = computed(() => {
  const hv: any = host.value;
  const list = (hv?.processes ?? hv?.Processes ?? props.device?.processes ?? []) as any[];
  const q = (procQuery.value || "").trim().toLowerCase();
  if (!q) return list;
  return list.filter((p) => {
    const pid = String(procPid(p));
    const name = String(procName(p)).toLowerCase();
    const exec = String(procExecPath(p)).toLowerCase();
    const svc = String(procServiceName(p)).toLowerCase();
    // also allow matching by listening port number
    let portHit = false;
    const qNum = Number(q);
    if (Number.isFinite(qNum) && qNum > 0) {
      const ports = procListenPorts(p);
      portHit = Array.isArray(ports) && ports.includes(qNum);
    }
    return portHit || pid.includes(q) || name.includes(q) || exec.includes(q) || svc.includes(q);
  });
});

// Pagination for large process list to avoid rendering lag
const highlightPid = ref<number | null>(null);
const procPage = ref(1);
const procPageSize = 50;
const procPageCount = computed(() => {
  const n = filteredProcesses.value?.length || 0;
  return Math.max(1, Math.ceil(n / procPageSize));
});
watch(filteredProcesses, () => {
  procPage.value = 1;
});
const pagedProcesses = computed(() => {
  const arr = filteredProcesses.value || [];
  const start = (procPage.value - 1) * procPageSize;
  return arr.slice(start, start + procPageSize);
});

const filteredSoftware = computed(() => {
  const list = (softwareListCache.value ?? []).map((s: any) => ({
    ...s,
    is_vulnerable: isSoftwareVulnerable(s),
  }));
  const q = (swQuery.value || "").trim().toLowerCase();
  let out = !q
    ? list
    : list.filter((s: any) => {
    const name = String(swName(s)).toLowerCase();
    const publisher = String(swPublisher(s)).toLowerCase();
    return name.includes(q) || publisher.includes(q);
  });
  if (swRiskOnly.value) {
    out = out.filter((s: any) => !!s?.is_vulnerable);
  }
  return out;
});

function normalizeVersionParts(v: string): number[] {
  return String(v || "")
    .trim()
    .split(/[.\-_+]/g)
    .map((p) => {
      const m = String(p).match(/\d+/);
      return m ? Number(m[0]) : 0;
    });
}

function isVersionLessThan(current: string, threshold: string): boolean {
  const a = normalizeVersionParts(current);
  const b = normalizeVersionParts(threshold);
  const n = Math.max(a.length, b.length);
  for (let i = 0; i < n; i++) {
    const av = a[i] ?? 0;
    const bv = b[i] ?? 0;
    if (av < bv) return true;
    if (av > bv) return false;
  }
  return false;
}

function isSoftwareVulnerable(s: any): boolean {
  const name = String(swName(s) || "").toLowerCase();
  const version = String(swVersion(s) || "").trim();
  if (!name || !version) return false;
  return VULNERABILITY_RULES.some((rule) => {
    const hit = name.includes(String(rule.name || "").toLowerCase());
    if (!hit) return false;
    const versionEq = String(rule.version_eq || "").trim();
    if (versionEq) {
      return version === versionEq;
    }
    const versionLt = String(rule.version_lt || "").trim();
    if (versionLt) {
      return isVersionLessThan(version, versionLt);
    }
    return false;
  });
}

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
  return filteredSoftware.value.filter((s: any) => isWindowsPatch(s));
});

const filteredNormalSoftware = computed(() => {
  if (!isWindowsDevice.value) return filteredSoftware.value;
  return filteredSoftware.value.filter((s: any) => !isWindowsPatch(s));
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

function parseInstallDateRaw(s: any): string {
  // Try common fields across platforms/payloads
  const raw =
    s?.InstallDate ??
    s?.install_date ??
    s?.InstalledOn ??
    s?.installed_on ??
    s?.LastModified ??
    s?.last_modified ??
    s?.lastModified ??
    "";
  const v = raw as any;
  if (!v) return "";
  // Numbers: treat as epoch (s or ms)
  if (typeof v === "number") {
    const ms = v < 1e11 ? v * 1000 : v;
    const d = new Date(ms);
    return isNaN(d.getTime()) ? "" : d.toISOString();
  }
  const str = String(v).trim();
  if (!str) return "";
  // Windows common: YYYYMMDD
  if (/^\d{8}$/.test(str)) {
    const y = str.slice(0, 4);
    const m = str.slice(4, 6);
    const d = str.slice(6, 8);
    return `${y}-${m}-${d}`;
  }
  // ISO-like string
  const d = new Date(str);
  if (!isNaN(d.getTime())) return d.toISOString();
  // Fallback: return as-is
  return str;
}

function formatDateLocal(s: string): string {
  if (!s) return "";
  const d = new Date(s);
  if (!isNaN(d.getTime())) {
    return `${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, "0")}-${String(
      d.getDate()
    ).padStart(2, "0")} ${String(d.getHours()).padStart(2, "0")}:${String(d.getMinutes()).padStart(
      2,
      "0"
    )}`;
  }
  return s;
}

function swInstallDateText(s: any) {
  return formatDateLocal(parseInstallDateRaw(s));
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

function formatUnixSecLocal(sec: number): string {
  if (!sec || !Number.isFinite(sec)) return "";
  const d = new Date(sec * 1000);
  if (Number.isNaN(d.getTime())) return "";
  const y = d.getFullYear();
  const m = String(d.getMonth() + 1).padStart(2, "0");
  const day = String(d.getDate()).padStart(2, "0");
  const hh = String(d.getHours()).padStart(2, "0");
  const mm = String(d.getMinutes()).padStart(2, "0");
  const ss = String(d.getSeconds()).padStart(2, "0");
  return `${y}-${m}-${day} ${hh}:${mm}:${ss}`;
}

const nowMs = ref(Date.now());
const heartbeatTimer = window.setInterval(() => {
  nowMs.value = Date.now();
}, 1000);
onUnmounted(() => {
  window.clearInterval(heartbeatTimer);
  window.removeEventListener("click", closeSecurityContextMenu, true);
});

const lastHeartbeatAgoText = computed(() => {
  const updatedSec = props.device.updatedAtSec || 0;
  const updatedMs = updatedSec > 0 ? updatedSec * 1000 : 0;
  const t = updatedMs || (hostCollectedAt.value ? new Date(hostCollectedAt.value as any).getTime() : 0);
  if (!t || Number.isNaN(t)) return "";
  const diffSec = Math.max(0, Math.floor((nowMs.value - t) / 1000));
  if (diffSec < 5) return "心跳：刚刚";
  if (diffSec < 60) return `心跳：${diffSec} 秒前`;
  const mins = Math.floor(diffSec / 60);
  if (mins < 60) return `心跳：${mins} 分钟前`;
  const hours = Math.floor(mins / 60);
  return `心跳：${hours} 小时前`;
});

const heartbeatToneClass = computed(() => {
  const updatedSec = props.device.updatedAtSec || 0;
  const updatedMs = updatedSec > 0 ? updatedSec * 1000 : 0;
  const t = updatedMs || (hostCollectedAt.value ? new Date(hostCollectedAt.value as any).getTime() : 0);
  if (!t || Number.isNaN(t)) return "text-slate-400";
  const diffSec = Math.max(0, Math.floor((nowMs.value - t) / 1000));
  if (diffSec < 15) return "text-emerald-300";
  if (diffSec < 60) return "text-sky-300";
  if (diffSec < 120) return "text-amber-300";
  return "text-red-300";
});

const heartbeatAgeSec = computed<number | null>(() => {
  const updatedSec = props.device.updatedAtSec || 0;
  const updatedMs = updatedSec > 0 ? updatedSec * 1000 : 0;
  const t = updatedMs || (hostCollectedAt.value ? new Date(hostCollectedAt.value as any).getTime() : 0);
  if (!t || Number.isNaN(t)) return null;
  const diffSec = Math.max(0, Math.floor((nowMs.value - t) / 1000));
  return diffSec;
});

function closeSecurityContextMenu() {
  showSecurityContextMenu.value = false;
  securityContextRow.value = null;
}

function onSecurityRowContextMenu(row: any, ev: MouseEvent) {
  securityContextRow.value = row;
  securityContextMenuPos.value = { x: ev.clientX, y: ev.clientY };
  showSecurityContextMenu.value = true;
  window.addEventListener("click", closeSecurityContextMenu, true);
}

async function markPortAsWhitelisted() {
  const row = securityContextRow.value;
  const devId = props.device.deviceId;
  if (!row || !devId) {
    closeSecurityContextMenu();
    return;
  }
  try {
    await request.post(`/api/v1/device/${encodeURIComponent(devId)}/risk_whitelist`, {
      protocol: String(row.protocol || "tcp").toLowerCase(),
      port: Number(row.port || 0),
      scope: row.scope || "",
      reason: "Marked as known/authorized risk from UI",
    });
    // Optimistically mark as AUTHORIZED in UI.
    row.riskLevel = "AUTHORIZED";
  } catch (e) {
    // eslint-disable-next-line no-console
    console.error("markPortAsWhitelisted failed", e);
  } finally {
    closeSecurityContextMenu();
  }
}

const effectiveWsStatus = computed<WSConnectionStatus>(() => {
  // If WS itself is disconnected, always reflect that first.
  if (props.wsStatus === "offline") return "offline";
  if (props.wsStatus === "reconnecting") return "reconnecting";

  // WS is open, but if heartbeat is too stale, treat as needing reconnect.
  const age = heartbeatAgeSec.value;
  if (age == null) return "online";
  if (age >= HEARTBEAT_OFFLINE_TIMEOUT_SEC) return "offline";
  if (age >= HEARTBEAT_RECONNECT_TIMEOUT_SEC) return "reconnecting";
  return "online";
});

watch(
  () => effectiveWsStatus.value,
  (s) => {
    if (s === "online") manualRetrying.value = false;
  }
);

// ----- Security Snapshot (Listening Ports) -----
// Security audit: mark exposed public ports as CRITICAL.
const HIGH_RISK_PORTS = [22, 3389, 445, 3306, 6379];

// Consume security snapshot with snake/camel + host/root compatibility.
const securityData = computed(() => {
  const hm: any = hostMetrics.value as any;
  const d: any = props.device as any;
  return (
    hm?.security_snapshot ??
    hm?.securitySnapshot ??
    d?.security_snapshot ??
    d?.securitySnapshot ??
    []
  ) as any;
});

watch(
  securityData,
  (sd) => {
    const rawPorts = (sd as any)?.listening ?? (sd as any)?.Listening ?? [];
    const ports = asArray(rawPorts);

    const prev = securityListeningRowsCache.value || [];
    const prevByKey = new Map<string, any>();
    for (const r of prev) {
      const k = String(r?.key ?? "");
      if (k) prevByKey.set(k, r);
    }

    const next = ports.map((p: any) => {
      const protocol = p?.protocol ?? p?.Protocol ?? "-";
      const address = p?.address ?? p?.Address ?? "-";
      const port = Number(p?.port ?? p?.Port ?? 0);
      const scope = p?.scope ?? p?.Scope ?? "";
      const pid = Number(p?.pid ?? p?.PID ?? 0) || 0;
      const processName = String(p?.process_name ?? p?.processName ?? p?.ProcessName ?? p?.name ?? p?.Name ?? "").trim();

      const isPublic = scope === "public";
      const isCritical = isPublic && HIGH_RISK_PORTS.includes(port);
      let riskLevel = isCritical ? "CRITICAL" : isPublic ? "WARNING" : "NORMAL";
      // If backend already marked this as authorized (whitelisted), respect it.
      const backendRisk = String(p?.risk_level ?? p?.riskLevel ?? "").toUpperCase();
      if (backendRisk === "AUTHORIZED") {
        riskLevel = "AUTHORIZED";
      }
      const key = `${pid || 0}|${port}|${String(protocol)}|${String(address)}|${String(scope)}`;
      const existed = prevByKey.get(key);
      const row = existed ?? {};
      row.key = key;
      row.protocol = protocol;
      row.address = address;
      row.port = port;
      row.scope = scope;
      row.pid = pid;
      row.processName = processName;
      row.riskLevel = riskLevel;
      return row;
    });

    // Stable sort: risk desc, then port asc, then protocol/address
    const riskRank = (r: any) => (r?.riskLevel === "CRITICAL" ? 2 : r?.riskLevel === "WARNING" ? 1 : 0);
    next.sort((a: any, b: any) => {
      const ra = riskRank(a);
      const rb = riskRank(b);
      if (ra !== rb) return rb - ra;
      const pa = Number(a?.port || 0);
      const pb = Number(b?.port || 0);
      if (pa !== pb) return pa - pb;
      const proA = String(a?.protocol ?? "");
      const proB = String(b?.protocol ?? "");
      if (proA !== proB) return proA.localeCompare(proB, "en");
      const adA = String(a?.address ?? "");
      const adB = String(b?.address ?? "");
      return adA.localeCompare(adB, "en");
    });

    // Update cache only when actually changed to reduce table jitter.
    const same =
      prev.length === next.length &&
      prev.every((r: any, i: number) => String(r?.key ?? "") === String(next[i]?.key ?? "") && String(r?.riskLevel ?? "") === String(next[i]?.riskLevel ?? ""));
    if (!same) {
      securityListeningRowsCache.value = next;
    } else {
      // Still update fields in-place (pid/name may become available later) without changing order/identity.
      for (let i = 0; i < prev.length; i++) {
        Object.assign(prev[i], next[i]);
      }
    }

    securityPanelLoading.value = false;
  },
  { immediate: true }
);

const publicListeningCount = computed(() => {
  return (securityListeningRowsCache.value || []).filter((r: any) => r?.scope === "public").length;
});

const latencyEstimateText = computed(() => {
  // Frontend-only placeholder/estimate:
  // We don't have true RTT yet, so we approximate "data freshness delay" from last snapshot time.
  const updatedSec = props.device.updatedAtSec || 0;
  const updatedMs = updatedSec > 0 ? updatedSec * 1000 : 0;
  const t = updatedMs || (hostCollectedAt.value ? new Date(hostCollectedAt.value as any).getTime() : 0);
  if (!t || Number.isNaN(t)) return "";
  const diffMs = Math.max(0, nowMs.value - t);
  if (diffMs < 1000) return `延迟(估计/占位)：<1 秒（基于心跳）`;
  const diffSec = Math.floor(diffMs / 1000);
  if (diffSec < 60) return `延迟(估计/占位)：${diffSec} 秒（基于心跳）`;
  const mins = Math.floor(diffSec / 60);
  if (mins < 60) return `延迟(估计/占位)：${mins} 分钟（基于心跳）`;
  const hours = Math.floor(mins / 60);
  return `延迟(估计/占位)：${hours} 小时（基于心跳）`;
});

// 5-minute sparkline (frontend only): accumulate successive host_metrics samples.
const FIVE_MIN_MS = 5 * 60 * 1000;
type SparkSample = { t: number; v: number };
const cpuHistory = ref<SparkSample[]>([]);
const memHistory = ref<SparkSample[]>([]);
const pingHistory = ref<SparkSample[]>([]);

function parseCollectedAtMs(hm: any): number | null {
  const ca = hm?.CollectedAt ?? hm?.collected_at ?? hm?.collectedAt;
  if (ca == null) return null;
  if (typeof ca === "number") {
    // seconds or milliseconds (best-effort)
    const ms = ca < 1e11 ? ca * 1000 : ca;
    const d = new Date(ms);
    return Number.isNaN(d.getTime()) ? null : d.getTime();
  }
  if (typeof ca === "string") {
    const d = new Date(ca);
    return Number.isNaN(d.getTime()) ? null : d.getTime();
  }
  return null;
}

watch(
  () => host.value,
  (hm) => {
    if (!hm) {
      cpuHistory.value = [];
      memHistory.value = [];
      pingHistory.value = [];
      return;
    }

    const cpuRaw = hm?.CPU?.Total ?? hm?.cpu_total_percent;
    const memRaw = hm?.Memory?.UsedPercent ?? hm?.memory_used_percent;
    const pingRaw = hm?.ping_latency_ms ?? hm?.pingLatencyMs ?? hm?.PingLatencyMs;

    const cpuOk = cpuRaw !== undefined && cpuRaw !== null && cpuRaw !== "";
    const memOk = memRaw !== undefined && memRaw !== null && memRaw !== "";
    const pingOk = pingRaw !== undefined && pingRaw !== null && pingRaw !== "";
    if (!cpuOk && !memOk && !pingOk) return;

    const t = parseCollectedAtMs(hm) ?? nowMs.value ?? Date.now();

    if (cpuOk) {
      const cpuV = Number(cpuRaw);
      if (Number.isFinite(cpuV)) cpuHistory.value.push({ t, v: cpuV });
    }
    if (memOk) {
      const memV = Number(memRaw);
      if (Number.isFinite(memV)) memHistory.value.push({ t, v: memV });
    }
    if (pingOk) {
      const pingV = Number(pingRaw);
      if (Number.isFinite(pingV) && pingV > 0) pingHistory.value.push({ t, v: pingV });
    }

    // Keep only last 5 minutes.
    const cutoff = (nowMs.value || Date.now()) - FIVE_MIN_MS;
    cpuHistory.value = cpuHistory.value.filter((s) => s.t >= cutoff);
    memHistory.value = memHistory.value.filter((s) => s.t >= cutoff);
    pingHistory.value = pingHistory.value.filter((s) => s.t >= cutoff);
  }
);

function sparklinePoints(history: SparkSample[], width = 200, height = 40, pad = 2): string {
  if (!history.length) return "";
  const tMin = history[0].t;
  const tMax = history[history.length - 1].t;
  const innerW = Math.max(1, width - pad * 2);
  const innerH = Math.max(1, height - pad * 2);

  return history
    .map((s) => {
      const x = pad + ((s.t - tMin) / (tMax - tMin || 1)) * innerW;
      const v = Math.max(0, Math.min(100, Number.isFinite(s.v) ? s.v : 0));
      const y = pad + (1 - v / 100) * innerH;
      return `${x.toFixed(1)},${y.toFixed(1)}`;
    })
    .join(" ");
}

function sparklinePointsPing(history: SparkSample[], width = 200, height = 40, pad = 2): string {
  if (!history.length) return "";
  const tMin = history[0].t;
  const tMax = history[history.length - 1].t;
  const innerW = Math.max(1, width - pad * 2);
  const innerH = Math.max(1, height - pad * 2);

  const maxV = Math.max(
    1,
    ...history.map((s) => (Number.isFinite(s.v) ? s.v : 0)).filter((v) => v >= 0)
  );
  // Cap to keep chart readable.
  const cap = Math.max(1, Math.min(1000, maxV));

  return history
    .map((s) => {
      const x = pad + ((s.t - tMin) / (tMax - tMin || 1)) * innerW;
      const v = Math.max(0, Math.min(cap, Number.isFinite(s.v) ? s.v : 0));
      const y = pad + (1 - v / cap) * innerH;
      return `${x.toFixed(1)},${y.toFixed(1)}`;
    })
    .join(" ");
}

const cpuSpark = computed(() => sparklinePoints(cpuHistory.value));
const memSpark = computed(() => sparklinePoints(memHistory.value));
const pingSpark = computed(() => sparklinePointsPing(pingHistory.value));

// -------- Disk I/O (read/write KB/s + IOPS + Util%) history by device --------
type DiskIOSample = { t: number; rKB: number; wKB: number; rIOPS: number; wIOPS: number; util: number };
const diskIoHist = ref<Record<string, DiskIOSample[]>>({});
const selectedDisk = ref<string>("");
const diskHoverIdx = ref<number | null>(null);

const diskIoList = computed(() => {
  const arr = asArray(host.value?.disk_io ?? host.value?.DiskIO);
  return arr as any[];
});

function diskDeviceName(s: any): string {
  return String(s?.device_name ?? s?.DeviceName ?? s?.name ?? s?.Name ?? "");
}

function ensureDiskSelected() {
  const keys = Object.keys(diskIoHist.value);
  if (!selectedDisk.value && keys.length) {
    selectedDisk.value = keys[0];
  }
  if (selectedDisk.value && !diskIoHist.value[selectedDisk.value] && keys.length) {
    selectedDisk.value = keys[0];
  }
}

watch(
  () => selectedDeviceId.value,
  () => {
    diskIoHist.value = {};
    selectedDisk.value = "";
    diskHoverIdx.value = null;
  }
);

function parseCollectedAtMsLoose(hm: any): number {
  const v = hm?.CollectedAt ?? hm?.collected_at ?? hm?.collectedAt;
  if (typeof v === "number") return v < 1e11 ? v * 1000 : v;
  if (typeof v === "string") {
    const d = new Date(v);
    const ms = d.getTime();
    return Number.isNaN(ms) ? Date.now() : ms;
  }
  return Date.now();
}

watch(
  () => host.value,
  (hm) => {
    if (!hm) return;
    const ioArr = asArray(hm?.disk_io ?? hm?.DiskIO);
    if (!ioArr.length) return;
    const t = parseCollectedAtMsLoose(hm);
    const cutoff = (nowMs.value || Date.now()) - FIVE_MIN_MS;
    for (const s of ioArr as any[]) {
      const name = diskDeviceName(s);
      if (!name) continue;
      const rKB = Number(s?.read_kbps ?? s?.ReadKBps ?? 0);
      const wKB = Number(s?.write_kbps ?? s?.WriteKBps ?? 0);
      const rIOPS = Number(s?.read_iops ?? s?.ReadIOPS ?? 0);
      const wIOPS = Number(s?.write_iops ?? s?.WriteIOPS ?? 0);
      const util = Math.max(0, Math.min(100, Number(s?.util_percent ?? s?.UtilPercent ?? 0)));
      if (!Number.isFinite(rKB) && !Number.isFinite(wKB)) continue;
      const list = diskIoHist.value[name] || [];
      list.push({
        t,
        rKB: Math.max(0, rKB),
        wKB: Math.max(0, wKB),
        rIOPS: Math.max(0, rIOPS),
        wIOPS: Math.max(0, wIOPS),
        util,
      });
      // Keep last 5 minutes
      const kept = list.filter((x) => x.t >= cutoff);
      diskIoHist.value = { ...diskIoHist.value, [name]: kept };
    }
    ensureDiskSelected();
  },
  { immediate: true, deep: false }
);

function diskSparkPointsRW(list: DiskIOSample[], width = 300, height = 60, pad = 2): { r: string; w: string } {
  if (!list || !list.length) return { r: "", w: "" };
  const tMin = list[0].t;
  const tMax = list[list.length - 1].t;
  const innerW = Math.max(1, width - pad * 2);
  const innerH = Math.max(1, height - pad * 2);
  const maxV = Math.max(
    1,
    ...list.flatMap((s) => [s.rKB, s.wKB]).map((v) => (Number.isFinite(v) ? v : 0))
  );
  // cap max to keep readable
  const cap = Math.max(1, Math.min(100000, maxV));
  const r = list
    .map((s) => {
      const x = pad + ((s.t - tMin) / (tMax - tMin || 1)) * innerW;
      const v = Math.max(0, Math.min(cap, Number.isFinite(s.rKB) ? s.rKB : 0));
      const y = pad + (1 - v / cap) * innerH;
      return `${x.toFixed(1)},${y.toFixed(1)}`;
    })
    .join(" ");
  const w = list
    .map((s) => {
      const x = pad + ((s.t - tMin) / (tMax - tMin || 1)) * innerW;
      const v = Math.max(0, Math.min(cap, Number.isFinite(s.wKB) ? s.wKB : 0));
      const y = pad + (1 - v / cap) * innerH;
      return `${x.toFixed(1)},${y.toFixed(1)}`;
    })
    .join(" ");
  return { r, w };
}

const selectedDiskHist = computed<DiskIOSample[]>(() => {
  const key = selectedDisk.value;
  if (!key) return [];
  return diskIoHist.value[key] || [];
});

const diskSparkRW = computed(() => diskSparkPointsRW(selectedDiskHist.value));

const selectedDiskUtil = computed(() => {
  const list = selectedDiskHist.value;
  if (!list.length) return 0;
  const last = list[list.length - 1];
  const u = Number(last?.util ?? 0);
  return Number.isFinite(u) ? Math.max(0, Math.min(100, u)) : 0;
});

function onDiskMouseMove(ev: MouseEvent) {
  const target = ev.currentTarget as SVGSVGElement | null;
  const list = selectedDiskHist.value;
  if (!target || !list.length) {
    diskHoverIdx.value = null;
    return;
  }
  const rect = target.getBoundingClientRect();
  const x = ev.clientX - rect.left;
  const width = rect.width;
  const idx = Math.max(0, Math.min(list.length - 1, Math.round((x / Math.max(1, width)) * (list.length - 1))));
  diskHoverIdx.value = idx;
}

function onDiskMouseLeave() {
  diskHoverIdx.value = null;
}

const diskHoverText = computed(() => {
  const list = selectedDiskHist.value;
  if (!list.length) return "";
  const i = diskHoverIdx.value == null ? list.length - 1 : diskHoverIdx.value;
  const s = list[i];
  return `IOPS R:${s.rIOPS.toFixed(1)} / W:${s.wIOPS.toFixed(1)}`;
});

// -------- SMART health (hardware_details.smart_info) --------
const hwSmart = computed(() => asArray(hw.value?.smart_info ?? hw.value?.SmartInfo));
function powerOnText(hours: any) {
  const h = Math.max(0, Number(hours || 0));
  const d = Math.floor(h / 24);
  const remH = h % 24;
  if (d > 0) return `${d}天${remH}小时`;
  return `${remH}小时`;
}

// -------- GPU stats (host_metrics.gpu_stats) --------
const gpuStats = computed(() => asArray(host.value?.gpu_stats ?? host.value?.GpuStats));

// -------- Alarm summary (overview) --------
const smartWarningsCount = computed(() => {
  const list = hwSmart.value || [];
  let count = 0;
  for (const s of list as any[]) {
    const status = String(s?.status ?? s?.Status ?? "").toUpperCase();
    const temp = Number(s?.temperature ?? s?.Temperature ?? NaN);
    const statusBad = status && status !== "PASSED";
    const tempHot = Number.isFinite(temp) && temp >= 65;
    if (statusBad || tempHot) count++;
  }
  return count;
});

type AlertItem = { text: string; level: "critical" | "warning" | "info" };
const alertSummaryItems = computed<AlertItem[]>(() => {
  const items: AlertItem[] = [];
  if (throughputSpike.value) {
    items.push({ text: "网络吞吐突增（>3x 基线）", level: "warning" });
  }
  if (networkDropAlarm.value) {
    items.push({ text: `网络丢包率偏高（${overallDropRatePct.value.toFixed(2)}%）`, level: "critical" });
  }
  if (smartWarningsCount.value > 0) {
    items.push({ text: `磁盘 SMART 预警 ${smartWarningsCount.value} 项`, level: "critical" });
  }
  return items;
});
</script>

<template>
  <div class="flex flex-col h-full">
    <div class="header-bar">
      <div class="min-w-0">
        <div class="text-base md:text-lg font-semibold truncate">
          设备详情：{{ selectedDeviceId || "未选择设备" }}
        </div>
        <div class="text-[11px] md:text-xs text-slate-400 mt-0.5 flex flex-wrap gap-3">
          <span v-if="device.updatedAtSec">上次快照：{{ formatUnixSecLocal(device.updatedAtSec) }}</span>
          <span v-if="lastHeartbeatAgoText" :class="heartbeatToneClass">{{ lastHeartbeatAgoText }}</span>
          <span v-if="latencyEstimateText" class="text-slate-300">{{ latencyEstimateText }}</span>
        </div>
      </div>

      <div class="flex items-center gap-2">
        <span class="status-dot" :class="statusClass"></span>
        <div class="text-sm text-slate-200">{{ statusText }}</div>
        <button
          v-if="effectiveWsStatus !== 'online'"
          :disabled="manualRetrying"
          :class="manualRetrying ? 'opacity-70 cursor-not-allowed' : ''"
          class="text-[11px] px-2 py-1 rounded border border-slate-800 bg-slate-900/40 hover:bg-slate-900/70"
          @click="onRetryWs"
        >
          {{ manualRetrying ? "重试中…" : "手动重试" }}
        </button>
      </div>
    </div>

    <div v-if="!hasSelectedDevice" class="flex-1 flex items-center justify-center p-6">
      <div class="card-base card-padding max-w-md w-full text-center">
        <div class="text-sm font-semibold text-slate-200 mb-1">请选择设备</div>
        <div class="text-xs text-slate-500">从左侧设备列表选择一个设备后，将显示实时指标、进程与服务状态。</div>
      </div>
    </div>
    <div v-else-if="hasSelectedDevice && !hostMetrics" class="flex-1 p-4 space-y-4">
      <div class="card-base card-padding animate-pulse space-y-3">
        <div class="h-5 w-40 bg-slate-800 rounded"></div>
        <div class="h-3 w-full bg-slate-800 rounded"></div>
        <div class="h-3 w-5/6 bg-slate-800 rounded"></div>
      </div>
      <div class="card-base card-padding animate-pulse h-56"></div>
      <div class="card-base card-padding animate-pulse h-56"></div>
    </div>
    <div v-else-if="showEmptyWaiting" class="flex-1 flex items-center justify-center text-slate-500 text-sm">
      等待 Agent 首次上报...
    </div>
    <div v-else class="flex-1 overflow-y-auto p-4 space-y-4">
      <div v-if="isWaitingRealtime" class="text-xs text-amber-300/90 px-3 py-2 rounded-lg border border-amber-500/30 bg-amber-500/10">
        正在等待实时数据...
      </div>
      <!-- Tabs -->
      <div class="tab-bar">
        <button
          class="tab-btn transition-colors"
          :class="activeTab==='overview' ? 'tab-btn-active' : ''"
          @click="activeTab='overview'"
        >概览</button>
        <button
          class="tab-btn transition-colors"
          :class="activeTab==='network' ? 'tab-btn-active' : ''"
          @click="activeTab='network'"
        >网络</button>
        <button
          class="tab-btn transition-colors"
          :class="activeTab==='hardware' ? 'tab-btn-active' : ''"
          @click="activeTab='hardware'"
        >存储与硬件</button>
        <button
          class="tab-btn transition-colors"
          :class="activeTab==='compute' ? 'tab-btn-active' : ''"
          @click="activeTab='compute'"
        >算力与进程</button>
        <button
          class="tab-btn transition-colors"
          :class="activeTab==='security' ? 'tab-btn-active' : ''"
          @click="activeTab='security'"
        >安全审计</button>
        <button
          class="tab-btn transition-colors"
          :class="activeTab==='software' ? 'tab-btn-active' : ''"
          @click="activeTab='software'"
        >软件资产</button>
      </div>

      <!-- Overview panels -->
      <section v-if="activeTab==='overview'" class="grid grid-cols-1 gap-4">
        <!-- Device overview -->
        <div class="card-base card-padding">
          <div class="text-sm font-semibold mb-3">设备概览</div>
          <div class="text-xs text-slate-300 space-y-2">
            <div><span class="text-slate-500">主机名：</span><span class="font-mono">{{ hostHostname || '未知' }}</span></div>
            <div><span class="text-slate-500">硬件指纹：</span><span class="font-mono break-all">{{ hostFingerprint || device.deviceId }}</span></div>
            <div><span class="text-slate-500">采集时间：</span><span class="font-mono break-all">{{ hostCollectedAt || hw?.collected_at || '未知' }}</span></div>
          </div>
        </div>
        <!-- Alarm summary -->
        <div class="card-base card-padding">
          <div class="flex items-center justify-between mb-2">
            <div class="text-sm font-semibold">告警摘要</div>
            <div class="text-[11px] text-slate-500">概览汇总</div>
          </div>
          <div v-if="alertSummaryItems.length" class="flex flex-wrap gap-2">
            <span
              v-for="(a, i) in alertSummaryItems"
              :key="i"
              class="text-[11px] px-2 py-0.5 rounded border"
              :class="a.level === 'critical' ? 'border-red-700 bg-red-900/30 text-red-300' : a.level === 'warning' ? 'border-amber-700 bg-amber-900/30 text-amber-300' : 'border-slate-700 bg-slate-900/30 text-slate-300'"
            >
              {{ a.text }}
            </span>
          </div>
          <div v-else class="text-[12px] text-slate-500">暂无告警</div>
        </div>

        <!-- Realtime metrics (split into functional cards) -->
        <div v-if="false" class="lg:col-span-2 grid grid-cols-1 md:grid-cols-2 gap-4 items-stretch">
          <!-- CPU / Memory card -->
          <div class="bg-slate-900/50 rounded-lg border border-slate-800 p-4 h-full flex flex-col">
            <div class="text-sm font-semibold mb-3">CPU / 内存</div>
            <div>
              <div class="flex justify-between text-xs text-slate-300 mb-1">
                <span>CPU 占用率</span><span class="font-mono">{{ percent(cpuTotal) }}%</span>
              </div>
              <div class="progress-track">
                <div class="progress-bar-emerald" :style="{ width: percent(cpuTotal) + '%' }"></div>
              </div>
              <div class="mt-2">
                <div class="flex items-center justify-between text-[11px] text-slate-500 mb-1">
                  <span>CPU 5 分钟趋势</span>
                  <span v-if="cpuHistory.length" class="font-mono">{{ cpuHistory.length }}点</span>
                </div>
                <div class="h-8 bg-slate-950/30 border border-slate-800 rounded px-2 py-1 flex items-center">
                  <svg v-if="cpuSpark" viewBox="0 0 200 40" class="w-full h-full" preserveAspectRatio="none">
                    <polyline :points="cpuSpark" fill="none" stroke="#34d399" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" />
                  </svg>
                  <div v-else class="text-[11px] text-slate-500 w-full text-center">暂无数据</div>
                </div>
              </div>
            </div>
            <div class="mt-4">
              <div class="flex justify-between text-xs text-slate-300 mb-1">
                <span>内存使用率</span><span class="font-mono">{{ percent(memUsedPercent) }}%</span>
              </div>
              <div class="progress-track">
                <div class="progress-bar-sky" :style="{ width: percent(memUsedPercent) + '%' }"></div>
              </div>
              <div class="mt-2">
                <div class="flex items-center justify-between text-[11px] text-slate-500 mb-1">
                  <span>内存 5 分钟趋势</span>
                  <span v-if="memHistory.length" class="font-mono">{{ memHistory.length }}点</span>
                </div>
                <div class="h-8 bg-slate-950/30 border border-slate-800 rounded px-2 py-1 flex items-center">
                  <svg v-if="memSpark" viewBox="0 0 200 40" class="w-full h-full" preserveAspectRatio="none">
                    <polyline :points="memSpark" fill="none" stroke="#38bdf8" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" />
                  </svg>
                  <div v-else class="text-[11px] text-slate-500 w-full text-center">暂无数据</div>
                </div>
              </div>
              <div class="grid grid-cols-3 gap-2 text-[11px] text-slate-400 mt-3">
                <div><div class="text-slate-500 mb-1">总内存</div><div class="font-mono">{{ formatBytes(memTotal) }}</div></div>
                <div><div class="text-slate-500 mb-1">已用</div><div class="font-mono">{{ formatBytes(memUsed) }}</div></div>
                <div><div class="text-slate-500 mb-1">磁盘使用率</div><div class="font-mono">{{ percent(diskUsedPercent) }}%</div></div>
              </div>
            </div>
          </div>

          <!-- Network overview card -->
          <div class="card-base card-padding h-full">
            <div class="text-sm font-semibold mb-3">网络概览</div>
            <div class="text-xs text-slate-300 space-y-1">
              <div class="text-slate-500">网络累计收发</div>
              <div class="font-mono">↑ {{ formatBytes(netSent) }} / ↓ {{ formatBytes(netRecv) }}</div>
              <div class="text-slate-500">当前速率</div>
              <div class="font-mono">↑ {{ formatRateBytesPerSec(txRate) }} / ↓ {{ formatRateBytesPerSec(rxRate) }}</div>
            <div class="mt-1">
              <div class="flex items-center justify-between text-[11px] text-slate-500 mb-1">
                <span>5 分钟速率趋势（总吞吐）</span>
                <span v-if="rateHistory.length" class="font-mono">{{ rateHistory.length }}点</span>
              </div>
              <div class="h-8 bg-slate-950/30 border border-slate-800 rounded px-2 py-1 flex items-center">
                <svg v-if="rateSpark5m" viewBox="0 0 200 40" class="w-full h-full" preserveAspectRatio="none">
                  <polyline :points="rateSpark5m" fill="none" stroke="#a78bfa" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" />
                </svg>
                <div v-else class="text-[11px] text-slate-500 w-full text-center">暂无数据</div>
              </div>
            </div>
              <div class="text-slate-500">日累计流量（24小时）</div>
              <div class="font-mono">
                ↑ {{ dailyTxBytes != null ? formatBytes(dailyTxBytes) : "-" }} / ↓ {{ dailyRxBytes != null ? formatBytes(dailyRxBytes) : "-" }}
              </div>
              <div class="text-slate-500">小时流量（1小时）</div>
              <div class="font-mono">
                ↑ {{ hourlyTxBytes != null ? formatBytes(hourlyTxBytes) : "-" }} / ↓ {{ hourlyRxBytes != null ? formatBytes(hourlyRxBytes) : "-" }}
              </div>
              <div class="text-slate-500">最高瞬时流量（1小时）</div>
              <div class="font-mono">
                ↑ {{ maxTxRateInHour != null ? formatRateBytesPerSec(maxTxRateInHour) : "-" }} / ↓ {{ maxRxRateInHour != null ? formatRateBytesPerSec(maxRxRateInHour) : "-" }}
              </div>
              <div v-if="networkAlertText" class="text-[11px] text-red-300 animate-pulse mt-1">
                {{ networkAlertText }}
              </div>
            </div>
          </div>

          <!-- Latency trend card -->
          <div class="card-base card-padding h-full flex flex-col card-panel panel-chart-45">
            <div class="text-sm font-semibold mb-3">延迟趋势（RTT）</div>
            <div class="text-[11px] text-slate-500 mb-1">
              {{ pingHistory.length ? `${pingHistory.length}点` : "暂无数据" }} · 当前：{{ pingLatencyMs ? `${pingLatencyMs} ms` : "未采集" }}
            </div>
            <div class="h-8 bg-slate-950/30 border border-slate-800 rounded px-2 py-1 flex items-center">
              <svg v-if="pingSpark" viewBox="0 0 200 40" class="w-full h-full" preserveAspectRatio="none">
                <polyline :points="pingSpark" fill="none" stroke="#f472b6" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" />
              </svg>
              <div v-else class="text-[11px] text-slate-500 w-full text-center">暂无数据</div>
            </div>
          </div>

          <!-- NIC details card -->
          <div class="md:col-span-2 card-base card-padding card-panel panel-table-60">
            <div class="text-sm font-semibold mb-2">网卡详情</div>
            <div class="text-[11px] text-slate-500 mb-1">共 {{ netIfaceRows.length }} 个活跃接口</div>
            <div v-if="netIfaceRows.length" class="overflow-x-auto">
              <table class="min-w-full text-xs">
                <thead class="bg-slate-800 text-slate-300 sticky top-0 z-10">
                  <tr>
                    <th class="px-2 py-1 text-left">接口</th>
                    <th class="px-2 py-1 text-left">RX速率</th>
                    <th class="px-2 py-1 text-left">TX速率</th>
                    <th class="px-2 py-1 text-left">丢包Δ</th>
                    <th class="px-2 py-1 text-left">丢包率</th>
                    <th class="px-2 py-1 text-left">告警</th>
                  </tr>
                </thead>
                <tbody class="bg-slate-900/20">
                  <tr v-for="row in netIfaceRows" :key="row.name" class="border-t border-slate-800/60">
                    <td class="px-2 py-1 font-mono text-slate-200 break-all">{{ row.name }}</td>
                    <td class="px-2 py-1 font-mono text-slate-400 break-all">
                      {{ row.rxRateBps != null ? formatRateBytesPerSec(row.rxRateBps) : "-" }}
                    </td>
                    <td class="px-2 py-1 font-mono text-slate-400 break-all">
                      {{ row.txRateBps != null ? formatRateBytesPerSec(row.txRateBps) : "-" }}
                    </td>
                    <td class="px-2 py-1 font-mono text-slate-500">
                      {{ row.dropDelta != null ? roundInt(row.dropDelta) : "-" }}
                    </td>
                    <td
                      class="px-2 py-1 font-mono"
                      :class="row.dropRatePct != null && Number(row.dropRatePct) > 1 ? 'text-red-300' : 'text-slate-500'"
                    >
                      {{ row.dropRatePct != null ? formatPercent2(row.dropRatePct) : '-' }}
                    </td>
                    <td class="px-2 py-1">
                      <span
                        v-if="ifaceSpike(row.name)"
                        class="text-[11px] px-1.5 py-0.5 rounded border border-amber-700 bg-amber-900/30 text-amber-300"
                      >吞吐突增</span>
                    </td>
                  </tr>
                </tbody>
              </table>
            </div>
            <div v-else class="text-slate-500 text-[11px] mt-1">未采集网卡丢包/流量</div>
          </div>

          <!-- Disk I/O performance card -->
          <div class="md:col-span-2 card-base card-padding card-panel panel-chart-45">
            <div class="text-sm font-semibold mb-2">磁盘 I/O 性能</div>
            <div class="flex items-center justify-between text-[11px] text-slate-500 mb-1">
              <div class="flex items-center gap-2">
                <span>设备：</span>
                <select v-model="selectedDisk" class="bg-slate-950/60 border border-slate-800 rounded px-1.5 py-0.5 text-[11px] outline-none">
                  <option v-for="k in Object.keys(diskIoHist)" :key="k" :value="k">{{ k }}</option>
                </select>
              </div>
              <div class="font-mono">
                {{ selectedDisk ? selectedDisk : '未选择' }}
              </div>
            </div>
            <div class="h-16 bg-slate-950/30 border border-slate-800 rounded px-2 py-1 flex items-center relative">
              <svg v-if="selectedDiskHist.length" viewBox="0 0 300 60" class="w-full h-full" preserveAspectRatio="none" @mousemove="onDiskMouseMove" @mouseleave="onDiskMouseLeave">
                <polyline :points="diskSparkRW.r" fill="none" stroke="#22c55e" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" />
                <polyline :points="diskSparkRW.w" fill="none" stroke="#f59e0b" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" />
              </svg>
              <div v-else class="text-[11px] text-slate-500 w-full text-center">暂无 I/O 数据（可能正在初始化）</div>
              <div v-if="selectedDiskHist.length" class="absolute right-2 bottom-1 text-[11px] text-slate-300 font-mono">
                {{ diskHoverText }}
              </div>
            </div>
            <div v-if="selectedDiskHist.length" class="mt-2">
              <div class="flex items-center justify-between text-[11px] text-slate-400 mb-1">
                <span>I/O 利用率（近采样）</span>
                <span class="font-mono">{{ selectedDiskUtil.toFixed(1) }}%</span>
              </div>
              <div class="progress-track h-1.5">
                <div class="progress-bar-emerald" :style="{ width: selectedDiskUtil.toFixed(1) + '%' }"></div>
              </div>
            </div>
            <div v-if="selectedDiskHist.length" class="text-[11px] text-slate-500 mt-1">
              绿色=读速率(KB/s)，橙色=写速率(KB/s)；悬停显示 IOPS
            </div>
          </div>
        </div>

      </section>

      <!-- Storage & Hardware -->
      <div v-if="activeTab==='hardware'" class="card-base card-padding">
          <div class="flex items-center justify-between mb-3">
            <div class="text-sm font-semibold">硬件详情</div>
            <div class="text-xs text-slate-500">hardware_details</div>
          </div>
          <!-- Hardware health (SMART) -->
          <div class="mb-3">
            <div class="text-xs font-semibold text-slate-200 mb-2">硬件健康</div>
            <div v-if="hwSmart.length" class="grid grid-cols-1 md:grid-cols-2 lg:grid-cols-3 gap-2">
              <div
                v-for="(s, i) in hwSmart"
                :key="i"
                class="rounded border border-slate-800 p-2 bg-slate-950/40"
              >
                <div class="flex items-center justify-between">
                  <div class="text-[11px] text-slate-400">设备</div>
                  <div
                    class="text-[11px] px-1.5 py-0.5 rounded"
                    :class="String(s.status || s.Status).toUpperCase() === 'PASSED' ? 'bg-emerald-600/30 text-emerald-300' : 'bg-red-600/30 text-red-300 animate-pulse'"
                  >
                    {{ String(s.status || s.Status || 'UNKNOWN').toUpperCase() === 'PASSED' ? '健康' : '预警' }}
                  </div>
                </div>
                <div class="mt-1 text-xs text-slate-300 font-mono break-all">
                  {{ s.device_name || s.DeviceName || '-' }}
                </div>
                <div class="mt-1 text-[11px] text-slate-400 font-mono break-all">
                  {{ s.model || s.Model || '-' }}
                </div>
                <div class="mt-1 text-[11px] text-slate-300">
                  温度：<span class="font-mono">{{ (s.temperature ?? s.Temperature) ?? '-' }}</span> ℃
                </div>
                <div class="mt-1 text-[11px] text-slate-300">
                  通电：<span class="font-mono">{{ powerOnText(s.power_on_hours ?? s.PowerOnHours) }}</span>
                </div>
              </div>
            </div>
            <div v-else class="text-[11px] text-slate-500">未采集 SMART 健康（工具未安装或权限不足）</div>
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
              <div v-if="hwDisks.length" class="overflow-x-auto panel-table-60 scroll-area scroll-dark">
                <table class="min-w-full text-xs">
                  <thead class="bg-slate-800 text-slate-300 sticky top-0 z-10">
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
              <div v-else-if="hwMemorySlots.length" class="overflow-x-auto panel-table-60 scroll-area scroll-dark">
                <table class="min-w-full text-xs">
                  <thead class="bg-slate-800 text-slate-300 sticky top-0 z-10">
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
              <div v-if="hwIfaces.length" class="overflow-x-auto panel-table-60 scroll-area scroll-dark">
                <table class="min-w-full text-xs">
                  <thead class="bg-slate-800 text-slate-300 sticky top-0 z-10">
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

      <!-- Security Audit -->
      <div v-if="activeTab==='security'" class="grid grid-cols-1 lg:grid-cols-3 gap-4 items-stretch">
        <template v-if="securityPanelLoading">
          <div class="lg:col-span-3 flex-1 p-4 space-y-4">
            <div class="card-base card-padding animate-pulse space-y-3">
              <div class="h-5 w-56 bg-slate-800 rounded"></div>
              <div class="h-12 w-40 bg-slate-800 rounded"></div>
              <div class="h-8 w-full bg-slate-800/60 rounded"></div>
              <div class="h-56 bg-slate-800/40 rounded"></div>
            </div>
          </div>
        </template>
        <template v-else>
          <!-- Left: exposure overview -->
          <div class="lg:col-span-1 card-base card-padding">
            <div class="text-sm font-semibold mb-1">暴露风险概览</div>
            <div class="text-3xl font-bold text-red-200 mt-2">
              {{ publicListeningCount }}
            </div>
            <div class="text-xs text-slate-500 mt-1">公网监听（scope=public）</div>
          </div>

          <!-- Right: port scan list -->
          <div class="lg:col-span-2 card-base card-padding">
            <div class="flex items-center justify-between mb-2">
              <div class="text-sm font-semibold">端口扫描清单</div>
              <div class="text-xs text-slate-500">共 {{ (securityListeningRowsCache?.length ?? 0) }} 条</div>
            </div>
            <div v-if="(securityListeningRowsCache?.length ?? 0) > 0" class="panel-table-60 scroll-area scroll-dark">
              <table class="min-w-full text-xs">
                <thead class="bg-slate-800 text-slate-300 sticky top-0 z-10">
                  <tr>
                    <th class="px-3 py-2 text-left">协议</th>
                    <th class="px-3 py-2 text-left">端口</th>
                    <th class="px-3 py-2 text-left">监听地址</th>
                    <th class="px-3 py-2 text-left">风险等级</th>
                  </tr>
                </thead>
                <tbody class="bg-slate-900/30">
                  <tr
                    v-for="(p, i) in securityListeningRowsCache"
                    :key="p?.key || `${p?.pid || 0}-${p?.port || 0}`"
                    class="border-t border-slate-800/60"
                    :class="p?.riskLevel === 'CRITICAL' ? 'bg-red-500/10' : (p?.riskLevel === 'WARNING' ? 'bg-amber-500/10' : (p?.riskLevel === 'AUTHORIZED' ? 'bg-emerald-500/10' : ''))"
                    @contextmenu.prevent="onSecurityRowContextMenu(p, $event)"
                  >
                    <td class="px-3 py-2 font-mono text-slate-100">{{ (p?.protocol || '').toUpperCase() || '-' }}</td>
                    <td class="px-3 py-2">
                      <div class="flex items-center gap-2">
                        <button
                          class="font-mono text-sky-300 hover:text-sky-200 underline underline-offset-2"
                          @click="jumpToPort(p)"
                          :disabled="!p?.port"
                          :title="p?.port ? '在进程列表中查找该端口' : ''"
                        >
                          {{ p?.port ?? '-' }}
                        </button>
                        <span
                          v-if="p?.riskLevel === 'CRITICAL'"
                          class="w-2.5 h-2.5 rounded-full bg-red-400 animate-ping"
                          title="CRITICAL"
                        ></span>
                        <span
                          v-else-if="p?.riskLevel === 'WARNING'"
                          class="w-2 h-2 rounded-full bg-amber-300 opacity-90"
                          title="WARNING"
                        ></span>
                        <span
                          v-else-if="p?.riskLevel === 'AUTHORIZED'"
                          class="w-2 h-2 rounded-full bg-emerald-300 opacity-90"
                          title="AUTHORIZED"
                        ></span>
                      </div>
                    </td>
                    <td class="px-3 py-2 font-mono text-slate-400 break-all">{{ p?.address || '-' }}</td>
                    <td class="px-3 py-2">
                      <span class="relative inline-flex items-center group">
                        <span
                          class="text-[11px] px-2 py-0.5 rounded border font-mono"
                          :class="
                            p?.riskLevel === 'CRITICAL'
                              ? 'border-red-700 bg-red-500/10 text-red-300 animate-pulse'
                              : p?.riskLevel === 'WARNING'
                              ? 'border-amber-700 bg-amber-900/30 text-amber-300'
                              : p?.riskLevel === 'AUTHORIZED'
                              ? 'border-emerald-700 bg-emerald-500/10 text-emerald-300'
                              : 'border-slate-700 bg-slate-900/30 text-slate-300'
                          "
                        >
                          {{ p?.riskLevel || '-' }}
                        </span>
                        <!-- Tooltip only for CRITICAL -->
                        <div
                          v-if="p?.riskLevel === 'CRITICAL'"
                          class="pointer-events-none absolute left-1/2 top-full z-30 mt-2 w-72 -translate-x-1/2 opacity-0 transition-opacity duration-150 group-hover:opacity-100"
                        >
                          <div class="rounded border border-slate-700 bg-slate-950/95 px-3 py-2 text-[11px] text-slate-200 shadow-lg">
                            该高危端口正暴露在公网（0.0.0.0），可能面临暴力破解风险
                          </div>
                        </div>
                      </span>
                    </td>
                  </tr>
                </tbody>
              </table>
            </div>
            <div v-else class="text-slate-500 text-sm py-6 text-center">未发现监听端口</div>
          </div>
        </template>
      </div>

      <!-- Security audit context menu -->
      <teleport to="body">
        <div
          v-if="showSecurityContextMenu"
          class="fixed z-50 bg-slate-900 border border-slate-700 rounded shadow-lg text-xs text-slate-200"
          :style="{ left: securityContextMenuPos.x + 'px', top: securityContextMenuPos.y + 'px' }"
        >
          <button
            class="px-3 py-2 hover:bg-slate-700 w-full text-left"
            @click.stop="markPortAsWhitelisted"
          >
            标记为已知风险 / 白名单
          </button>
        </div>
      </teleport>

      <!-- Software Assets -->
      <div v-if="activeTab==='software'" class="grid grid-cols-1 gap-4">
        <template v-if="softwarePanelLoading">
          <div class="card-base card-padding animate-pulse space-y-3">
            <div class="h-5 w-56 bg-slate-800 rounded"></div>
            <div class="h-12 w-44 bg-slate-800 rounded"></div>
            <div class="h-8 w-full bg-slate-800/60 rounded"></div>
            <div class="h-56 bg-slate-800/40 rounded"></div>
          </div>
        </template>
        <template v-else>
          <div class="card-base card-padding">
            <div class="flex items-start justify-between gap-3 flex-wrap">
              <div>
                <div class="text-sm font-semibold">软件资产</div>
                <div class="text-xs text-slate-500 mt-1">
                  共发现 <span class="text-slate-200 font-semibold">{{ softwareListCache.length }}</span> 个安装包
                </div>
              </div>
              <div class="w-full max-w-sm">
                <div class="flex items-center gap-3">
                  <input
                    v-model="swQuery"
                    class="flex-1 bg-slate-950/40 border border-slate-800 rounded px-3 py-2 text-xs text-slate-200 placeholder:text-slate-600 focus:outline-none focus:ring-1 focus:ring-sky-500/50"
                    placeholder="实时搜索：按 名称 / 厂商 模糊过滤"
                  />
                  <label class="inline-flex items-center gap-1.5 text-[11px] text-slate-300 select-none">
                    <input v-model="swRiskOnly" type="checkbox" class="accent-red-500" />
                    仅显示有风险的软件
                  </label>
                </div>
                <div class="text-[11px] text-slate-500 mt-1">
                  当前显示 {{ filteredSoftware.length }} / {{ softwareListCache.length }}
                </div>
              </div>
            </div>
          </div>

          <div class="card-base card-padding card-panel panel-table-60 overflow-hidden">
            <div v-if="filteredSoftware && filteredSoftware.length" class="panel-table-60 scroll-area scroll-dark">
              <table class="min-w-full text-xs">
                <thead class="bg-slate-800 text-slate-300 sticky top-0 z-10">
                  <tr>
                    <th class="px-3 py-2 text-left">名称</th>
                    <th class="px-3 py-2 text-left">版本</th>
                    <th class="px-3 py-2 text-left">厂商</th>
                  </tr>
                </thead>
                <tbody class="bg-slate-900/30">
                  <tr
                    v-for="s in filteredSoftware"
                    :key="String(swName(s)) + ':' + String(swVersion(s)) + ':' + String(swPublisher(s))"
                    class="border-t border-slate-800/60"
                    :class="s?.is_vulnerable ? 'bg-red-500/10' : ''"
                  >
                    <td class="px-3 py-2 font-mono text-slate-200 break-all">
                      <span class="inline-flex items-center gap-1">
                        <span>{{ swName(s) || '未采集' }}</span>
                        <span
                          v-if="s?.is_vulnerable"
                          class="inline-flex items-center justify-center w-4 h-4 rounded-full bg-red-500/15 text-red-300 border border-red-500/40 text-[10px] leading-none"
                          title="检测到潜在安全漏洞，建议升级"
                        >!</span>
                      </span>
                    </td>
                    <td class="px-3 py-2 font-mono text-slate-400 break-all">{{ swVersion(s) || '-' }}</td>
                    <td class="px-3 py-2 font-mono text-slate-500 break-all">{{ swPublisher(s) || '-' }}</td>
                  </tr>
                </tbody>
              </table>
            </div>
            <div v-else class="text-slate-500 text-sm py-6 text-center">无软件数据</div>
          </div>
        </template>
      </div>


      <!-- Network -->
      <div v-if="activeTab==='network'" class="grid grid-cols-1 lg:grid-cols-3 gap-4 items-stretch">
        <!-- Network overview card -->
          <div class="card-base card-padding h-full">
          <div class="text-sm font-semibold mb-3">网络概览</div>
          <div class="text-xs text-slate-300 space-y-1">
            <div class="text-slate-500">当前速率</div>
            <div class="font-mono">↑ {{ formatRateBytesPerSec(txRate) }} / ↓ {{ formatRateBytesPerSec(rxRate) }}</div>
            <div class="text-slate-500">累计收发</div>
            <div class="font-mono">↑ {{ formatBytes(netSent) }} / ↓ {{ formatBytes(netRecv) }}</div>
            <div class="mt-1">
              <div class="flex items-center justify-between text-[11px] text-slate-500 mb-1">
                <span>5 分钟速率趋势（总吞吐）</span>
                <span v-if="rateHistory.length" class="font-mono">{{ rateHistory.length }}点</span>
              </div>
              <div class="h-10 bg-slate-950/30 border border-slate-800 rounded px-2 py-1 flex items-center">
                <svg v-if="rateSpark5m" viewBox="0 0 200 40" class="w-full h-full" preserveAspectRatio="none">
                  <polyline :points="rateSpark5m" fill="none" stroke="#a78bfa" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" />
                </svg>
                <div v-else class="text-[11px] text-slate-500 w-full text-center">暂无数据</div>
              </div>
            </div>
            <div class="text-slate-500">24h / 1h</div>
            <div class="font-mono">
              24h: ↑ {{ dailyTxBytes != null ? formatBytes(dailyTxBytes) : "-" }} / ↓ {{ dailyRxBytes != null ? formatBytes(dailyRxBytes) : "-" }}
            </div>
            <div class="font-mono">
              1h: ↑ {{ hourlyTxBytes != null ? formatBytes(hourlyTxBytes) : "-" }} / ↓ {{ hourlyRxBytes != null ? formatBytes(hourlyRxBytes) : "-" }}
            </div>
            <div class="text-slate-500">1h 峰值</div>
            <div class="font-mono">
              ↑ {{ maxTxRateInHour != null ? formatRateBytesPerSec(maxTxRateInHour) : "-" }} / ↓ {{ maxRxRateInHour != null ? formatRateBytesPerSec(maxRxRateInHour) : "-" }}
            </div>
            <div v-if="networkAlertText" class="text-[11px] text-red-300 animate-pulse mt-1">
              {{ networkAlertText }}
            </div>
          </div>
        </div>

        <!-- Latency card -->
          <div class="card-base card-padding h-full">
          <div class="text-sm font-semibold mb-3">延迟趋势（RTT）</div>
          <div class="text-[11px] text-slate-500 mb-1">
            {{ pingHistory.length ? `${pingHistory.length}点` : "暂无数据" }} · 当前：{{ pingLatencyMs ? `${pingLatencyMs} ms` : "未采集" }}
          </div>
          <div class="h-10 bg-slate-950/30 border border-slate-800 rounded px-2 py-1 flex items-center">
            <svg v-if="pingSpark" viewBox="0 0 200 40" class="w-full h-full" preserveAspectRatio="none">
              <polyline :points="pingSpark" fill="none" stroke="#f472b6" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" />
            </svg>
            <div v-else class="text-[11px] text-slate-500 w-full text-center">暂无数据</div>
          </div>
        </div>

        <!-- NIC details card -->
        <div class="lg:col-span-3 card-base card-padding">
          <div class="text-sm font-semibold mb-2">网卡详情</div>
          <div class="text-[11px] text-slate-500 mb-1">共 {{ netIfaceRows.length }} 个活跃接口</div>
          <div v-if="netIfaceRows.length" class="overflow-x-auto">
            <table class="min-w-full text-xs">
              <thead class="bg-slate-800 text-slate-300 sticky top-0 z-10">
                <tr>
                  <th class="px-2 py-1 text-left">接口</th>
                  <th class="px-2 py-1 text-left">RX速率</th>
                  <th class="px-2 py-1 text-left">TX速率</th>
                  <th class="px-2 py-1 text-left">丢包Δ</th>
                  <th class="px-2 py-1 text-left">丢包率</th>
                </tr>
              </thead>
              <tbody class="bg-slate-900/20">
                <tr v-for="row in netIfaceRows" :key="row.name" class="border-t border-slate-800/60">
                  <td class="px-2 py-1 font-mono text-slate-200 break-all">{{ row.name }}</td>
                  <td class="px-2 py-1 font-mono text-slate-400 break-all">
                    {{ row.rxRateBps != null ? formatRateBytesPerSec(row.rxRateBps) : "-" }}
                  </td>
                  <td class="px-2 py-1 font-mono text-slate-400 break-all">
                    {{ row.txRateBps != null ? formatRateBytesPerSec(row.txRateBps) : "-" }}
                  </td>
                  <td class="px-2 py-1 font-mono text-slate-500">
                    {{ row.dropDelta != null ? Math.round(row.dropDelta) : "-" }}
                  </td>
                  <td
                    class="px-2 py-1 font-mono"
                    :class="row.dropRatePct != null && row.dropRatePct > 1 ? 'text-red-300' : 'text-slate-500'"
                  >
                    {{ row.dropRatePct != null ? row.dropRatePct.toFixed(2) + '%' : '-' }}
                  </td>
                </tr>
              </tbody>
            </table>
          </div>
          <div v-else class="text-slate-500 text-[11px] mt-1">未采集网卡丢包/流量</div>
        </div>

        <!-- Connections cards -->
        <div class="lg:col-span-1 card-base p-3">
          <div class="flex items-center justify-between mb-2">
            <div class="text-sm font-semibold">监听端口</div>
          </div>
          <div class="panel-table-60 scroll-area scroll-dark">
            <table class="min-w-full text-xs">
              <thead class="bg-slate-800 text-slate-300 sticky top-0 z-10">
                <tr>
                  <th class="px-3 py-2 text-left">协议</th>
                  <th class="px-3 py-2 text-left">本地</th>
                  <th class="px-3 py-2 text-left">进程</th>
                </tr>
              </thead>
              <tbody class="bg-slate-900/30">
                <tr
                  v-for="(c, i) in connections.filter(c => (c.state || c.State || '').toUpperCase() === 'LISTEN').slice(0, 100)"
                  :key="'L'+i"
                  class="border-t border-slate-800/60"
                >
                  <td class="px-3 py-2 font-mono text-slate-100">{{ c.protocol || c.Protocol || '-' }}</td>
                  <td class="px-3 py-2 font-mono text-slate-400 break-all">
                    {{ (c.local_addr || c.LocalAddr || '-') + ':' + (c.local_port || c.LocalPort || '-') }}
                  </td>
                  <td class="px-3 py-2 font-mono text-slate-500">
                    {{ c.pid || c.PID || '-' }}
                  </td>
                </tr>
                <tr v-if="!connections.some(c => (c.state || c.State || '').toUpperCase() === 'LISTEN')" class="border-t border-slate-800/60">
                  <td colspan="3" class="px-3 py-2 text-slate-500 text-center">暂无监听端口</td>
                </tr>
              </tbody>
            </table>
          </div>
        </div>
        <div class="lg:col-span-2 card-base p-3">
          <div class="flex items-center justify-between mb-2">
            <div class="text-sm font-semibold">网络连接</div>
            <div class="text-xs text-slate-500">共 {{ connections.length }} 条</div>
          </div>
          <div v-if="connections.length" class="panel-table-60 scroll-area scroll-dark">
            <table class="min-w-full text-xs">
              <thead class="bg-slate-800 text-slate-300 sticky top-0 z-10">
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
          <div v-else class="text-slate-500 text-sm py-8 text-center">等待 Agent 上报 network_connections...</div>
        </div>
      </div>

      <!-- Compute & Processes -->
      <div v-if="activeTab==='compute'" class="card-base card-padding h-[70vh] flex flex-col overflow-hidden">
        <div class="flex items-center justify-between mb-2">
          <div class="text-sm font-semibold">算力与进程</div>
          <div class="text-xs text-slate-500">
            GPU {{ gpuStats.length }} 张 · 进程 {{ (host?.processes?.length ?? host?.Processes?.length ?? device.processes?.length ?? 0) }} 条
          </div>
        </div>

        <!-- Abnormal service alert bar -->
        <div v-if="(failedServices?.length ?? 0) > 0" class="mb-3 rounded-lg border border-rose-500/30 bg-rose-500/10 px-3 py-2 text-xs text-rose-200 flex items-center justify-between flex-shrink-0">
          <div class="flex items-center gap-2">
            <span class="inline-block w-2 h-2 rounded-full bg-rose-500"></span>
            <span class="font-semibold">异常服务预警</span>
            <span class="text-rose-200/80">共 {{ failedServices?.length ?? 0 }} 个</span>
          </div>
          <div class="flex flex-wrap justify-end gap-2 text-[11px] text-rose-200/90">
            <button
              class="px-2 py-0.5 rounded border border-rose-500/30 hover:bg-rose-500/15"
              @click="ignoreCurrentFailed"
              title="仅本机前端忽略（LocalStorage）"
            >一键忽略</button>
            <button
              v-for="s in (failedServices || []).slice(0, 4)"
              :key="String(svcName(s))"
              class="px-2 py-0.5 rounded border border-rose-500/30 hover:bg-rose-500/15"
              @click="jumpToService(s)"
            >{{ svcName(s) }}<span v-if="svcExitCode(s)" class="opacity-80"> ({{ svcExitCode(s) }})</span></button>
            <span v-if="(failedServices?.length ?? 0) > 4" class="text-rose-200/70">+{{ (failedServices?.length ?? 0) - 4 }}</span>
          </div>
        </div>
        <div class="grid grid-cols-1 lg:grid-cols-2 gap-3 mb-3">
          <div class="card-base card-padding">
            <div class="text-xs font-semibold text-slate-200 mb-2">GPU 状态</div>
            <div v-if="gpuStats.length" class="space-y-2">
              <div v-for="(g, i) in gpuStats" :key="i" class="rounded border border-slate-800 p-2 bg-slate-900/30">
                <div class="text-xs font-mono text-slate-200 break-all mb-1">{{ g.model || g.Model || 'GPU' }}</div>
                <div class="text-[11px] text-slate-400 mb-1">利用率：<span class="font-mono">{{ percent(g.util_percent ?? g.UtilPercent) }}%</span></div>
                <div class="h-2 bg-slate-800 rounded-full overflow-hidden mb-2">
                  <div class="h-full bg-emerald-500" :style="{ width: (Number(g.util_percent ?? g.UtilPercent) || 0) + '%' }"></div>
                </div>
                <div class="text-[11px] text-slate-400 mb-1">
                  显存：<span class="font-mono">
                    {{ Math.round(Number(g.memory_used_mb ?? g.MemoryUsedMB) || 0) }} / {{ Math.round(Number(g.memory_total_mb ?? g.MemoryTotalMB) || 0) }} MB
                  </span>
                </div>
                <div class="h-2 bg-slate-800 rounded-full overflow-hidden mb-2">
                  <div
                    class="h-full bg-sky-500"
                    :style="{
                      width: ((Number(g.memory_used_mb ?? g.MemoryUsedMB) || 0) / Math.max(1, Number(g.memory_total_mb ?? g.MemoryTotalMB) || 0) * 100).toFixed(1) + '%'
                    }"
                  ></div>
                </div>
                <div class="text-[11px] text-slate-400">
                  温度：<span class="font-mono">{{ Number(g.temperature ?? g.TemperatureC) || 0 }} ℃</span>
                </div>
              </div>
            </div>
            <div v-else class="text-[11px] text-slate-500">未采集到 GPU 指标（NVIDIA 显卡需安装 nvidia-smi；macOS 为 best-effort）。</div>
          </div>
          <div class="card-base card-padding">
            <div class="text-xs font-semibold text-slate-200 mb-2">CPU / 内存概览</div>
            <div class="text-xs text-slate-300 space-y-2">
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
            </div>
          </div>
        </div>

        <!-- Key services card -->
        <div class="card-base card-padding mb-3 flex-shrink-0">
          <div class="flex items-center justify-between mb-2">
            <div class="text-xs font-semibold text-slate-200">关键服务</div>
            <div class="text-[11px] text-slate-500">Failed + 白名单（后端已过滤）</div>
          </div>
          <div v-if="(keyServices?.length ?? 0) > 0" class="grid grid-cols-1 md:grid-cols-2 gap-2">
            <button
              v-for="s in (keyServices || []).slice(0, 12)"
              :key="String(svcName(s))"
              class="text-left rounded-lg border border-slate-800 bg-slate-950/30 px-3 py-2 hover:bg-slate-900/30"
              @click="jumpToService(s)"
            >
              <div class="flex items-center justify-between">
                <div class="font-mono text-xs text-slate-200 break-all">{{ svcName(s) }}</div>
                <div
                  class="text-[11px] px-2 py-0.5 rounded border"
                  :class="String(svcStatus(s)).toLowerCase().includes('fail') ? 'border-rose-500/40 bg-rose-500/10 text-rose-200' : String(svcStatus(s)).toLowerCase().includes('active') || String(svcStatus(s)).toLowerCase().includes('run') ? 'border-emerald-500/40 bg-emerald-500/10 text-emerald-200' : 'border-slate-700 bg-slate-900/30 text-slate-300'"
                >{{ svcStatus(s) || '-' }}</div>
              </div>
              <div class="mt-1 text-[11px] text-slate-500 flex items-center justify-between">
                <span class="font-mono">Exit {{ svcExitCode(s) }}</span>
                <span v-if="svcUptimeSec(s) > 0" class="font-mono">{{ formatUptime(svcUptimeSec(s)) }}</span>
              </div>
            </button>
          </div>
          <div v-else class="text-[11px] text-slate-500">暂无服务数据（或当前无 Failed/白名单服务）。</div>
        </div>

        <div class="card-base flex flex-col flex-grow min-h-[500px] overflow-hidden">
          <div class="px-3 py-2 text-xs text-slate-300 bg-slate-800/60 flex items-center justify-between">
            <span>进程列表</span>
            <span class="text-slate-500">共 {{ (host?.processes?.length ?? host?.Processes?.length ?? device.processes?.length ?? 0) }} 条</span>
          </div>
          <div class="p-2 flex items-center gap-2">
            <input
              v-model="procQuery"
              class="w-full bg-slate-950/60 border border-slate-800 rounded-md px-2 py-1 text-xs outline-none focus:ring-2 focus:ring-cyan-500"
              placeholder="搜索 PID / 名称 / 路径"
            />
            <div class="text-[11px] text-slate-500 whitespace-nowrap px-2">
              共 {{ filteredProcesses?.length ?? 0 }} 条
            </div>
          </div>
          <div v-if="filteredProcesses && filteredProcesses.length" class="flex-1 min-h-0 panel-table-60 scroll-area scroll-dark">
            <table class="min-w-full text-xs">
              <thead class="bg-slate-800 text-slate-300 sticky top-0 z-10">
                <tr>
                  <th class="px-3 py-2 text-left w-[72px]">PID</th>
                  <th class="px-3 py-2 text-left w-[160px]">名称</th>
                  <th class="px-3 py-2 text-left min-w-[120px] w-[140px] whitespace-nowrap">用户</th>
                  <th class="px-3 py-2 text-left min-w-[120px] w-[140px] whitespace-nowrap">备注</th>
                  <th class="px-3 py-2 text-left min-w-[200px]">路径</th>
                  <th class="px-3 py-2 text-left w-[110px] whitespace-nowrap">内存</th>
                  <th class="px-3 py-2 text-left min-w-[120px] w-[170px] whitespace-nowrap">监听端口</th>
                  <th class="px-3 py-2 text-left w-[96px] whitespace-nowrap">操作</th>
                </tr>
              </thead>
              <tbody class="bg-slate-900/30">
                <tr
                  v-for="p in pagedProcesses"
                  :key="String(procPid(p))"
                  class="border-t border-slate-800/60"
                  :class="highlightPid === Number(procPid(p)) ? 'row-highlight' : ''"
                  :id="`proc-${Number(procPid(p))}`"
                >
                  <td class="px-3 py-2 font-mono text-slate-100">{{ procPid(p) }}</td>
                  <td class="px-3 py-2 font-mono text-slate-200 truncate max-w-[160px]">
                    {{ procName(p) || "-" }}
                  </td>
                  <td class="px-3 py-2 font-mono text-slate-300 whitespace-nowrap">
                    {{ procUsername(p) || "-" }}
                  </td>
                  <td class="px-3 py-2 text-[11px] text-slate-300 whitespace-nowrap">
                    <span
                      v-if="procServiceName(p)"
                      class="inline-flex items-center px-2 py-0.5 rounded border border-indigo-500/30 bg-indigo-500/10 text-indigo-200 font-mono"
                    >{{ procServiceName(p) }}</span>
                    <span v-else class="text-slate-400 font-mono">{{ procRemark(p) }}</span>
                  </td>
                  <td class="px-3 py-2 font-mono text-slate-400 break-all">
                    {{ procExecPath(p) ? shorten(procExecPath(p), 60) : "未采集" }}
                  </td>
                  <td class="px-3 py-2 font-mono text-slate-500 whitespace-nowrap">
                    {{ formatBytes(procMemoryBytes(p)) }}
                  </td>
                  <td class="px-3 py-2">
                    <div class="flex flex-wrap gap-1">
                      <span
                        v-for="port in procListenPorts(p).slice(0, 8)"
                        :key="port"
                        class="text-[10px] px-1.5 py-0.5 rounded border font-mono"
                        :class="isWebPort(port) ? 'bg-emerald-500/10 border-emerald-500/40 text-emerald-200' : 'bg-slate-900/40 border-slate-700 text-slate-300'"
                      >{{ port }}</span>
                      <span v-if="procListenPorts(p).length > 8" class="text-[10px] text-slate-500">+{{ procListenPorts(p).length - 8 }}</span>
                    </div>
                  </td>
                  <td class="px-3 py-2">
                    <button
                      class="text-[11px] px-2 py-1 rounded border border-rose-500/40 bg-rose-500/10 text-rose-200 hover:bg-rose-500/20"
                      disabled
                      title="暂未实现：结束进程"
                    >结束进程</button>
                  </td>
                </tr>
              </tbody>
            </table>
            <div class="flex items-center justify-end gap-2 p-2 text-[11px] text-slate-400">
              <button
                class="px-2 py-1 rounded border border-slate-700 hover:bg-slate-800 disabled:opacity-50"
                :disabled="procPage <= 1"
                @click="procPage = Math.max(1, procPage - 1)"
              >上一页</button>
              <span>第 <span class="font-mono">{{ procPage }}</span> / <span class="font-mono">{{ procPageCount }}</span> 页</span>
              <button
                class="px-2 py-1 rounded border border-slate-700 hover:bg-slate-800 disabled:opacity-50"
                :disabled="procPage >= procPageCount"
                @click="procPage = Math.min(procPageCount, procPage + 1)"
              >下一页</button>
            </div>
          </div>
          <div v-else class="flex-1 min-h-[260px] flex items-center justify-center text-slate-500 text-sm py-8">
            暂无进程数据
          </div>
        </div>
      </div>
    </div>
  </div>
</template>

<style scoped>
.card-panel {
  min-height: 12rem;   /* 统一最小高度，内容不足时不至于过矮 */
  max-height: 60vh;    /* 统一最高高度，避免页面被撑得过高 */
  overflow: auto;      /* 超出出现滚动条 */
}

/* 2s fade highlight for jumped row */
@keyframes rowFade {
  0% { background-color: rgba(245, 158, 11, 0.18); }
  100% { background-color: rgba(245, 158, 11, 0); }
}
.row-highlight {
  animation: rowFade 2s ease-out both;
}
</style>
