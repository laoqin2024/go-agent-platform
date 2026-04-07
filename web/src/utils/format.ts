export function formatSpeed(bytesPerSec: unknown): string {
  const n = Number(bytesPerSec);
  if (!Number.isFinite(n) || n <= 0) return "0 B/s";
  // Clamp absurdly large values to avoid UI overflow and keep units sane.
  const MAX_SAFE_BPS = 10 * 1024 * 1024 * 1024 * 1024; // 10 TB/s cap
  const capped = Math.min(n, MAX_SAFE_BPS);
  const units = ["B/s", "KB/s", "MB/s", "GB/s", "TB/s"];
  let v = capped;
  let i = 0;
  while (v >= 1024 && i < units.length - 1) {
    v /= 1024;
    i++;
  }
  const num = v.toFixed(v >= 100 ? 0 : v >= 10 ? 1 : 2);
  const suffix = units[i];
  if (n > MAX_SAFE_BPS) {
    return `${num} ${suffix}+`;
  }
  return `${num} ${suffix}`;
}

export function formatBytes(bytes: unknown): string {
  const n = Number(bytes);
  if (!Number.isFinite(n) || n <= 0) return "0 B";
  const units = ["B", "KB", "MB", "GB", "TB"];
  let v = n;
  let i = 0;
  while (v >= 1024 && i < units.length - 1) {
    v /= 1024;
    i++;
  }
  return `${v.toFixed(v >= 100 ? 0 : v >= 10 ? 1 : 2)} ${units[i]}`;
}
