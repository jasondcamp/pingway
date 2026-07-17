// Small DOM + formatting helpers (no framework).

export type Child = Node | string | null | undefined;

export function h<K extends keyof HTMLElementTagNameMap>(
  tag: K,
  attrs?: Record<string, string | number | boolean | EventListener | undefined>,
  ...children: Child[]
): HTMLElementTagNameMap[K] {
  const el = document.createElement(tag);
  if (attrs) {
    for (const [k, v] of Object.entries(attrs)) {
      if (v === undefined || v === false) continue;
      if (k.startsWith("on") && typeof v === "function") {
        el.addEventListener(k.slice(2).toLowerCase(), v as EventListener);
      } else if (v === true) {
        el.setAttribute(k, "");
      } else {
        el.setAttribute(k, String(v));
      }
    }
  }
  for (const c of children) {
    if (c == null) continue;
    el.append(typeof c === "string" ? document.createTextNode(c) : c);
  }
  return el;
}

export function clear(el: HTMLElement): HTMLElement {
  el.textContent = "";
  return el;
}

// --- formatting (all values arrive as µs / bps / unix ms) ---

export function fmtRTT(us: number): string {
  if (us < 0) return "—";
  const ms = us / 1000;
  if (ms < 10) return ms.toFixed(2);
  if (ms < 100) return ms.toFixed(1);
  return Math.round(ms).toString();
}

export function fmtBps(bps: number): string {
  if (bps <= 0) return "—";
  if (bps >= 1e9) return (bps / 1e9).toFixed(2) + " Gbps";
  if (bps >= 1e6) return (bps / 1e6).toFixed(1) + " Mbps";
  if (bps >= 1e3) return (bps / 1e3).toFixed(0) + " Kbps";
  return bps.toFixed(0) + " bps";
}

export function fmtDuration(ms: number): string {
  if (ms < 0) return "—";
  const s = Math.floor(ms / 1000);
  if (s < 60) return `${s}s`;
  const m = Math.floor(s / 60);
  if (m < 60) return `${m}m ${s % 60}s`;
  const hs = Math.floor(m / 60);
  if (hs < 48) return `${hs}h ${m % 60}m`;
  return `${Math.floor(hs / 24)}d ${hs % 24}h`;
}

export function fmtAgo(ts: number): string {
  return fmtDuration(Date.now() - ts) + " ago";
}

export function fmtTime(ts: number): string {
  return new Date(ts).toLocaleString(undefined, {
    month: "short",
    day: "numeric",
    hour: "2-digit",
    minute: "2-digit",
    second: "2-digit",
  });
}

export function fmtPct(p: number): string {
  if (p >= 99.995) return "100%";
  return p.toFixed(2) + "%";
}

// Status color class per spec: green <2% loss, yellow 2-10% loss or RTT
// >3x 24h baseline, red DOWN.
export function healthClass(t: {
  state: string;
  loss_60s_pct: number;
  last_rtt_us: number;
  baseline_rtt_us: number;
}): "ok" | "warn" | "down" {
  if (t.state === "down") return "down";
  if (t.loss_60s_pct >= 2 && t.loss_60s_pct <= 10) return "warn";
  if (t.loss_60s_pct > 10) return "warn";
  if (t.baseline_rtt_us > 0 && t.last_rtt_us > 3 * t.baseline_rtt_us) return "warn";
  return "ok";
}
