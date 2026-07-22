// Dashboard: live "right now" section (path viz, sparkline, loss, last
// speed test) + history section (charts, outage log, summary cards).

import {
  api,
  Stream,
  type CallprobeSnapshot,
  type OutageEvent,
  type Reflector,
  type Sample,
  type Status,
  type Summary,
  type Target,
} from "./api";
import { LiveSparkline, colorFor, renderLatencyChart, renderLossChart, renderMOSChart, renderSpeedChart } from "./charts";
import { mountLossInspector } from "./lossinspector";
import { localizeFault, renderPathViz } from "./pathviz";
import { timeRange } from "./timerange";
import { mountTimePicker } from "./timepicker";
import {
  clear,
  fmtAgo,
  fmtBps,
  fmtDuration,
  fmtPct,
  fmtRTT,
  fmtTime,
  h,
} from "./util";

export function mountDashboard(app: HTMLElement): () => void {
  let status: Status | null = null;
  let outageSort: { key: keyof OutageEvent | "duration"; dir: number } = {
    key: "started_at",
    dir: -1,
  };
  let outageFilter = 0; // target id, 0 = all
  let outages: OutageEvent[] = [];
  let historyEpoch = 0;

  // --- static skeleton ---
  const connDot = h("span", { class: "conn-dot", title: "SSE connection" });
  const banner = h("div", { class: "outage-banner", style: "display:none" });
  const faultLine = h("div", { class: "muted", style: "margin-top:8px" });
  const pathBox = h("div", { class: "pathviz" });
  const sparkBox = h("div");
  const sparkToggles = h("div", { style: "display:flex;gap:10px;flex-wrap:wrap;margin-top:6px" });
  const lossGrid = h("div", { class: "stat-grid" });
  const lossChartBox = h("div", { class: "chart-wrap" });
  const lossInspectorBox = h("div");
  const speedBox = h("div");
  const latencyChartBox = h("div", { class: "chart-wrap" });
  const speedChartBox = h("div", { class: "chart-wrap" });
  const outageBox = h("div");
  const summaryGrid = h("div", { class: "stat-grid" });
  const historyLabel = h("div", { class: "muted", style: "margin-bottom:10px" });
  const pickerSlot = h("span", { class: "picker-slot" });
  const pickerRow = h("div", { class: "picker-row" }, pickerSlot);
  const callGrid = h("div", { class: "stat-grid" });
  const callPanel = h("div", { class: "panel", style: "display:none" },
    h("h2", {}, "Call quality — synthetic call probe"), callGrid);
  const mosChartBox = h("div", { class: "chart-wrap" });
  const mosPanel = h("div", { class: "panel", style: "display:none" },
    h("h2", {}, "Call quality over time (MOS)"), mosChartBox);
  const freezeBox = h("div");
  const freezePanel = h("div", { class: "panel", style: "display:none" },
    h("h2", {}, "Call freezes"), freezeBox);
  let reflectors: Reflector[] = [];
  let lastSnapshots: CallprobeSnapshot[] = [];

  app.append(
    banner,
    h(
      "div",
      { class: "panel" },
      h("h2", {}, "Network path"),
      pathBox,
      faultLine,
    ),
    h(
      "div",
      { class: "row" },
      h(
        "div",
        { class: "panel", style: "flex:2;min-width:380px" },
        h("h2", {}, "Live latency — last 5 min"),
        sparkBox,
        sparkToggles,
      ),
      h(
        "div",
        { class: "panel", style: "flex:1" },
        h("h2", {}, "Last speed test"),
        speedBox,
      ),
    ),
    h("div", { class: "panel" }, h("h2", {}, "Packet loss — rolling 5 min"), lossGrid),
    callPanel,
    pickerRow,
    h("div", { class: "panel" }, h("h2", {}, "Packet loss over time"), lossChartBox),
    mosPanel,
    freezePanel,
    h("div", { class: "panel" }, h("h2", {}, "Loss inspector — raw drops"), lossInspectorBox),
    h("div", { class: "panel" }, h("h2", {}, "History"), historyLabel, summaryGrid),
    h("div", { class: "panel" }, h("h2", {}, "Latency"), latencyChartBox),
    h("div", { class: "panel" }, h("h2", {}, "Speed tests"), speedChartBox),
    h("div", { class: "panel" }, h("h2", {}, "Outage log"), outageBox),
  );
  document.querySelector(".topbar .conn-slot")?.append(connDot);

  const spark = new LiveSparkline(sparkBox, 140);

  // --- live section renderers ---

  function renderBanner() {
    if (!status) return;
    if (status.internet.state === "down") {
      banner.style.display = "flex";
      const since = status.internet.outage_since ?? Date.now();
      clear(banner).append(
        h("span", {}, "⚠ INTERNET OUTAGE"),
        h("span", { class: "num" }, `down for ${fmtDuration(Date.now() - since)}`),
      );
    } else {
      banner.style.display = "none";
    }
  }

  function renderLive() {
    if (!status) return;
    renderBanner();
    renderPathViz(pathBox, status.targets, status.speedtest_running);
    let fault = localizeFault(status.targets, status.speedtest_running);
    if (!fault && status.speedtest_running) {
      fault = "Speed test in progress — latency reflects a deliberately saturated line.";
    }
    faultLine.textContent = fault ?? "";
    faultLine.style.display = fault ? "block" : "none";

    clear(lossGrid);
    for (const t of status.targets.filter((x) => x.enabled)) {
      const cls =
        t.loss_pct < 2 ? "loss-ok" : t.loss_pct <= 10 ? "loss-warn" : "loss-bad";
      lossGrid.append(
        h(
          "div",
          { class: "stat" },
          h("div", { class: "label" }, t.name),
          h("div", { class: `value num ${cls}` }, t.loss_pct.toFixed(1) + "%"),
        ),
      );
    }
  }

  function renderCallprobe(snaps: CallprobeSnapshot[]) {
    lastSnapshots = snaps;
    if (snaps.length === 0) return;
    callPanel.style.display = "block";
    clear(callGrid);
    for (const sn of snaps) {
      const cls = !sn.alive || sn.mos < 3 ? "loss-bad" : sn.mos < 4 ? "loss-warn" : "loss-ok";
      callGrid.append(
        h(
          "div",
          { class: "stat" },
          h("div", { class: "label" }, `${sn.name} · MOS`),
          h(
            "div",
            { class: `value num ${cls}` },
            sn.alive ? sn.mos.toFixed(2) : "—",
            sn.in_freeze ? h("small", { class: "loss-bad" }, "  ● FROZEN") : null,
            !sn.alive ? h("small", { class: "loss-bad" }, " no replies") : null,
          ),
          h(
            "div",
            { class: "muted num", style: "font-size:11px;margin-top:2px" },
            `${sn.rtt_ms.toFixed(1)}ms rtt · ${sn.jitter_ms.toFixed(1)}ms jitter · ${sn.loss_pct.toFixed(1)}% loss`,
          ),
        ),
      );
    }
  }

  function renderSpeed() {
    if (!status) return;
    clear(speedBox);
    const last = status.last_speedtest;
    const btn = h(
      "button",
      {
        class: "primary",
        onClick: async () => {
          try {
            await api.runSpeedtest();
            btn.textContent = "Running…";
            (btn as HTMLButtonElement).disabled = true;
          } catch (e) {
            btn.textContent = String(e);
          }
        },
      },
      status.speedtest_running ? "Running…" : "Run now",
    ) as HTMLButtonElement;
    btn.disabled = status.speedtest_running;

    if (last && !last.error) {
      speedBox.append(
        h(
          "div",
          { class: "stat-grid", style: "margin-bottom:10px" },
          h(
            "div",
            { class: "stat" },
            h("div", { class: "label" }, "Down"),
            h("div", { class: "value num" }, fmtBps(last.download_bps)),
          ),
          h(
            "div",
            { class: "stat" },
            h("div", { class: "label" }, "Up"),
            h("div", { class: "value num" }, fmtBps(last.upload_bps)),
          ),
          h(
            "div",
            { class: "stat" },
            h("div", { class: "label" }, "Latency"),
            h(
              "div",
              { class: "value num" },
              last.latency_ms.toFixed(1),
              h("small", {}, " ms idle"),
            ),
          ),
          last.loaded_latency_ms > 0
            ? h(
                "div",
                { class: "stat" },
                h("div", { class: "label" }, "Loaded latency"),
                h(
                  "div",
                  { class: "value num" },
                  last.loaded_latency_ms.toFixed(1),
                  h("small", {}, " ms"),
                ),
              )
            : null,
        ),
        h(
          "div",
          { class: "muted", style: "margin-bottom:10px" },
          `${fmtAgo(last.ran_at)} · ${last.engine}` +
            (last.server_name ? ` · ${last.server_name}` : ""),
        ),
        btn,
      );
    } else {
      speedBox.append(
        h(
          "div",
          { class: "muted", style: "margin-bottom:10px" },
          last?.error
            ? `Last run failed (${last.error === "skipped_outage" ? "skipped: outage" : last.error}) — ${fmtAgo(last.ran_at)}`
            : "No speed test results yet.",
        ),
        btn,
      );
    }
  }

  // --- history section ---

  async function loadHistory() {
    const epoch = ++historyEpoch;
    const { from, to, label } = timeRange.get();
    historyLabel.textContent = label;
    const targets = (status?.targets ?? (await api.targets())) as Target[];
    const enabled = targets.filter((t) => t.enabled);

    const [series, tests, outs, summary, probeBuckets, freezes] = await Promise.all([
      Promise.all(
        enabled.map(async (t) => ({ target: t, data: await api.ping(t.id, from, to) })),
      ),
      api.speedtests(from, to),
      api.outages(from, to),
      api.summary(from, to),
      api.callprobeHistory(from, to).catch(() => []),
      api.freezes(from, to).catch(() => null),
    ]);
    if (epoch !== historyEpoch) return; // stale response, a newer load won

    if (probeBuckets.length > 0) {
      if (reflectors.length === 0) reflectors = await api.reflectors().catch(() => []);
      mosPanel.style.display = "block";
      renderMOSChart(mosChartBox, probeBuckets, reflectors);
    } else {
      mosPanel.style.display = "none";
    }
    if (freezes && (freezes.count > 0 || probeBuckets.length > 0)) {
      if (reflectors.length === 0) reflectors = await api.reflectors().catch(() => []);
      freezePanel.style.display = "block";
      renderFreezes(freezes.events, freezes.count, freezes.count_visible);
    } else {
      freezePanel.style.display = "none";
    }

    renderLatencyChart(latencyChartBox, series);
    renderLossChart(lossChartBox, series);
    if (tests.filter((t) => !t.error).length > 0) {
      renderSpeedChart(speedChartBox, tests);
    } else {
      clear(speedChartBox).append(h("div", { class: "muted" }, "No speed tests in range."));
    }
    outages = outs;
    renderOutages(targets);
    renderSummary(summary);
  }

  function renderFreezes(events: { reflector_id: number; started_at: number; duration_ms: number; packets_lost: number }[], count: number, visible: number) {
    const names = new Map(reflectors.map((r) => [r.id, r.name]));
    clear(freezeBox);
    freezeBox.append(
      h(
        "div",
        { class: "muted", style: "margin-bottom:8px" },
        `${count} freeze${count === 1 ? "" : "s"} in range · `,
        h("span", { class: visible > 0 ? "loss-bad" : "loss-ok" }, `${visible} over 200ms (visible stalls)`),
      ),
    );
    if (events.length === 0) {
      freezeBox.append(h("div", { class: "muted" }, "No freezes recorded. 🎉"));
      return;
    }
    const table = h(
      "table",
      {},
      h(
        "tr",
        {},
        h("th", {}, "When"),
        h("th", {}, "Reflector"),
        h("th", { class: "num" }, "Duration"),
        h("th", { class: "num" }, "Packets lost"),
        h("th", {}, ""),
      ),
    );
    for (const f of events.slice(0, 100)) {
      table.append(
        h(
          "tr",
          {},
          h("td", { class: "num" }, new Date(f.started_at).toLocaleString()),
          h("td", {}, names.get(f.reflector_id) ?? String(f.reflector_id)),
          h("td", { class: `num ${f.duration_ms >= 200 ? "loss-bad" : ""}` }, `${f.duration_ms} ms`),
          h("td", { class: "num" }, String(f.packets_lost)),
          h("td", {}, f.duration_ms >= 200 ? h("span", { class: "badge open" }, "visible stall") : null),
        ),
      );
    }
    freezeBox.append(table);
  }

  function renderOutages(targets: Target[]) {
    const names = new Map(targets.map((t) => [t.id, t.name]));
    clear(outageBox);

    const filterSel = h("select", {
      onChange: (e: Event) => {
        outageFilter = Number((e.target as HTMLSelectElement).value);
        renderOutages(targets);
      },
    }) as HTMLSelectElement;
    filterSel.append(h("option", { value: "0" }, "All targets"));
    for (const t of targets) {
      filterSel.append(
        h("option", { value: String(t.id), selected: outageFilter === t.id }, t.name),
      );
    }
    outageBox.append(h("div", { style: "margin-bottom:8px" }, filterSel));

    const rows = outages
      .filter((o) => !outageFilter || o.target_id === outageFilter)
      .sort((a, b) => {
        const va = outageSort.key === "duration" ? (a.duration_ms ?? Infinity) : (a[outageSort.key] ?? 0);
        const vb = outageSort.key === "duration" ? (b.duration_ms ?? Infinity) : (b[outageSort.key] ?? 0);
        return (Number(va) - Number(vb)) * outageSort.dir;
      });

    if (rows.length === 0) {
      outageBox.append(h("div", { class: "muted" }, "No outages in range. 🎉"));
      return;
    }

    const th = (label: string, key: typeof outageSort.key) =>
      h(
        "th",
        {
          onClick: () => {
            outageSort =
              outageSort.key === key
                ? { key, dir: -outageSort.dir }
                : { key, dir: -1 };
            renderOutages(targets);
          },
        },
        label + (outageSort.key === key ? (outageSort.dir < 0 ? " ↓" : " ↑") : ""),
      );

    const table = h(
      "table",
      {},
      h(
        "thead",
        {},
        h(
          "tr",
          {},
          th("Target", "target_id"),
          th("Start", "started_at"),
          th("End", "ended_at"),
          th("Duration", "duration"),
        ),
      ),
    );
    const tbody = h("tbody");
    for (const o of rows) {
      tbody.append(
        h(
          "tr",
          {},
          h("td", {}, names.get(o.target_id) ?? `#${o.target_id}`),
          h("td", { class: "num" }, fmtTime(o.started_at)),
          h(
            "td",
            { class: "num" },
            o.ended_at ? fmtTime(o.ended_at) : h("span", { class: "badge open" }, "ONGOING"),
          ),
          h(
            "td",
            { class: "num" },
            o.ended_at
              ? fmtDuration(o.duration_ms ?? 0)
              : fmtDuration(Date.now() - o.started_at) + "…",
          ),
        ),
      );
    }
    table.append(tbody);
    outageBox.append(table);
  }

  function renderSummary(s: Summary) {
    clear(summaryGrid);
    summaryGrid.append(
      h(
        "div",
        { class: "stat" },
        h("div", { class: "label" }, "Internet uptime"),
        h(
          "div",
          {
            class: `value num ${s.internet_uptime_pct >= 99.9 ? "loss-ok" : s.internet_uptime_pct >= 99 ? "loss-warn" : "loss-bad"}`,
          },
          fmtPct(s.internet_uptime_pct),
        ),
      ),
      h(
        "div",
        { class: "stat" },
        h("div", { class: "label" }, "Internet outages"),
        h(
          "div",
          { class: "value num" },
          String(s.internet_outage_count),
          h("small", {}, s.internet_outage_count ? ` · ${fmtDuration(s.internet_outage_total_ms)}` : ""),
        ),
      ),
    );
    for (const t of s.targets) {
      summaryGrid.append(
        h(
          "div",
          { class: "stat" },
          h("div", { class: "label" }, `${t.name} p95`),
          h(
            "div",
            { class: "value num" },
            fmtRTT(t.rtt_p95_us),
            h("small", {}, " ms · " + fmtPct(t.uptime_pct)),
          ),
        ),
      );
    }
    if (s.speedtest) {
      summaryGrid.append(
        h(
          "div",
          { class: "stat" },
          h("div", { class: "label" }, "Avg down / up"),
          h(
            "div",
            { class: "value num" },
            fmtBps(s.speedtest.down_avg_bps),
            h("small", {}, " / " + fmtBps(s.speedtest.up_avg_bps)),
          ),
        ),
      );
    }
  }

  function renderSparkToggles(targets: Target[]) {
    clear(sparkToggles);
    targets
      .filter((t) => t.enabled)
      .forEach((t, i) => {
        const cb = h("input", { type: "checkbox", checked: true }) as HTMLInputElement;
        cb.addEventListener("change", () => spark.toggle(t.id, cb.checked));
        sparkToggles.append(
          h(
            "label",
            { style: `display:flex;align-items:center;gap:4px;font-size:12px;color:${colorFor(i)}` },
            cb,
            t.name,
          ),
        );
      });
  }

  // --- data flow ---

  // backfill the sparkline from stored samples so it renders full on
  // load instead of slowly populating from the live stream
  let sparkSeeded = false;
  async function seedSparkline(targets: Target[]) {
    if (sparkSeeded) return;
    sparkSeeded = true;
    const to = Date.now();
    const from = to - 5 * 60_000;
    const all: Sample[] = [];
    await Promise.all(
      targets.map(async (t) => {
        const s = await api.ping(t.id, from, to, "raw");
        for (const p of s.points) {
          all.push({
            target_id: t.id,
            ts: p.ts,
            rtt_us: p.rtt_avg_us ?? 0,
            success: p.rtt_avg_us != null,
          });
        }
      }),
    );
    all.sort((a, b) => a.ts - b.ts);
    if (all.length) spark.push(all);
  }

  async function refreshStatus() {
    try {
      const next = await api.status();
      status = next;
      const enabled = next.targets.filter((t) => t.enabled);
      spark.setTargets(enabled);
      seedSparkline(enabled);
      renderSparkToggles(next.targets);
      renderLive();
      renderSpeed();
      renderCallprobe(next.callprobe ?? lastSnapshots);
    } catch {
      /* transient; SSE reconnect + next poll will recover */
    }
  }

  const stream = new Stream({
    onPing: (samples) => spark.push(samples),
    onCallprobe: (snaps) => renderCallprobe(snaps),
    onStatus: () => refreshStatus(),
    onSpeedtest: () => {
      refreshStatus();
      loadHistory();
    },
    onConnect: () => connDot.classList.add("on"),
    onDisconnect: () => connDot.classList.remove("on"),
  });

  const stopInspector = mountLossInspector(lossInspectorBox, () => status?.targets ?? []);
  const unsubRange = timeRange.subscribe(loadHistory);
  const stopPicker = mountTimePicker(pickerSlot);

  refreshStatus().then(loadHistory);
  stream.start();
  const statusTimer = setInterval(refreshStatus, 5000);
  const bannerTimer = setInterval(renderBanner, 1000);
  // relative ranges slide forward: refresh history periodically
  const historyTimer = setInterval(() => {
    if (timeRange.get().isRelative) loadHistory();
  }, 60_000);

  return () => {
    stream.stop();
    stopInspector();
    stopPicker();
    unsubRange();
    clearInterval(statusTimer);
    clearInterval(bannerTimer);
    clearInterval(historyTimer);
  };
}
