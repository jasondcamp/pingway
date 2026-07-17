// Loss inspector: raw-resolution view of packet loss for one target.
// Every burst of consecutive dropped pings renders as a red tick on a
// timeline strip (periodic loss shows up as evenly spaced ticks), with a
// table of bursts and the gap since the previous one.

import type { Target } from "./api";
import { timeRange } from "./timerange";
import { clear, fmtDuration, fmtTime, h } from "./util";

interface Burst {
  started_at: number;
  ended_at: number;
  lost: number;
  gap_ms?: number;
}

interface BurstResponse {
  target_id: number;
  from: number;
  to: number;
  sent: number;
  lost: number;
  bursts: Burst[];
  median_gap_ms: number;
}

// raw ping samples are retained ~48h; the inspector clamps the global
// range to that window
const RAW_RETENTION_MS = 48 * 3_600_000;

export function mountLossInspector(root: HTMLElement, getTargets: () => Target[]): () => void {
  let targetID = 0;
  let epoch = 0;

  const controls = h("div", { class: "range-picker" });
  const clampNote = h("div", { class: "muted", style: "display:none;margin-bottom:6px" });
  const summary = h("div", { class: "muted", style: "margin-bottom:8px" });
  const strip = h("div", { class: "loss-strip" });
  const stripLabels = h("div", { class: "loss-strip-labels" });
  const table = h("div", { style: "margin-top:12px;max-height:260px;overflow-y:auto" });
  root.append(controls, clampNote, summary, strip, stripLabels, table);

  async function load() {
    const my = ++epoch;
    const targets = getTargets().filter((t) => t.enabled);
    if (targets.length === 0) {
      // mounted before the first status fetch; retry until targets exist
      setTimeout(() => {
        if (my === epoch) load();
      }, 1000);
      return;
    }
    if (!targets.some((t) => t.id === targetID)) {
      targetID = (targets.find((t) => t.tier === 3) ?? targets[0]).id;
    }
    renderControls(targets);

    const range = timeRange.get();
    const to = range.to;
    let from = range.from;
    const oldestRaw = Date.now() - RAW_RETENTION_MS;
    if (from < oldestRaw) {
      from = oldestRaw;
      clampNote.textContent =
        "Raw samples are retained 48h — showing the most recent 48h of the selected range.";
      clampNote.style.display = "block";
    } else {
      clampNote.style.display = "none";
    }
    if (to <= from) return;
    const resp = await fetch(`/api/lossbursts?target=${targetID}&from=${from}&to=${to}`);
    if (!resp.ok || my !== epoch) return;
    const data: BurstResponse = await resp.json();
    if (my !== epoch) return;
    render(data);
  }

  function renderControls(targets: Target[]) {
    clear(controls);
    const sel = h("select", {
      onChange: (e: Event) => {
        targetID = Number((e.target as HTMLSelectElement).value);
        load();
      },
    }) as HTMLSelectElement;
    for (const t of targets) {
      sel.append(h("option", { value: String(t.id), selected: t.id === targetID }, t.name));
    }
    controls.append(sel);
  }

  function render(data: BurstResponse) {
    const span = data.to - data.from;
    const lossPct = data.sent > 0 ? (100 * data.lost) / data.sent : 0;

    clear(summary);
    if (data.bursts.length === 0) {
      summary.textContent = `No packet loss in this range (${data.sent.toLocaleString()} pings). 🎉`;
    } else {
      const parts = [
        `${data.bursts.length} burst${data.bursts.length === 1 ? "" : "s"}`,
        `${data.lost} lost of ${data.sent.toLocaleString()} pings (${lossPct.toFixed(2)}%)`,
      ];
      if (data.median_gap_ms > 0) {
        parts.push(`median gap between bursts: ${fmtDuration(data.median_gap_ms)}`);
      }
      summary.textContent = parts.join(" · ");
    }

    // timeline strip: one red tick per burst, positioned by time
    clear(strip);
    for (const b of data.bursts) {
      const left = (100 * (b.started_at - data.from)) / span;
      const width = Math.max(0.25, (100 * (b.ended_at - b.started_at + 1000)) / span);
      const durS = Math.round((b.ended_at - b.started_at) / 1000) + 1;
      strip.append(
        h("div", {
          class: "loss-tick",
          style: `left:${left}%;width:${width}%`,
          title: `${fmtTime(b.started_at)} — ${b.lost} lost over ~${durS}s`,
        }),
      );
    }
    clear(stripLabels);
    stripLabels.append(
      h("span", {}, fmtTime(data.from)),
      h("span", {}, fmtTime(data.from + span / 2)),
      h("span", {}, fmtTime(data.to)),
    );

    // burst table, newest first
    clear(table);
    if (data.bursts.length === 0) return;
    const tbl = h(
      "table",
      {},
      h(
        "thead",
        {},
        h(
          "tr",
          {},
          h("th", {}, "Start"),
          h("th", { class: "num" }, "Lost"),
          h("th", { class: "num" }, "Duration"),
          h("th", { class: "num" }, "Gap since previous"),
        ),
      ),
    );
    const tbody = h("tbody");
    for (const b of [...data.bursts].reverse()) {
      tbody.append(
        h(
          "tr",
          {},
          h("td", { class: "num" }, fmtTime(b.started_at)),
          h("td", { class: "num" }, String(b.lost)),
          h("td", { class: "num" }, `~${Math.round((b.ended_at - b.started_at) / 1000) + 1}s`),
          h("td", { class: "num" }, b.gap_ms != null ? fmtDuration(b.gap_ms) : "—"),
        ),
      );
    }
    tbl.append(tbody);
    table.append(tbl);
  }

  load();
  const timer = setInterval(load, 15_000);
  const unsub = timeRange.subscribe(load);
  return () => {
    clearInterval(timer);
    unsub();
  };
}
