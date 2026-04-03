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
        const parsed = typeof ev.data === "string" ? JSON.parse(ev.data) : ev.data;
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

