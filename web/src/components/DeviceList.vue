<script setup lang="ts">
import { computed, ref } from "vue";

type Props = {
  devices: Array<{
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
  }>;
  selectedDeviceId: string;
};

const props = defineProps<Props>();
const emit = defineEmits<{
  (e: "select", deviceId: string): void;
}>();

const q = ref("");
const onlineOnly = ref(true);

type Row = Props["devices"][number];

function normOS(os?: string) {
  const v = (os ?? "").trim();
  return v || "OS 未知";
}

function clampPct(v: any) {
  const n = Number(v ?? 0);
  if (!Number.isFinite(n)) return 0;
  return Math.min(100, Math.max(0, n));
}

function ifaceTypeLabel(v?: string) {
  if (v === "physical") return "有线";
  if (v === "wireless") return "无线";
  return "未知";
}

const filtered = computed<Row[]>(() => {
  const query = q.value.trim().toLowerCase();
  const list = [...(props.devices ?? [])].sort(
    (a, b) => (b.updated_at ?? 0) - (a.updated_at ?? 0)
  );
  const list2 = onlineOnly.value ? list.filter((d) => !!d.online) : list;
  if (!query) return list2;
  return list2.filter((d) => {
    const id = (d.device_id ?? "").toLowerCase();
    const hn = (d.hostname ?? "").toLowerCase();
    const os = (d.os ?? "").toLowerCase();
    const ip = (d.ip ?? "").toLowerCase();
    const mac = (d.mac ?? "").toLowerCase();
    return id.includes(query) || hn.includes(query) || os.includes(query) || ip.includes(query) || mac.includes(query);
  });
});

const grouped = computed(() => {
  const groups = new Map<string, Row[]>();
  for (const d of filtered.value) {
    const key = normOS(d.os);
    if (!groups.has(key)) groups.set(key, []);
    groups.get(key)!.push(d);
  }
  return Array.from(groups.entries()).sort((a, b) => a[0].localeCompare(b[0]));
});

function timeAgo(sec?: number) {
  if (!sec || sec <= 0) return "未知";
  const diff = Math.max(0, Math.floor(Date.now() / 1000 - sec));
  if (diff < 10) return "刚刚";
  if (diff < 60) return `${diff}s`;
  if (diff < 3600) return `${Math.floor(diff / 60)}m`;
  if (diff < 86400) return `${Math.floor(diff / 3600)}h`;
  return `${Math.floor(diff / 86400)}d`;
}
</script>

<template>
  <div class="h-full border-r border-slate-800 bg-slate-950/40">
    <div class="p-3 border-b border-slate-800">
      <div class="flex items-center justify-between">
        <div class="text-sm font-semibold">设备列表</div>
        <label class="flex items-center gap-2 text-xs text-slate-300 select-none">
          <input v-model="onlineOnly" type="checkbox" class="accent-cyan-400" />
          只看在线
        </label>
      </div>
      <div class="mt-2 flex gap-2">
        <input
          v-model="q"
          class="flex-1 rounded border border-slate-800 bg-slate-900/60 px-2 py-1 text-sm outline-none focus:ring-2 focus:ring-cyan-500"
          placeholder="搜索：hostname / OS / IP"
        />
      </div>
    </div>

    <div class="p-2 overflow-y-auto h-[calc(100%-56px)]">
      <div v-if="grouped.length === 0" class="text-sm text-slate-500 px-2 py-6 text-center">
        暂无设备
      </div>

      <div v-for="[osName, list] in grouped" :key="osName" class="mb-3">
        <div class="px-2 py-1 text-[11px] text-slate-400 flex items-center justify-between">
          <span class="uppercase tracking-wider">{{ osName }}</span>
          <span class="text-slate-500">{{ list.length }}</span>
        </div>

        <button
          v-for="d in list"
          :key="d.device_id"
          class="w-full text-left rounded px-3 py-2 mb-1 border border-transparent hover:border-slate-700 hover:bg-slate-900/50 transition"
          :class="d.device_id === selectedDeviceId ? 'border-cyan-500/70 bg-cyan-500/10' : ''"
          @click="emit('select', d.device_id)"
        >
          <div class="flex items-start justify-between gap-2">
            <div class="min-w-0 flex items-start gap-2">
              <span
                class="w-2 h-2 rounded-full shrink-0 mt-1.5"
                :class="d.online ? 'bg-emerald-400' : 'bg-slate-600'"
                :title="d.online ? '在线' : '离线'"
              ></span>
              <div class="min-w-0">
                <div class="text-sm text-slate-100 truncate">
                  {{ d.hostname || "未命名主机" }}
                </div>
                <div class="text-[11px] text-slate-400 truncate">
                  {{ d.ip || "IP 未知" }} · {{ timeAgo(d.updated_at) }}
                </div>
                <div class="text-[11px] text-slate-500 truncate mt-0.5">
                  {{ ifaceTypeLabel(d.iface_type) }} · MAC: {{ d.mac || "未知" }}
                </div>

                <div class="mt-2 grid grid-cols-2 gap-2">
                  <div>
                    <div class="flex items-center justify-between text-[10px] text-slate-400 mb-1">
                      <span>CPU</span><span class="font-mono">{{ clampPct(d.cpu_percent).toFixed(1) }}%</span>
                    </div>
                    <div class="h-1.5 bg-slate-800 rounded-full overflow-hidden">
                      <div class="h-full bg-emerald-400" :style="{ width: clampPct(d.cpu_percent) + '%' }"></div>
                    </div>
                  </div>
                  <div>
                    <div class="flex items-center justify-between text-[10px] text-slate-400 mb-1">
                      <span>Mem</span><span class="font-mono">{{ clampPct(d.mem_used_percent).toFixed(1) }}%</span>
                    </div>
                    <div class="h-1.5 bg-slate-800 rounded-full overflow-hidden">
                      <div class="h-full bg-sky-400" :style="{ width: clampPct(d.mem_used_percent) + '%' }"></div>
                    </div>
                  </div>
                </div>
              </div>
            </div>
          </div>
        </button>
      </div>
    </div>
  </div>
</template>

