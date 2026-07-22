// uPlot chart wrappers: live sparkline, latency history, speed history.

import uPlot from "uplot";
import "uplot/dist/uPlot.min.css";
import type { CallprobeBucket, PingSeries, Reflector, Sample, SpeedTest, Target } from "./api";

export const SERIES_COLORS = [
  "#58a6ff",
  "#3fb950",
  "#d29922",
  "#bc8cff",
  "#f778ba",
  "#39c5cf",
  "#ffa657",
  "#7ee787",
];

export function colorFor(i: number): string {
  return SERIES_COLORS[i % SERIES_COLORS.length];
}

function axisStyle(): Partial<uPlot.Axis> {
  const styles = getComputedStyle(document.documentElement);
  return {
    stroke: styles.getPropertyValue("--fg-dim").trim() || "#8b949e",
    grid: { stroke: styles.getPropertyValue("--border").trim() + "55", width: 1 },
    ticks: { show: false },
  };
}

// --- live sparkline: last 5 min of raw samples, one line per target ---

export class LiveSparkline {
  private plot: uPlot | null = null;
  private buf = new Map<number, { ts: number[]; rtt: (number | null)[] }>();
  private targetOrder: number[] = [];
  private hidden = new Set<number>();
  private windowMs: number;

  constructor(
    private el: HTMLElement,
    private height = 120,
    windowMinutes = 5,
  ) {
    this.windowMs = windowMinutes * 60_000;
  }

  setTargets(targets: Target[]) {
    const ids = targets.map((t) => t.id);
    const changed =
      ids.length !== this.targetOrder.length || ids.some((id, i) => this.targetOrder[i] !== id);
    if (!changed) return;
    this.targetOrder = ids;
    for (const id of ids) {
      if (!this.buf.has(id)) this.buf.set(id, { ts: [], rtt: [] });
    }
    for (const id of [...this.buf.keys()]) {
      if (!ids.includes(id)) this.buf.delete(id);
    }
    this.rebuild(targets);
  }

  toggle(id: number, visible: boolean) {
    if (visible) this.hidden.delete(id);
    else this.hidden.add(id);
    if (this.plot) {
      const idx = this.targetOrder.indexOf(id);
      if (idx >= 0) this.plot.setSeries(idx + 1, { show: visible });
    }
  }

  push(samples: Sample[]) {
    const cutoff = Date.now() - this.windowMs;
    for (const s of samples) {
      const b = this.buf.get(s.target_id);
      if (!b) continue;
      b.ts.push(s.ts);
      b.rtt.push(s.success ? s.rtt_us / 1000 : null);
    }
    for (const b of this.buf.values()) {
      let drop = 0;
      while (drop < b.ts.length && b.ts[drop] < cutoff) drop++;
      if (drop) {
        b.ts.splice(0, drop);
        b.rtt.splice(0, drop);
      }
    }
    this.redraw();
  }

  private rebuild(targets: Target[]) {
    this.plot?.destroy();
    const series: uPlot.Series[] = [{}];
    targets.forEach((t, i) => {
      series.push({
        label: t.name,
        stroke: colorFor(i),
        width: 1.5,
        spanGaps: false,
        show: !this.hidden.has(t.id),
      });
    });
    this.plot = new uPlot(
      {
        width: this.el.clientWidth || 600,
        height: this.height,
        series,
        legend: { show: false },
        cursor: { show: false },
        scales: { x: { time: true } },
        axes: [
          { ...axisStyle(), space: 80 },
          { ...axisStyle(), size: 46, values: (_u, vals) => vals.map((v) => v + "ms") },
        ],
      },
      this.alignedData(),
      this.el,
    );
    new ResizeObserver(() => {
      this.plot?.setSize({ width: this.el.clientWidth, height: this.height });
    }).observe(this.el);
  }

