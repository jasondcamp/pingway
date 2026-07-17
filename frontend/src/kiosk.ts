// Kiosk mode: dark, chromeless, readable at 3 meters. Auto-reconnecting
// SSE; if the backend is unreachable the MONITOR OFFLINE panel takes over
// so a dead monitor is never mistaken for a dead internet connection.

import { api, Stream, type Status } from "./api";
import { LiveSparkline } from "./charts";
import { renderPathViz } from "./pathviz";
import { clear, fmtBps, fmtDuration, fmtRTT, h } from "./util";

export function mountKiosk(app: HTMLElement): () => void {
  document.body.classList.add("kiosk");

  const params = new URLSearchParams(location.search);
  if (params.get("theme") === "light") {
    document.documentElement.setAttribute("data-theme", "light");
  }
  const scale = parseFloat(params.get("scale") ?? "1") || 1;

  let status: Status | null = null;
  let lastDataAt = Date.now();

  const outageBanner = h("div", { class: "kiosk-outage-banner" });
  const pathBox = h("div", { class: "pathviz" });
  const bigRow = h("div", { class: "kiosk-big-row" });
  const sparkBox = h("div", { class: "kiosk-spark" });
  const offline = h(
    "div",
    { class: "kiosk-offline" },
    h("div", { class: "title" }, "MONITOR OFFLINE"),
    h("div", { class: "sub" }, "The pingway backend is unreachable. This is a display problem, not (necessarily) an internet problem."),
  );

  const root = h("div", { class: "kiosk-root" }, outageBanner, pathBox, bigRow, sparkBox);
  if (scale !== 1) {
    root.style.transform = `scale(${scale})`;
    root.style.width = `${100 / scale}vw`;
    root.style.height = `${100 / scale}vh`;
  }
  app.append(root, offline);

  const spark = new LiveSparkline(sparkBox, Math.round(window.innerHeight * 0.16), 60);

  function big(label: string, value: string, unit: string, cls = ""): HTMLElement {
    return h(
      "div",
      { class: "kiosk-big" },
      h("div", { class: "label" }, label),
      h("div", { class: `value num ${cls}` }, value, h("small", {}, " " + unit)),
    );
  }

  function render() {
    if (!status) return;

    // outage banner with live elapsed time
    if (status.internet.state === "down") {
      const since = status.internet.outage_since ?? Date.now();
      outageBanner.textContent = `INTERNET DOWN — ${fmtDuration(Date.now() - since)}`;
      outageBanner.classList.add("show");
    } else {
      outageBanner.classList.remove("show");
    }

    renderPathViz(pathBox, status.targets, status.speedtest_running);

    // big numbers: primary tier-3 target latency + loss, last speed test
    const tier3 = status.targets.filter((t) => t.enabled && t.tier === 3);
    const primary = tier3[0];
    clear(bigRow);
    if (primary) {
      const down = primary.state === "down";
      bigRow.append(
        big(
          primary.name,
          down ? "DOWN" : fmtRTT(primary.last_rtt_us),
          down ? "" : "ms",
          down ? "loss-bad" : "loss-ok",
        ),
      );
      const loss = primary.loss_60s_pct;
      bigRow.append(
        big(
          "Loss 60s",
          loss.toFixed(1) + "%",
          "",
          loss < 2 ? "loss-ok" : loss <= 10 ? "loss-warn" : "loss-bad",
        ),
      );
    }
    const last = status.last_speedtest;
    if (last && !last.error) {
      bigRow.append(big("Down", fmtBps(last.download_bps).split(" ")[0], fmtBps(last.download_bps).split(" ")[1] ?? ""));
      bigRow.append(big("Up", fmtBps(last.upload_bps).split(" ")[0], fmtBps(last.upload_bps).split(" ")[1] ?? ""));
    }
  }

  async function refresh() {
    try {
      status = await api.status();
      lastDataAt = Date.now();
      const enabled = status.targets.filter((t) => t.enabled);
      spark.setTargets(enabled);
      render();
    } catch {
      /* offline watchdog handles it */
    }
  }

  const stream = new Stream({
    onPing: (samples) => {
      lastDataAt = Date.now();
      spark.push(samples);
    },
    onStatus: () => refresh(),
    onSpeedtest: () => refresh(),
  });

  // offline watchdog: no data (SSE or poll) for 15s => MONITOR OFFLINE
  const watchdog = setInterval(() => {
    offline.classList.toggle("show", Date.now() - lastDataAt > 15_000);
    render(); // keep elapsed-time counters moving
  }, 1000);

  refresh();
  stream.start();
  const poll = setInterval(refresh, 10_000);

  return () => {
    stream.stop();
    clearInterval(poll);
    clearInterval(watchdog);
    document.body.classList.remove("kiosk");
  };
}
