// API client + SSE stream with auto-reconnect.

export interface Target {
  id: number;
  name: string;
  host: string;
  tier: number;
  sort_order: number;
  enabled: boolean;
  created_at: number;
}

export interface TargetStatus extends Target {
  state: "up" | "down" | "unknown";
  last_rtt_us: number;
  loss_60s_pct: number;
  baseline_rtt_us: number;
  outage_since?: number;
}

export interface SpeedTest {
  id: number;
  engine: string;
  server_name: string;
  server_id: string;
  download_bps: number;
  upload_bps: number;
  latency_ms: number;
  loaded_latency_ms: number;
  packet_loss: number;
  ran_at: number;
  duration_ms: number;
  error: string;
}

export interface Status {
  version: string;
  ping_mode: string;
  started_at: number;
  now: number;
  internet: { state: "up" | "down"; outage_since?: number };
  targets: TargetStatus[];
  last_speedtest: SpeedTest | null;
  speedtest_running: boolean;
}

export interface Sample {
  target_id: number;
  ts: number;
  rtt_us: number;
  success: boolean;
}

export interface OutageEvent {
  id: number;
  target_id: number;
  started_at: number;
  ended_at: number | null;
  duration_ms: number | null;
}

export interface PingPoint {
  ts: number;
  rtt_avg_us: number | null;
  rtt_p95_us?: number;
  sent: number;
  lost: number;
  loss_pct: number;
}

export interface PingSeries {
  target_id: number;
  resolution: string;
  from: number;
  to: number;
  points: PingPoint[];
}

export interface TargetSummary {
  target_id: number;
  name: string;
  tier: number;
  uptime_pct: number;
  rtt_avg_us: number;
  rtt_p95_us: number;
  sent: number;
  lost: number;
  outage_count: number;
  outage_total_ms: number;
}

export interface Summary {
  range: string;
  from: number;
  to: number;
  targets: TargetSummary[];
  speedtest: {
    count: number;
    down_min_bps: number;
    down_avg_bps: number;
    down_max_bps: number;
    up_min_bps: number;
    up_avg_bps: number;
    up_max_bps: number;
    latency_avg_ms: number;
  } | null;
  internet_uptime_pct: number;
  internet_outage_count: number;
  internet_outage_total_ms: number;
}

export interface AppSettings {
  speedtest_engine: string;
  speedtest_interval_minutes: number;
  speedtest_enabled: boolean;
  ookla_accept_eula: boolean;
  retention_raw_hours: number;
  retention_rollup_1m_days: number;
  config_lock: boolean;
}

async function req<T>(url: string, init?: RequestInit): Promise<T> {
  const resp = await fetch(url, init);
  if (!resp.ok) {
    let msg = `${resp.status}`;
    try {
      const body = await resp.json();
      if (body.error) msg = body.error;
    } catch {
      /* keep status code */
    }
    throw new Error(msg);
  }
  if (resp.status === 204) return undefined as T;
  return resp.json();
}

export const api = {
  status: () => req<Status>("/api/status"),
  targets: () => req<Target[]>("/api/targets"),
  createTarget: (t: Partial<Target>) =>
    req<Target>("/api/targets", { method: "POST", body: JSON.stringify(t) }),
  updateTarget: (id: number, t: Partial<Target>) =>
    req<Target>(`/api/targets/${id}`, { method: "PUT", body: JSON.stringify(t) }),
  deleteTarget: (id: number) => req<void>(`/api/targets/${id}`, { method: "DELETE" }),
  ping: (target: number, from: number, to: number, resolution?: string) =>
    req<PingSeries>(
      `/api/ping?target=${target}&from=${from}&to=${to}` +
        (resolution ? `&resolution=${resolution}` : ""),
    ),
  speedtests: (from: number, to: number) =>
    req<SpeedTest[]>(`/api/speedtests?from=${from}&to=${to}`),
  runSpeedtest: () => req<{ status: string }>("/api/speedtest/run", { method: "POST" }),
  outages: (from: number, to: number, target?: number) =>
    req<OutageEvent[]>(
      `/api/outages?from=${from}&to=${to}` + (target ? `&target=${target}` : ""),
    ),
  summary: (from: number, to: number) => req<Summary>(`/api/summary?from=${from}&to=${to}`),
  settings: () => req<AppSettings>("/api/settings"),
  saveSettings: (s: AppSettings) =>
    req<AppSettings>("/api/settings", { method: "PUT", body: JSON.stringify(s) }),
};

export type StreamHandlers = {
  onPing?: (samples: Sample[]) => void;
  onStatus?: (ev: Record<string, unknown>) => void;
  onSpeedtest?: (result: SpeedTest) => void;
  onConnect?: () => void;
  onDisconnect?: () => void;
};

// Stream wraps EventSource with connection-state callbacks. EventSource
// reconnects automatically; we surface state so the kiosk can show the
// MONITOR OFFLINE panel.
export class Stream {
  private es: EventSource | null = null;
  constructor(private handlers: StreamHandlers) {}

  start() {
    this.es = new EventSource("/api/stream");
    this.es.onopen = () => this.handlers.onConnect?.();
    this.es.onerror = () => this.handlers.onDisconnect?.();
    this.es.addEventListener("ping", (e) =>
      this.handlers.onPing?.(JSON.parse((e as MessageEvent).data)),
    );
    this.es.addEventListener("status", (e) =>
      this.handlers.onStatus?.(JSON.parse((e as MessageEvent).data)),
    );
    this.es.addEventListener("speedtest", (e) =>
      this.handlers.onSpeedtest?.(JSON.parse((e as MessageEvent).data)),
    );
  }

  stop() {
    this.es?.close();
    this.es = null;
  }
}
