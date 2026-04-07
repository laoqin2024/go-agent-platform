import { ref } from "vue";

export type WSConnectionStatus = "online" | "reconnecting" | "offline";

export function useWebSocket(onMessage: (data: any, raw: any) => void) {
  const status = ref<WSConnectionStatus>("offline");
  const ws = ref<WebSocket | null>(null);

  let manualClose = false;
  let deviceId: string | null = null;
  let lastUrl: string | null = null;
  let consecutiveFailures = 0;

  // Exponential backoff: 2s, 4s, 8s...
  let delayMs = 2000;
  let timer: number | null = null;
  const maxDelayMs = 30000;

  // Anti-flicker / isolation
  let lastComputeTime = 0;
  let lastValidNetBytes: { sent: number; recv: number; ts: number } | null = null;
  let isFirstFrame = true;
  // Cache last good interface counters so we can "freeze" only network-related fields.
  // This avoids dropping the entire frame (which would also skip host_metrics.security_snapshot updates).
  let lastNetIfacesRaw: any[] | null = null;

  const clearTimer = () => {
    if (timer != null) {
      window.clearTimeout(timer);
      timer = null;
    }
  };

  const disconnect = () => {
    manualClose = true;
    clearTimer();
    if (ws.value) {
      try {
        ws.value.close();
      } catch {
        // ignore
      }
    }
    ws.value = null;
    status.value = "offline";
  };

  const scheduleReconnect = () => {
    if (manualClose || !deviceId) return;
    status.value = "reconnecting";
    clearTimer();
    const currentDelay = delayMs;
    delayMs = Math.min(delayMs * 2, maxDelayMs);
    timer = window.setTimeout(() => {
      // If deviceId is cleared meanwhile, do nothing.
      if (!deviceId) return;
      connect(deviceId);
    }, currentDelay);
  };

  const connect = (id: string) => {
    deviceId = id;
    manualClose = false;
    clearTimer();
    // Make UI feedback immediate; we'll switch to "online" on socket.onopen.
    status.value = "reconnecting";
    // Reset per-device WS caches to avoid leaking previous device state
    lastComputeTime = 0;
    lastValidNetBytes = null;
    isFirstFrame = true;

    let url: string;
    // In dev we MUST go through Vite so proxy `/ws` can forward to Go backend.
    if (import.meta.env.DEV) {
      const wsProtocol = window.location.protocol === "https:" ? "wss:" : "ws:";
      url = `${wsProtocol}//${window.location.host}/ws/${encodeURIComponent(id)}`;
    } else {
      // Fallback: same-origin WS.
      const wsProtocol = window.location.protocol === "https:" ? "wss:" : "ws:";
      url = `${wsProtocol}//${window.location.host}/ws/${encodeURIComponent(id)}`;
    }

    // Close previous socket if any.
    if (ws.value) {
      try {
        ws.value.close();
      } catch {
        // ignore
      }
    }

    // Reset backoff when we explicitly connect to a new device.
    delayMs = 2000;

    const socket = new WebSocket(url);
    ws.value = socket;
    lastUrl = url;

    socket.onopen = () => {
      status.value = "online";
      // Reset backoff after successful connect.
      delayMs = 2000;
      consecutiveFailures = 0;
      // Debug log
      // eslint-disable-next-line no-console
      console.log("[ws] open", { deviceId, url: lastUrl });
    };

    socket.onmessage = (ev) => {
      // eslint-disable-next-line no-console
      console.log("[ws] message", { deviceId, url: lastUrl, raw: ev.data });
      try {
        const now = Date.now();
        // Throttle: dt < 100ms => drop frame to avoid jitter
        if (now - lastComputeTime < 100) {
          return;
        }
        lastComputeTime = now;

        const parsed = typeof ev.data === "string" ? JSON.parse(ev.data) : ev.data;

        // 1) Single-device filter: message must belong to the active device (when available)
        const msgDeviceId =
          parsed?.device_id ??
          parsed?.deviceId ??
          parsed?.host_metrics?.device_id ??
          parsed?.hostMetrics?.device_id ??
          parsed?.host_metrics?.deviceId ??
          parsed?.hostMetrics?.deviceId ??
          null;
        if (deviceId && msgDeviceId && msgDeviceId !== deviceId) {
          return;
        }

        // 2) Gather network counters for guards and first-frame init
        const netIfaces =
          parsed?.host_metrics?.network_interfaces ??
          parsed?.hostMetrics?.network_interfaces ??
          parsed?.network_interfaces ??
          [];
        if (Array.isArray(netIfaces) && netIfaces.length > 0) {
          let sumSent = 0;
          let sumRecv = 0;
          for (const it of netIfaces) {
            const bs =
              Number(it?.bytes_sent ?? it?.BytesSent ?? it?.bytesSent ?? 0);
            const br =
              Number(it?.bytes_recv ?? it?.BytesRecv ?? it?.bytesRecv ?? 0);
            sumSent += Number.isFinite(bs) ? bs : 0;
            sumRecv += Number.isFinite(br) ? br : 0;
          }
          // First-frame guard: establish baseline only, do not forward to UI
          if (isFirstFrame) {
            lastValidNetBytes = { sent: sumSent, recv: sumRecv, ts: now };
            lastNetIfacesRaw = netIfaces;
            isFirstFrame = false;
            return;
          }
          // State freezing: reject frames with regressed or all-zero counters within 3s window
          if (lastValidNetBytes) {
            const regressed =
              sumSent < lastValidNetBytes.sent || sumRecv < lastValidNetBytes.recv;
            const isAllZero = sumSent === 0 && sumRecv === 0;
            const within3s = now - lastValidNetBytes.ts < 3000;
            if ((regressed || isAllZero) && within3s) {
              // Freeze network counters but still forward the frame so other host_metrics
              // fields (e.g. security_snapshot / listening ports) can update.
              const patched: any = parsed;
              const targetNetIfaces = lastNetIfacesRaw;
              if (targetNetIfaces && Array.isArray(targetNetIfaces)) {
                if (patched?.host_metrics?.network_interfaces !== undefined) {
                  patched.host_metrics.network_interfaces = targetNetIfaces;
                } else if (patched?.hostMetrics?.network_interfaces !== undefined) {
                  patched.hostMetrics.network_interfaces = targetNetIfaces;
                } else if (patched?.network_interfaces !== undefined) {
                  patched.network_interfaces = targetNetIfaces;
                } else {
                  // Best-effort: set both host_metrics and hostMetrics to keep downstream lookups working.
                  if (patched?.host_metrics) patched.host_metrics.network_interfaces = targetNetIfaces;
                  if (patched?.hostMetrics) patched.hostMetrics.network_interfaces = targetNetIfaces;
                }
              }

              // Also freeze total Network bytes if present (used by netSent/netRecv).
              if (lastValidNetBytes) {
                if (patched?.host_metrics?.Network) {
                  patched.host_metrics.Network.BytesSent = lastValidNetBytes.sent;
                  patched.host_metrics.Network.BytesRecv = lastValidNetBytes.recv;
                }
                if (patched?.host_metrics?.network_bytes_sent !== undefined) {
                  patched.host_metrics.network_bytes_sent = lastValidNetBytes.sent;
                }
                if (patched?.host_metrics?.network_bytes_recv !== undefined) {
                  patched.host_metrics.network_bytes_recv = lastValidNetBytes.recv;
                }
                if (patched?.hostMetrics?.Network) {
                  patched.hostMetrics.Network.BytesSent = lastValidNetBytes.sent;
                  patched.hostMetrics.Network.BytesRecv = lastValidNetBytes.recv;
                }
                if (patched?.hostMetrics?.network_bytes_sent !== undefined) {
                  patched.hostMetrics.network_bytes_sent = lastValidNetBytes.sent;
                }
                if (patched?.hostMetrics?.network_bytes_recv !== undefined) {
                  patched.hostMetrics.network_bytes_recv = lastValidNetBytes.recv;
                }
              }

              onMessage(patched, ev.data);
              return;
            }
          }
          lastValidNetBytes = { sent: sumSent, recv: sumRecv, ts: now };
          lastNetIfacesRaw = netIfaces;
        }

        onMessage(parsed, ev.data);
      } catch {
        // ignore invalid message
        // eslint-disable-next-line no-console
        console.warn("[ws] message parse error");
        onMessage(null, ev.data);
      }
    };

    socket.onclose = (ev) => {
      ws.value = null;
      // eslint-disable-next-line no-console
      console.log("[ws] close", { deviceId, url: lastUrl, code: ev.code, reason: ev.reason });
      consecutiveFailures++;
      // Auto-fallback: if dev proxy keeps failing, try direct backend port 8080 on same host.
      if (import.meta.env.DEV && consecutiveFailures >= 2) {
        const wsProtocol = window.location.protocol === "https:" ? "wss:" : "ws:";
        const direct = `${wsProtocol}//${window.location.hostname}:8080/ws/${encodeURIComponent(id)}`;
        lastUrl = direct;
        // eslint-disable-next-line no-console
        console.warn("[ws] proxy failed, trying direct backend", { direct });
        try {
          const alt = new WebSocket(direct);
          ws.value = alt;
          alt.onopen = () => {
            status.value = "online";
            delayMs = 2000;
            // eslint-disable-next-line no-console
            console.log("[ws] open (direct backend)", { deviceId, url: direct });
          };
          alt.onmessage = socket.onmessage!;
          alt.onclose = (e2) => {
            ws.value = null;
            // eslint-disable-next-line no-console
            console.log("[ws] close (direct backend)", { code: e2.code, reason: e2.reason });
            scheduleReconnect();
          };
          alt.onerror = () => {
            // eslint-disable-next-line no-console
            console.error("[ws] error (direct backend)");
            try { alt.close(); } catch {}
          };
          return; // Do not scheduleReconnect here; the alt handlers will.
        } catch {
          // Ignore and proceed to normal reconnect
        }
      }
      scheduleReconnect();
    };

    socket.onerror = () => {
      // eslint-disable-next-line no-console
      console.error("[ws] error", { deviceId, url: lastUrl });
      // Trigger reconnection path quickly by closing.
      try {
        socket.close();
      } catch {
        // ignore
      }
    };
  };

  return {
    status,
    connect,
    disconnect,
  };
}

