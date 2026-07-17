// Global time range store, New Relic style: one picker in the top bar
// drives every history panel. Relative ranges resolve from/to at read
// time so refreshes slide the window forward; custom ranges are fixed.

export type TimeRange =
  | { kind: "relative"; ms: number }
  | { kind: "custom"; from: number; to: number };

export const PRESETS: { label: string; ms: number }[] = [
  { label: "30 minutes", ms: 30 * 60_000 },
  { label: "60 minutes", ms: 3_600_000 },
  { label: "3 hours", ms: 3 * 3_600_000 },
  { label: "6 hours", ms: 6 * 3_600_000 },
  { label: "12 hours", ms: 12 * 3_600_000 },
  { label: "24 hours", ms: 24 * 3_600_000 },
  { label: "3 days", ms: 3 * 86_400_000 },
  { label: "7 days", ms: 7 * 86_400_000 },
  { label: "30 days", ms: 30 * 86_400_000 },
];

type Listener = () => void;

class TimeRangeStore {
  private cur: TimeRange = { kind: "relative", ms: 3_600_000 };
  private listeners = new Set<Listener>();

  get(): { from: number; to: number; label: string; isRelative: boolean } {
    if (this.cur.kind === "relative") {
      const to = Date.now();
      const preset = PRESETS.find((p) => p.ms === (this.cur as { ms: number }).ms);
      return {
        from: to - this.cur.ms,
        to,
        label: `Since ${preset?.label ?? this.fmtSpan(this.cur.ms)} ago`,
        isRelative: true,
      };
    }
    return {
      from: this.cur.from,
      to: this.cur.to,
      label: `${this.fmtStamp(this.cur.from)} → ${this.fmtStamp(this.cur.to)}`,
      isRelative: false,
    };
  }

  raw(): TimeRange {
    return this.cur;
  }

  set(r: TimeRange) {
    this.cur = r;
    for (const l of this.listeners) l();
  }

  subscribe(l: Listener): () => void {
    this.listeners.add(l);
    return () => this.listeners.delete(l);
  }

  private fmtSpan(ms: number): string {
    const h = ms / 3_600_000;
    if (h < 1) return `${Math.round(ms / 60_000)} minutes`;
    if (h < 48) return `${Math.round(h)} hours`;
    return `${Math.round(h / 24)} days`;
  }

  private fmtStamp(ts: number): string {
    return new Date(ts).toLocaleString(undefined, {
      month: "short",
      day: "numeric",
      hour: "2-digit",
      minute: "2-digit",
    });
  }
}

export const timeRange = new TimeRangeStore();
