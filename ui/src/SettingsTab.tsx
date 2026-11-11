import { useI18n, useSession } from "@edelweiss/sdk";
import { createMemo, createSignal, For, Show } from "solid-js";
import type { IfaceView, SettingsView } from "./api";
import { serverName } from "./format";
import { keyed } from "./keys";
import { Check, Field } from "./Modal";

/** An interface of the table: one that the view has, or one that is chosen but is not there now (a VPN that is down). */
type Row = IfaceView & { gone: boolean };

/**
 * The settings tab: the name in the network, the workgroup, the switch for older devices (SMB2), the switch for the
 * discovery (Explorer and Finder), and the interfaces Samba is reachable over. "Speichern" hands the whole settings to the
 * page (onSave), which asks for admin mode, sends them as the job `settings` and follows it, whichever tab is open.
 *
 * An interface is shown as:
 *   - a bridge of Docker or libvirt: disabled, "nie" (never chosen, never sent);
 *   - one with no private address that is not chosen: disabled, "keine private Adresse";
 *   - one that is chosen: checked, and it can be unchecked whatever it has now (also one that has become public-only, or
 *     is not there now);
 *   - one with a private address: checkable. "nicht freigegeben" says that the saved settings do not have it, and a chosen
 *     one that the discovery would not answer on (a public IPv4 beside the private one, or no private IPv4: the field
 *     `discovery` of the view) says "Samba ja, aber nicht im Explorer/Finder sichtbar".
 * The discovery switch on with no chosen interface in the discovery says that Explorer and Finder find nothing.
 *
 * The form is the user's draft, taken from the settings once: a new view every few seconds changes nothing in it. The rows
 * are by the name of the interface, so a new view keeps them and the focus. An empty name and workgroup show what Samba
 * takes then as their placeholders: the host name (up to its first dot) and WORKGROUP.
 */
export function SettingsTab(props: {
  settings: SettingsView;
  ifaces: IfaceView[];
  readOnly: boolean;
  /** A job runs, the save of these settings included. */
  busy: boolean;
  /** Save: the page asks for admin mode, sends the settings as a job and follows it. */
  onSave: (settings: SettingsView) => void;
}) {
  const { t } = useI18n();
  const session = useSession();
  const start = props.settings;
  const [name, setName] = createSignal(start.name);
  const [workgroup, setWorkgroup] = createSignal(start.workgroup);
  const [smb2, setSmb2] = createSignal(start.smb2);
  const [discovery, setDiscovery] = createSignal(start.discovery);
  const [chosen, setChosen] = createSignal(
    start.interfaces.filter((n) => !props.ifaces.some((i) => i.name === n && i.bridge)),
  );
  const list = createMemo<Row[]>(() => {
    const have = new Set(props.ifaces.map((i) => i.name));
    const gone = props.settings.interfaces
      .filter((n) => !have.has(n))
      .map((n): Row => ({ name: n, bridge: false, discovery: false, addrs: [], gone: true }));
    return [...props.ifaces.map((i): Row => ({ ...i, gone: false })), ...gone];
  });
  const rows = keyed(list, (r) => r.name);
  const noDiscovery = () =>
    discovery() && !props.ifaces.some((i) => i.discovery && !i.bridge && chosen().includes(i.name));
  const locked = () => props.readOnly || props.busy;

  const toggle = (iface: string, on: boolean) =>
    setChosen((now) => (on ? (now.includes(iface) ? now : [...now, iface]) : now.filter((n) => n !== iface)));
  const send = () => {
    const settings: SettingsView = {
      name: name().trim(),
      workgroup: workgroup().trim(),
      smb2: smb2(),
      discovery: discovery(),
      interfaces: list()
        .filter((r) => chosen().includes(r.name))
        .map((r) => r.name),
    };
    props.onSave(settings);
  };

  return (
    <section class="card card-border bg-base-100">
      <div class="card-body gap-3 p-4">
        <Field label={t("settings.name")}>
          {(id) => (
            <input
              id={id}
              type="text"
              class="input input-sm w-full"
              autocomplete="off"
              placeholder={serverName("", session.info()?.hostname)}
              disabled={locked()}
              value={name()}
              onInput={(e) => setName(e.currentTarget.value)}
            />
          )}
        </Field>
        <Field label={t("settings.workgroup")}>
          {(id) => (
            <input
              id={id}
              type="text"
              class="input input-sm w-full"
              autocomplete="off"
              placeholder="WORKGROUP"
              disabled={locked()}
              value={workgroup()}
              onInput={(e) => setWorkgroup(e.currentTarget.value)}
            />
          )}
        </Field>
        <Check label={t("settings.smb2")} checked={smb2()} disabled={locked()} onChange={setSmb2} />
        <div>
          <Check
            label={t("settings.discovery")}
            checked={discovery()}
            disabled={locked()}
            onChange={setDiscovery}
          />
          <Show when={noDiscovery()}>
            <p class="text-xs text-warning">{t("services.noLocalInterface")}</p>
          </Show>
        </div>
        <div class="flex flex-col gap-1">
          <span class="text-sm">{t("settings.interfaces")}</span>
          <div class="overflow-x-auto">
            <table class="table table-sm">
              <tbody>
                <For each={rows.keys()}>
                  {(iface) => {
                    const r = rows.track(iface);
                    const checked = () => chosen().includes(iface);
                    const hasPrivate = () => r().addrs.some((a) => a.private);
                    const disabled = () => locked() || r().bridge || (!checked() && !hasPrivate());
                    const notes = () => {
                      const row = r();
                      if (row.bridge) return [t("settings.never")];
                      if (row.gone) return [t("settings.gone")];
                      if (!checked() && !hasPrivate()) return [t("settings.noPrivate")];
                      return [
                        ...(props.settings.interfaces.includes(iface) ? [] : [t("settings.notShared")]),
                        ...(checked() && !row.discovery ? [t("settings.notDiscoverable")] : []),
                      ];
                    };
                    return (
                      <tr>
                        <td class="w-px">
                          <input
                            type="checkbox"
                            class="checkbox checkbox-sm"
                            aria-label={iface}
                            checked={checked()}
                            disabled={disabled()}
                            onChange={(e) => toggle(iface, e.currentTarget.checked)}
                          />
                        </td>
                        <td class="font-semibold">{iface}</td>
                        <td>
                          {r()
                            .addrs.map((a) => a.cidr)
                            .join(", ")}
                        </td>
                        <td class="text-sm opacity-70">{notes().join(" · ")}</td>
                      </tr>
                    );
                  }}
                </For>
              </tbody>
            </table>
          </div>
          <p class="text-xs opacity-70">{t("settings.note")}</p>
        </div>
        <Show when={!props.readOnly}>
          <div class="flex justify-end">
            <button type="button" class="btn btn-primary" disabled={props.busy} onClick={send}>
              {t("settings.save")}
            </button>
          </div>
        </Show>
      </div>
    </section>
  );
}
