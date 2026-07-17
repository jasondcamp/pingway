// Path visualization: [You] -> tier 1 -> tier 2 -> tier 3 -> [Internet].
// Nodes show name + live RTT + status color; links are colored by the
// health of the downstream group. Shared by dashboard and kiosk.

import type { TargetStatus } from "./api";
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
    { class: `path-node ${cls}`, title: `${t.host} — loss ${t.loss_60s_pct.toFixed(1)}% (60s)` },
    h("div", { class: "name" }, t.name),
    h("div", { class: "rtt num" }, rtt),
    h("div", { class: "sub" }, sub),
  );
}

// Layout: [You] -> tier 1 -> tier 2 -> [Internet] -> tier 3 anchors.
// The anchors are hosts on the far side of the internet, so they render
// beyond the cloud; the Internet node itself goes red when ALL anchors
// are down (that's the definition of an internet outage).
export function renderPathViz(root: HTMLElement, targets: TargetStatus[], speedtestRunning = false): void {
  clear(root);
  const enabled = targets.filter((t) => t.enabled);
  const local: TargetStatus[][] = [1, 2]
    .map((n) => enabled.filter((t) => t.tier === n))
    .filter((g) => g.length > 0);
  const anchors = enabled.filter((t) => t.tier === 3);

  root.append(h("div", { class: "path-node endpoint" }, "You"));

  for (const group of local) {
    const gh = worst(group.map((t) => healthClass(t, speedtestRunning)));
    root.append(h("div", { class: `path-link ${gh}` }));
    const tierBox = h("div", { class: "path-tier" });
    for (const t of group) tierBox.append(node(t, speedtestRunning));
    root.append(tierBox);
  }

  const anchorHealth: Health = anchors.length ? worst(anchors.map((t) => healthClass(t, speedtestRunning))) : "ok";
  const internetDown = anchors.length > 0 && anchors.every((t) => t.state === "down");
  const linkCls = internetDown ? "down" : anchorHealth;
  root.append(h("div", { class: `path-link ${linkCls}` }));
  root.append(
    h(
      "div",
      { class: `path-node endpoint ${internetDown ? "down" : ""}` },
      internetDown ? "OFFLINE" : "Internet",
    ),
  );

  if (anchors.length > 0) {
    root.append(h("div", { class: `path-link ${linkCls}` }));
    const tierBox = h("div", { class: "path-tier" });
    for (const t of anchors) tierBox.append(node(t, speedtestRunning));
    root.append(tierBox);
  }
}

// localizeFault returns a one-line diagnosis based on tier health, the
// "where is the problem" answer.
export function localizeFault(targets: TargetStatus[], speedtestRunning = false): string | null {
  const enabled = targets.filter((t) => t.enabled);
  const health = (n: number): Health | null => {
    const g = enabled.filter((t) => t.tier === n);
    return g.length ? worst(g.map((t) => healthClass(t, speedtestRunning))) : null;
  };
  const t1 = health(1);
  const t2 = health(2);
  const t3 = health(3);

  if (t3 === "down") {
    if (t1 === "down") return "Your LAN is unreachable — check your router and cabling.";
    if (t2 === "down") return "LAN is healthy but the ISP gateway is down — the problem is your ISP link.";
    return "LAN and ISP hop look fine but internet anchors are down — upstream ISP or routing problem.";
  }
  if (t3 === "warn") {
    if (t1 === "ok" && (t2 === "ok" || t2 === null))
      return "Local network healthy; packet loss or latency upstream — likely ISP congestion.";
    if (t1 === "warn" || t1 === "down")
      return "Problems start at your LAN — check local equipment first.";
    return "Elevated loss or latency on the path.";
  }
  if (t1 === "down") return "A LAN device is down but the internet path is fine.";
  if (t2 === "down") return "ISP gateway hop not answering pings (path may still be fine).";
  return null;
}
