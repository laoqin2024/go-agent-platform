import axios from "axios";

// Prefer explicit API base for production; otherwise default to same-origin
// so Vite dev server `/api` proxy can forward to the Go backend.
const baseURL = (import.meta.env.VITE_API_BASE_URL as string | undefined) ?? "";

export const request = axios.create({
  baseURL,
  timeout: 10000,
});

