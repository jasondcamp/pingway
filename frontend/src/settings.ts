// Settings page: target CRUD, speed test engine/interval, retention knobs,
// Ookla EULA toggle.

import { api, type AppSettings, type Target } from "./api";
import { clear, h } from "./util";

export function mountSettings(app: HTMLElement): () => void {
  const targetsBox = h("div");
  const settingsBox = h("div");
  const errBox = h("div", { class: "error-text" });

  app.append(
    errBox,
    h("div", { class: "panel" }, h("h2", {}, "Targets"), targetsBox),
    h("div", { class: "panel" }, h("h2", {}, "Speed tests & retention"), settingsBox),
  );

  const showErr = (e: unknown) => {
    errBox.textContent = e ? String(e) : "";
  };

  // --- targets ---

  async function loadTargets() {
    const targets = await api.targets();
    renderTargets(targets);
  }

  function targetRow(t: Target | null, onDone: () => void): HTMLElement {
    const name = h("input", { value: t?.name ?? "", placeholder: "Name" }) as HTMLInputElement;
    const host = h("input", { value: t?.host ?? "", placeholder: "IP or hostname" }) as HTMLInputElement;
    const tier = h("select") as HTMLSelectElement;
    for (const [v, label] of [
      ["1", "1 — LAN"],
      ["2", "2 — ISP hop"],
      ["3", "3 — Internet"],
    ]) {
      tier.append(h("option", { value: v, selected: String(t?.tier ?? 3) === v }, label));
    }
    const enabled = h("input", { type: "checkbox", checked: t?.enabled ?? true }) as HTMLInputElement;

    const save = async () => {
      showErr(null);
      const payload = {
        name: name.value,
        host: host.value,
        tier: Number(tier.value),
        sort_order: t?.sort_order ?? 0,
        enabled: enabled.checked,
      };
      try {
        if (t) await api.updateTarget(t.id, payload);
        else await api.createTarget(payload);
        onDone();
      } catch (e) {
        showErr(e);
      }
    };

    const cells: HTMLElement[] = [
      h("td", {}, name),
      h("td", {}, host),
      h("td", {}, tier),
      h("td", { style: "text-align:center" }, enabled),
      h(
        "td",
        {},
        h("button", { onClick: save }, t ? "Save" : "Add"),
        t
          ? h(
              "button",
              {
                class: "danger",
                style: "margin-left:6px",
                onClick: async () => {
                  if (!confirm(`Delete target "${t.name}"? History is kept.`)) return;
                  showErr(null);
                  try {
                    await api.deleteTarget(t.id);
                    onDone();
                  } catch (e) {
                    showErr(e);
                  }
                },
              },
              "Delete",
            )
          : null,
      ) as HTMLElement,
    ];
    const tr = h("tr");
    for (const c of cells) tr.append(c);
    return tr;
  }

  function renderTargets(targets: Target[]) {
    clear(targetsBox);
    const table = h(
      "table",
      {},
      h(
        "thead",
        {},
        h(
          "tr",
          {},
          h("th", {}, "Name"),
          h("th", {}, "Host"),
          h("th", {}, "Tier"),
          h("th", {}, "Enabled"),
          h("th", {}, ""),
        ),
      ),
    );
    const tbody = h("tbody");
    for (const t of targets) tbody.append(targetRow(t, loadTargets));
    tbody.append(targetRow(null, loadTargets));
    table.append(tbody);
    targetsBox.append(table);
  }

  // --- app settings ---

  async function loadSettings() {
    const s = await api.settings();
    renderSettings(s);
  }

  function renderSettings(s: AppSettings) {
    clear(settingsBox);
    if (s.config_lock) {
      settingsBox.append(
        h(
          "div",
          { class: "muted", style: "margin-bottom:10px" },
          "CONFIG_LOCK is set — settings are managed by env/yaml and cannot be edited here.",
        ),
      );
    }

    const engine = h("select", { disabled: s.config_lock }) as HTMLSelectElement;
    for (const e of ["librespeed", "cloudflare", "ookla"]) {
      engine.append(h("option", { value: e, selected: s.speedtest_engine === e }, e));
    }
    const interval = h("input", {
      type: "number",
      value: String(s.speedtest_interval_minutes),
      min: "5",
      disabled: s.config_lock,
    }) as HTMLInputElement;
    const stEnabled = h("input", {
      type: "checkbox",
      checked: s.speedtest_enabled,
      disabled: s.config_lock,
    }) as HTMLInputElement;
    const eula = h("input", {
      type: "checkbox",
      checked: s.ookla_accept_eula,
      disabled: s.config_lock,
    }) as HTMLInputElement;
    const rawHours = h("input", {
      type: "number",
      value: String(s.retention_raw_hours),
      min: "2",
      disabled: s.config_lock,
    }) as HTMLInputElement;
    const rollupDays = h("input", {
      type: "number",
      value: String(s.retention_rollup_1m_days),
      min: "1",
      disabled: s.config_lock,
    }) as HTMLInputElement;

    settingsBox.append(
      h("label", { class: "field" }, h("span", { class: "field-name" }, "Speed tests enabled"), stEnabled),
      h("label", { class: "field" }, h("span", { class: "field-name" }, "Engine"), engine),
      h(
        "label",
        { class: "field" },
        h("span", { class: "field-name" }, "Interval (minutes, ±10% jitter)"),
        interval,
      ),
      h(
        "label",
        { class: "field" },
        h("span", { class: "field-name" }, "Accept Ookla EULA (downloads CLI)"),
        eula,
      ),
      h(
        "label",
        { class: "field" },
        h("span", { class: "field-name" }, "Raw sample retention (hours)"),
        rawHours,
      ),
      h(
        "label",
        { class: "field" },
        h("span", { class: "field-name" }, "1-minute rollup retention (days)"),
        rollupDays,
      ),
    );
    if (!s.config_lock) {
      settingsBox.append(
        h(
          "button",
          {
              class: "primary",
              style: "margin-top:10px",
              onClick: async () => {
                showErr(null);
                try {
                  await api.saveSettings({
                    speedtest_engine: engine.value,
                    speedtest_interval_minutes: Number(interval.value),
                    speedtest_enabled: stEnabled.checked,
                    ookla_accept_eula: eula.checked,
                    retention_raw_hours: Number(rawHours.value),
                    retention_rollup_1m_days: Number(rollupDays.value),
                    config_lock: false,
                  });
                  loadSettings();
                } catch (e) {
                  showErr(e);
                }
              },
            },
          "Save settings",
        ),
      );
    }
  }

  loadTargets().catch(showErr);
  loadSettings().catch(showErr);
  return () => {};
}
