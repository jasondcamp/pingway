// Path visualization: [You] -> tier 1 -> tier 2 -> tier 3 -> [Internet].
// Nodes show name + live RTT + status color; links are colored by the
// health of the downstream group. Shared by dashboard and kiosk.

import type { Glance, TargetStatus } from "./api";
import { h, clear, fmtRTT, healthClass } from "./util";

type Health = "ok" | "warn" | "down";

function worst(healths: Health[]): Health {
  if (healths.includes("down")) return "down";
  if (healths.includes("warn")) return "warn";
  return "ok";
}

function node(t: TargetStatus, speedtestRunning: boolean): HTMLElement {
  const cls = healthClass(t, speedtestRunning);
  const rtt = t.state === "down" ? "DOWN" : fmtRTT(t.last_rtt_us);
  const sub = t.state === "down" ? t.host : "ms";
  return h(
    "div",
    { class: `path-node ${cls}`, title: `${t.host} — loss ${t.loss_pct.toFixed(1)}% (5 min)` },
    h("div", { class: "name" }, t.name),
    h("div", { class: "rtt num" }, rtt),
    h("div", { class: "sub" }, sub),
  );
}

// Layout: [You] -> | LAN | -> | ISP NETWORK | -> | INTERNET |.
// Each tier renders inside a labeled zone whose border carries the zone's
// health color, making the ownership boundaries (your gear / your ISP's
// access network / the wider internet) explicit. The Internet zone shows
// OFFLINE when ALL anchors are down (the definition of an internet
// outage).
export function renderPathViz(root: HTMLElement, targets: TargetStatus[], speedtestRunning = false): void {
  clear(root);
  const enabled = targets.filter((t) => t.enabled);
  const lan = enabled.filter((t) => t.tier === 1);
  const isp = enabled.filter((t) => t.tier === 2);
  const anchors = enabled.filter((t) => t.tier === 3);
  const internetDown = anchors.length > 0 && anchors.every((t) => t.state === "down");

  const groupHealth = (g: TargetStatus[]): Health =>
    g.length ? worst(g.map((t) => healthClass(t, speedtestRunning))) : "ok";

  const zone = (label: string, g: TargetStatus[], health: Health, offline = false) => {
    root.append(h("div", { class: `path-link ${health}` }));
    const box = h(
      "div",
      { class: `path-zone ${health}` },
      h("div", { class: "path-zone-label" }, offline ? `${label} — OFFLINE` : label),
    );
    // 1-2 targets stack vertically; more split into two balanced rows so a
    // zone never grows taller than two lines (e.g. 4 anchors render 2x2)
    const tierBox = h("div", { class: "path-tier" });
    if (g.length > 2) {
      const split = Math.ceil(g.length / 2);
      for (const rowTargets of [g.slice(0, split), g.slice(split)]) {
        const row = h("div", { class: "path-tier-row" });
        for (const t of rowTargets) row.append(node(t, speedtestRunning));
        tierBox.append(row);
      }
    } else {
      for (const t of g) tierBox.append(node(t, speedtestRunning));
    }
    box.append(tierBox);
    root.append(box);
  };

  root.append(h("div", { class: "path-node endpoint" }, "You"));

  if (lan.length > 0) zone("LAN", lan, groupHealth(lan));
  if (isp.length > 0) zone("ISP network", isp, internetDown ? "down" : groupHealth(isp));
  if (anchors.length > 0) {
    zone("Internet", anchors, internetDown ? "down" : groupHealth(anchors), internetDown);
  }
}

// localizeFault turns the server's verdict into a sentence.
//
// It no longer decides anything. The previous version evaluated tier health
// here, in the renderer, and only produced a message when the internet tier
// was unhealthy: its last branches tested `t1 === "down"` and
// `t2 === "down"`, so a LAN that was merely degraded matched nothing. On a
// LAN at 95ms with 39% loss, healthClass returned "warn" (down comes from
// the outage detector, which had not fired), the internet anchors were fine,
// every branch fell through, and the page said nothing at all. Silence reads
// as health.
//
// The evaluation now happens once, in Go, and this maps the answer to words.
// The wall panel reads the same verdict from the same endpoint, so the two
// cannot disagree about whether the network is broken.
export function localizeFault(g: Glance | null): string | null {
  if (!g) return null;
  if (g.stale || g.status === "unknown")
    return "No probe results recently — the monitor may be restarting.";
  if (g.status === "ok" || g.worst_hop === null) return null;

  const down = g.status === "down";
  switch (g.worst_hop) {
    case "lan":
      return down
        ? "Your LAN is unreachable — check local equipment and cabling first."
        : "Problems start at your LAN — check local equipment first.";
    case "isp":
      return "LAN is healthy but the ISP hop is down and the internet is suffering — the problem is your ISP link.";
    case "internet":
      return down
        ? "LAN and ISP hop look fine but the internet is unreachable — upstream ISP or routing problem."
        : "Local network healthy; loss or latency upstream — likely ISP congestion.";
  }
  return null;
}
