import "./style.css";
import { h } from "./util";
import { mountDashboard } from "./dashboard";
import { mountKiosk } from "./kiosk";
import { mountSettings } from "./settings";

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
  container.append(
    h(
      "header",
      { class: "topbar" },
      h("h1", {}, "pingway"),
      h("span", { class: "conn-slot" }),
      h(
        "nav",
        { class: "nav" },
        nav("Dashboard", "/"),
        nav("Settings", "/settings"),
        h("a", { href: "/kiosk" }, "Kiosk"),
      ),
    ),
  );
  const page = h("div");
  container.append(page);
  appRoot.append(container);

  teardown = path.startsWith("/settings") ? mountSettings(page) : mountDashboard(page);
}

window.addEventListener("popstate", () => navigate(location.pathname, false));
navigate(location.pathname, false);