  private alignedData(): uPlot.AlignedData {
    // merge all target timestamps into one x axis (they tick together, so
    // dedup by second)
    const xs = new Set<number>();
    for (const b of this.buf.values()) for (const t of b.ts) xs.add(Math.floor(t / 1000));
    const xArr = [...xs].sort((a, b) => a - b);
    const xIdx = new Map(xArr.map((x, i) => [x, i]));
    const data: uPlot.AlignedData = [xArr];
    for (const id of this.targetOrder) {
      const col: (number | null)[] = new Array(xArr.length).fill(null);
      const b = this.buf.get(id);
      if (b) {
        for (let i = 0; i < b.ts.length; i++) {
          const xi = xIdx.get(Math.floor(b.ts[i] / 1000));
          if (xi !== undefined) col[xi] = b.rtt[i];
        }
      }
      data.push(col);
    }
    return data;
  }

  private redraw() {
    if (!this.plot) return;
    this.plot.setData(this.alignedData());
  }
}

// --- latency history chart (rollup or raw series per target) ---

export function renderLatencyChart(
  el: HTMLElement,
  seriesByTarget: { target: Target; data: PingSeries }[],
  height = 260,
): uPlot {
  el.textContent = "";
  // union of all timestamps
  const xs = new Set<number>();
  for (const s of seriesByTarget) for (const p of s.data.points) xs.add(Math.floor(p.ts / 1000));
  const xArr = [...xs].sort((a, b) => a - b);
  const xIdx = new Map(xArr.map((x, i) => [x, i]));

  const data: uPlot.AlignedData = [xArr];
  const series: uPlot.Series[] = [{}];
  seriesByTarget.forEach((s, i) => {
    const rtt: (number | null)[] = new Array(xArr.length).fill(null);
    const loss: (number | null)[] = new Array(xArr.length).fill(null);
    for (const p of s.data.points) {
      const xi = xIdx.get(Math.floor(p.ts / 1000))!;
      rtt[xi] = p.rtt_avg_us != null ? p.rtt_avg_us / 1000 : null;
      loss[xi] = p.loss_pct > 0 ? p.loss_pct : null;
    }
    data.push(rtt);
    series.push({
      label: s.target.name,
      stroke: colorFor(i),
      width: 1.5,
      spanGaps: false,
      value: (_u, v) => (v == null ? "—" : v.toFixed(1) + "ms"),
    });
    data.push(loss);
    series.push({
      label: s.target.name + " loss",
      scale: "%",
      stroke: colorFor(i) + "88",
      paths: uPlot.paths?.bars ? uPlot.paths.bars({ size: [0.6, 4] }) : undefined,
      points: { show: false },
      value: (_u, v) => (v == null ? "" : v.toFixed(1) + "%"),
    });
  });

  return new uPlot(
    {
      width: el.clientWidth || 800,
      height,
      series,
      scales: { x: { time: true }, "%": { range: [0, 100] } },
      axes: [
        { ...axisStyle(), space: 80 },
        { ...axisStyle(), size: 52, values: (_u, vals) => vals.map((v) => v + "ms") },
        {
          ...axisStyle(),
          scale: "%",
          side: 1,
          size: 44,
          values: (_u, vals) => vals.map((v) => v + "%"),
        },
      ],
    },
    data,
    el,
  );
}

// --- packet loss history chart (loss % per target over time) ---

export function renderLossChart(
  el: HTMLElement,
  seriesByTarget: { target: Target; data: PingSeries }[],
  height = 180,
): uPlot {
  el.textContent = "";
  const xs = new Set<number>();
  for (const s of seriesByTarget) for (const p of s.data.points) xs.add(Math.floor(p.ts / 1000));
  const xArr = [...xs].sort((a, b) => a - b);
  const xIdx = new Map(xArr.map((x, i) => [x, i]));

  const data: uPlot.AlignedData = [xArr];
  const series: uPlot.Series[] = [{}];
  seriesByTarget.forEach((s, i) => {
    const loss: (number | null)[] = new Array(xArr.length).fill(null);
    for (const p of s.data.points) {
      const xi = xIdx.get(Math.floor(p.ts / 1000))!;
      loss[xi] = p.loss_pct;
    }
    data.push(loss);
    series.push({
      label: s.target.name,
      stroke: colorFor(i),
      width: 1.5,
      spanGaps: true,
      value: (_u, v) => (v == null ? "—" : v.toFixed(1) + "%"),
    });
  });

  return new uPlot(
    {
      width: el.clientWidth || 800,
      height,
      series,
      scales: {
        x: { time: true },
        // keep 0 pinned and give small loss values room; scale up when
        // real loss exceeds the floor
        y: { range: (_u, _min, max) => [0, Math.max(10, Math.ceil(max || 0))] },
      },
      axes: [
        { ...axisStyle(), space: 80 },
        { ...axisStyle(), size: 46, values: (_u, vals) => vals.map((v) => v + "%") },
      ],
    },
    data,
    el,
  );
}

