import axios, { AxiosError } from "axios";

// Prefer explicit API base for production; otherwise default to same-origin
// so Vite dev server `/api` proxy can forward to the Go backend.
const baseURL = (import.meta.env.VITE_API_BASE_URL as string | undefined) ?? "";

export const request = axios.create({
  baseURL,
  timeout: 10000,
});

let lastErrorNotifyAt = 0;
let lastErrorText = "";

function notifyError(message: string) {
  const text = String(message || "").trim() || "请求失败，请稍后重试";
  const now = Date.now();
  // Avoid flooding users with identical alerts in a short burst.
  if (text === lastErrorText && now-lastErrorNotifyAt < 2000) return;
  lastErrorText = text;
  lastErrorNotifyAt = now;
  if (typeof window !== "undefined" && typeof window.alert === "function") {
    window.alert(text);
  }
}

function extractErrorMessage(err: unknown): string {
  const e = err as AxiosError<any>;
  const status = e?.response?.status;
  const data = e?.response?.data as any;
  const backendMsg =
    String(data?.error || data?.message || data?.msg || "").trim();
  if (status) {
    return backendMsg || `请求失败（HTTP ${status}）`;
  }
  if (e?.code === "ECONNABORTED") return "请求超时，请检查网络或稍后重试";
  return backendMsg || e?.message || "请求异常，请稍后重试";
}

request.interceptors.response.use(
  (resp) => resp,
  (err) => {
    const msg = extractErrorMessage(err);
    notifyError(msg);
    return Promise.reject(err);
  }
);
