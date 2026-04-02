import { ref } from "vue";

export type WSConnectionStatus = "online" | "reconnecting" | "offline";

export function useWebSocket(onMessage: (data: any) => void) {
  const status = ref<WSConnectionStatus>("offline");
  const ws = ref<WebSocket | null>(null);

  let manualClose = false;
  let deviceId: string | null = null;

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

    const apiBase = import.meta.env.VITE_API_BASE_URL as
      | string
      | undefined;
    let url: string;
    if (apiBase) {
      // Convert http(s) baseURL to ws(s).
      try {
        const u = new URL(apiBase);
        const proto = u.protocol === "https:" ? "wss" : "ws";
        // Ensure we don't end up with double slashes.
        const base = u.pathname.endsWith("/") ? u.pathname.slice(0, -1) : u.pathname;
        url = `${proto}://${u.host}${base}/ws/${encodeURIComponent(id)}`;
      } catch {
        url = `${window.location.protocol === "https:" ? "wss" : "ws"}://${window.location.host}/ws/${encodeURIComponent(id)}`;
      }
    } else {
      url = `${window.location.protocol === "https:" ? "wss" : "ws"}://${window.location.host}/ws/${encodeURIComponent(id)}`;
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

    socket.onopen = () => {
      status.value = "online";
      // Reset backoff after successful connect.
      delayMs = 2000;
    };

    socket.onmessage = (ev) => {
      try {
        const parsed = typeof ev.data === "string" ? JSON.parse(ev.data) : ev.data;
        onMessage(parsed);
      } catch {
        // ignore invalid message
      }
    };

    socket.onclose = () => {
      ws.value = null;
      scheduleReconnect();
    };

    socket.onerror = () => {
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

