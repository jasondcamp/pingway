// Top-bar time range dropdown: relative presets + custom from/to.

import { PRESETS, timeRange } from "./timerange";
import { clear, h } from "./util";

function toLocalInputValue(ts: number): string {
  const d = new Date(ts);
  const pad = (n: number) => String(n).padStart(2, "0");
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}T${pad(d.getHours())}:${pad(d.getMinutes())}`;
}

export function mountTimePicker(slot: HTMLElement): () => void {
  const button = h("button", { class: "timepicker-btn num" }) as HTMLButtonElement;
  const panel = h("div", { class: "timepicker-panel" });
  const wrap = h("div", { class: "timepicker" }, button, panel);
  slot.append(wrap);

  let open = false;

  function refreshButton() {
    button.textContent = timeRange.get().label + " ▾";
  }

  function renderPanel() {
    clear(panel);
    const cur = timeRange.raw();

    const presetBox = h("div", { class: "timepicker-presets" });
    for (const p of PRESETS) {
      presetBox.append(
        h(
          "button",
          {
            class: cur.kind === "relative" && cur.ms === p.ms ? "active" : "",
            onClick: () => {
              timeRange.set({ kind: "relative", ms: p.ms });
              close();
            },
          },
          p.label,
        ),
      );
    }
    panel.append(h("div", { class: "timepicker-heading" }, "Relative range"), presetBox);

    const now = Date.now();
    const initFrom = cur.kind === "custom" ? cur.from : now - 3_600_000;
    const initTo = cur.kind === "custom" ? cur.to : now;
    const fromIn = h("input", { type: "datetime-local", value: toLocalInputValue(initFrom) }) as HTMLInputElement;
    const toIn = h("input", { type: "datetime-local", value: toLocalInputValue(initTo) }) as HTMLInputElement;
    const err = h("div", { class: "error-text", style: "display:none" });
    panel.append(
      h("div", { class: "timepicker-heading" }, "Custom range"),
      h("div", { class: "timepicker-custom" }, fromIn, h("span", { class: "muted" }, "→"), toIn),
      err,
      h(
        "button",
        {
          class: "primary",
          style: "margin-top:8px",
          onClick: () => {
            const from = new Date(fromIn.value).getTime();
            const to = new Date(toIn.value).getTime();
            if (isNaN(from) || isNaN(to) || to <= from) {
              err.textContent = "End must be after start.";
              err.style.display = "block";
              return;
            }
            timeRange.set({ kind: "custom", from, to });
            close();
          },
        },
        "Apply",
      ),
    );
  }

  function close() {
    open = false;
    panel.classList.remove("show");
    refreshButton();
  }

  button.addEventListener("click", (e) => {
    e.stopPropagation();
    open = !open;
    if (open) {
      renderPanel();
      panel.classList.add("show");
    } else {
      close();
    }
  });
  const onDocClick = (e: MouseEvent) => {
    if (open && !wrap.contains(e.target as Node)) close();
  };
  document.addEventListener("click", onDocClick);

  const unsub = timeRange.subscribe(refreshButton);
  refreshButton();

  return () => {
    document.removeEventListener("click", onDocClick);
    unsub();
    wrap.remove();
  };
}
