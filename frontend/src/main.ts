import "./style.css";
import { h } from "./util";
import { mountDashboard } from "./dashboard";
import { mountKiosk } from "./kiosk";
import { mountSettings } from "./settings";
import { mountTimePicker } from "./timepicker";

const appRoot = document.getElementById("app")!;
let teardown: (() => void) | null = null;

function navigate(path: string, push = true) {
  teardown?.();
  appRoot.textContent = "";
  document.documentElement.removeAttribute("data-theme");
  if (push) history.pushState({}, "", path);

  if (path.startsWith("/kiosk")) {
    teardown = mountKiosk(appRoot);
    return;
  }

  // standard chrome for dashboard + settings
  const container = h("div", { class: "container" });
  const nav = (label: string, to: string) =>
    h(
      "a",
      {
        href: to,
        class: location.pathname === to ? "active" : "",
        onClick: (e: Event) => {
          e.preventDefault();
          navigate(to);
        },
      },
      label,
    );
  const pickerSlot = h("span", { class: "picker-slot" });
  const clock = h("span", { class: "clock" });
  const tickClock = () => {
    clock.textContent = new Date().toLocaleTimeString(undefined, {
      hour: "numeric",
      minute: "2-digit",
      second: "2-digit",
      timeZoneName: "short",
    });
  };
  tickClock();
  const clockTimer = window.setInterval(tickClock, 1000);
  container.append(
    h(
      "header",
      { class: "topbar" },
      h("img", { src: "/pingway-logo-white.png", alt: "pingway", class: "logo" }),
      h("span", { class: "conn-slot" }),
      pickerSlot,
      h(
        "nav",
        { class: "nav" },
        clock,
        nav("Dashboard", "/"),
        nav("Settings", "/settings"),
        h("a", { href: "/kiosk" }, "Kiosk"),
      ),
    ),
  );
  const page = h("div");
  container.append(page);
  appRoot.append(container);

  if (path.startsWith("/settings")) {
    teardown = mountSettings(page);
  } else {
    const stopPicker = mountTimePicker(pickerSlot);
    const stopDash = mountDashboard(page);
    teardown = () => {
      stopPicker();
      stopDash();
    };
  }
  const pageTeardown = teardown;
  teardown = () => {
    window.clearInterval(clockTimer);
    pageTeardown?.();
  };
}

window.addEventListener("popstate", () => navigate(location.pathname, false));
navigate(location.pathname, false);