// --- speed test history chart ---

export function renderSpeedChart(el: HTMLElement, tests: SpeedTest[], height = 220): uPlot {
  el.textContent = "";
  const ok = tests.filter((t) => !t.error);
  const xArr = ok.map((t) => Math.floor(t.ran_at / 1000));
  const down = ok.map((t) => t.download_bps / 1e6);
  const up = ok.map((t) => t.upload_bps / 1e6);

  return new uPlot(
    {
      width: el.clientWidth || 800,
      height,
      series: [
        {},
        {
          label: "Down",
          stroke: "#58a6ff",
          width: 2,
          points: { show: true, size: 5 },
          value: (_u, v) => (v == null ? "—" : v.toFixed(1) + " Mbps"),
        },
        {
          label: "Up",
          stroke: "#bc8cff",
          width: 2,
          points: { show: true, size: 5 },
          value: (_u, v) => (v == null ? "—" : v.toFixed(1) + " Mbps"),
        },
      ],
      scales: {
        x: {
          time: true,
          // a single test in range degenerates the auto-range; pin an
          // explicit ±30 min window around it
          ...(xArr.length === 1 ? { auto: false, range: [xArr[0] - 1800, xArr[0] + 1800] as [number, number] } : {}),
        },
      },
      axes: [
        { ...axisStyle(), space: 80 },
        { ...axisStyle(), size: 60, values: (_u, vals) => vals.map((v) => v + "M") },
      ],
    },
    [xArr, down, up],
    el,
  );
}

// --- call quality (MOS) history chart ---

export function renderMOSChart(
  el: HTMLElement,
  buckets: CallprobeBucket[],
  reflectors: Reflector[],
  height = 200,
): uPlot {
  el.textContent = "";
  const ids = [...new Set(buckets.map((b) => b.reflector_id))].sort((a, b) => a - b);
  const names = new Map(reflectors.map((r) => [r.id, r.name]));

  const xsSet = new Set<number>();
  for (const b of buckets) xsSet.add(Math.floor(b.ts / 1000));
  const xs = [...xsSet].sort((a, b) => a - b);
  const xIdx = new Map(xs.map((x, i) => [x, i]));

  const data: uPlot.AlignedData = [xs];
  const series: uPlot.Series[] = [{}];
  ids.forEach((id, i) => {
    const ys: (number | null)[] = new Array(xs.length).fill(null);
    for (const b of buckets) {
      if (b.reflector_id === id) ys[xIdx.get(Math.floor(b.ts / 1000))!] = b.mos_x100 / 100;
    }
    data.push(ys);
    series.push({
      label: names.get(id) ?? `reflector ${id}`,
      stroke: colorFor(i),
      width: 2,
      spanGaps: true,
      value: (_u, v) => (v == null ? "—" : "MOS " + v.toFixed(2)),
    });
  });

  return new uPlot(
    {
      width: el.clientWidth || 800,
      height,
      series,
      scales: { x: { time: true }, y: { auto: false, range: [1, 5] } },
      axes: [
        { ...axisStyle(), space: 80 },
        { ...axisStyle(), size: 40 },
      ],
      hooks: {
        drawClear: [
          (u) => {
            // shade the "unusable for calls" band (MOS < 3)
            const ctx = u.ctx;
            const y3 = u.valToPos(3, "y", true);
            const bottom = u.valToPos(1, "y", true);
            ctx.save();
            ctx.fillStyle = "rgba(248, 81, 73, 0.07)";
            ctx.fillRect(u.bbox.left, y3, u.bbox.width, bottom - y3);
            ctx.restore();
          },
        ],
      },
    },
    data,
    el,
  );
}
