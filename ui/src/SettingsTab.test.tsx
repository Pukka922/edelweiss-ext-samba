import { cleanup, fireEvent, within } from "@solidjs/testing-library";
import { createSignal } from "solid-js";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { IfaceView, SettingsView } from "./api";
import { SettingsTab } from "./SettingsTab";
import { renderExt, type } from "./test/render";

afterEach(() => {
  cleanup();
  vi.useRealTimers();
  vi.unstubAllGlobals();
});

const iface = (name: string, cidrs: [string, boolean][], over: Partial<IfaceView> = {}): IfaceView => ({
  name,
  bridge: false,
  discovery: cidrs.length > 0 && cidrs.every(([, priv]) => priv) && cidrs.some(([c]) => !c.includes(":")),
  addrs: cidrs.map(([cidr, priv]) => ({ cidr, private: priv })),
  ...over,
});

/** eth0 and wg0 are private; eth1 has a public address only, eth2 both, eth3 IPv6 only; docker0 and virbr0 are bridges. */
const ifaces = (): IfaceView[] => [
  iface("eth0", [["192.168.1.10/24", true]]),
  iface("wg0", [["10.8.0.1/24", true]]),
  iface("docker0", [["172.17.0.1/16", true]], { bridge: true, discovery: false }),
  iface("virbr0", [["192.168.122.1/24", true]], { bridge: true, discovery: false }),
  iface("eth1", [["203.0.113.5/24", false]]),
  iface("eth2", [
    ["192.168.2.10/24", true],
    ["203.0.113.9/24", false],
  ]),
  iface("eth3", [["fe80::1/64", true]]),
];

const settings = (over: Partial<SettingsView> = {}): SettingsView => ({
  name: "nas",
  workgroup: "WORKGROUP",
  smb2: false,
  discovery: true,
  interfaces: ["eth0"],
  ...over,
});

interface State {
  settings: SettingsView;
  ifaces: IfaceView[];
}

function renderTab(
  opts: Partial<State> & {
    readOnly?: boolean;
    busy?: boolean;
    locale?: "en" | "de";
  } = {},
) {
  const [state, setState] = createSignal<State>({
    settings: opts.settings ?? settings(),
    ifaces: opts.ifaces ?? ifaces(),
  });
  const onSave = vi.fn<(s: SettingsView) => void>();
  const r = renderExt(
    () => (
      <SettingsTab
        settings={state().settings}
        ifaces={state().ifaces}
        readOnly={opts.readOnly ?? false}
        busy={opts.busy ?? false}
        onSave={onSave}
      />
    ),
    undefined,
    opts.locale ?? "de",
  );
  const field = (label: string) => r.getByLabelText(label) as HTMLInputElement;
  const box = (name: string) => r.getByRole("checkbox", { name }) as HTMLInputElement;
  const row = (name: string) => box(name).closest("tr") as HTMLElement;
  const save = () => r.getByRole("button", { name: "Speichern" }) as HTMLButtonElement;
  /** What the page was asked to save, as the job request it makes of it. */
  const jobs = () => onSave.mock.calls.map(([settings]) => ({ kind: "settings", settings }));
  return { ...r, state, setState, onSave, field, box, row, save, jobs };
}

const SMB2 = /^Ältere Geräte zulassen \(SMB2\)/;
const DISCOVERY = /^Im Netzwerk anzeigen/;

describe("SettingsTab", () => {
  it("shows the settings of the preview", () => {
    const r = renderTab();
    expect(r.field("Name im Netzwerk").value).toBe("nas");
    expect(r.field("Arbeitsgruppe").value).toBe("WORKGROUP");
    expect((r.getByRole("checkbox", { name: SMB2 }) as HTMLInputElement).checked).toBe(false);
    expect((r.getByRole("checkbox", { name: DISCOVERY }) as HTMLInputElement).checked).toBe(true);
    expect(r.getByText(/Auch auf einer gewählten Schnittstelle nimmt Samba nur Verbindungen/)).toBeTruthy();
  });

  it("shows the defaults that empty names stand for: the host name and WORKGROUP", () => {
    const r = renderTab({ settings: settings({ name: "", workgroup: "" }) });
    expect(r.field("Name im Netzwerk").value).toBe("");
    expect(r.field("Name im Netzwerk").placeholder).toBe("alpine");
    expect(r.field("Arbeitsgruppe").value).toBe("");
    expect(r.field("Arbeitsgruppe").placeholder).toBe("WORKGROUP");
  });

  it("shows each interface with its addresses; one that is not chosen says it is not shared", () => {
    const r = renderTab();
    expect(r.box("eth0").checked).toBe(true);
    expect(r.box("eth0").disabled).toBe(false);
    expect(r.row("eth0").textContent).toContain("192.168.1.10/24");
    expect(r.row("eth0").textContent).not.toContain("nicht freigegeben");
    expect(r.box("wg0").checked).toBe(false);
    expect(r.box("wg0").disabled).toBe(false);
    expect(r.row("wg0").textContent).toContain("10.8.0.1/24");
    expect(r.row("wg0").textContent).toContain("nicht freigegeben");
  });

  it("disables the bridges, with never", () => {
    const r = renderTab();
    for (const name of ["docker0", "virbr0"]) {
      expect(r.box(name).disabled).toBe(true);
      expect(r.box(name).checked).toBe(false);
      expect(r.row(name).textContent).toContain("nie");
    }
  });

  it("disables an interface that has no private address, with the reason", () => {
    const r = renderTab();
    expect(r.box("eth1").disabled).toBe(true);
    expect(r.row("eth1").textContent).toContain("keine private Adresse");
    expect(r.row("eth1").textContent).toContain("203.0.113.5/24");
  });

  it("leaves a chosen interface that has become public-only checked, and lets it be unchecked", () => {
    const r = renderTab({ settings: settings({ interfaces: ["eth0", "eth1"] }) });
    expect(r.box("eth1").checked).toBe(true);
    expect(r.box("eth1").disabled).toBe(false);
    expect(r.row("eth1").textContent).not.toContain("keine private Adresse");
    fireEvent.click(r.box("eth1"));
    expect(r.box("eth1").checked).toBe(false);
    expect(r.box("eth1").disabled).toBe(true); // and then it is as an unchosen one
  });

  it("says that a chosen interface outside the discovery serves Samba, but is not seen in Explorer and Finder", () => {
    const r = renderTab({ settings: settings({ interfaces: ["eth0", "eth2", "eth3"] }) });
    const note = "Samba ja, aber nicht im Explorer/Finder sichtbar";
    expect(r.row("eth0").textContent).not.toContain(note);
    expect(r.row("eth2").textContent).toContain(note); // a public IPv4 beside the private one
    expect(r.row("eth3").textContent).toContain(note); // IPv6 only
    expect(r.box("eth2").disabled).toBe(false);
    expect(r.box("eth3").disabled).toBe(false);
    // the note follows the choice
    fireEvent.click(r.box("eth2"));
    expect(r.row("eth2").textContent).not.toContain(note);
    fireEvent.click(r.box("eth2"));
    expect(r.row("eth2").textContent).toContain(note);
  });

  it("shows a chosen interface that is not there now, checked, so that saving does not drop it", () => {
    const r = renderTab({ settings: settings({ interfaces: ["eth0", "tun9"] }) });
    expect(r.box("tun9").checked).toBe(true);
    expect(r.row("tun9").textContent).toContain("gerade nicht vorhanden");
    fireEvent.click(r.save());
    expect(r.jobs()).toEqual([
      expect.objectContaining({ settings: expect.objectContaining({ interfaces: ["eth0", "tun9"] }) }),
    ]);
  });

  it("hands the page the full settings to save", () => {
    const r = renderTab({ settings: settings({ interfaces: ["eth0", "eth1"] }) });
    type(r.field("Name im Netzwerk"), " nas2 ");
    type(r.field("Arbeitsgruppe"), "HOME");
    fireEvent.click(r.getByRole("checkbox", { name: SMB2 }));
    fireEvent.click(r.getByRole("checkbox", { name: DISCOVERY }));
    fireEvent.click(r.box("wg0"));
    fireEvent.click(r.box("eth1"));
    fireEvent.click(r.save());
    expect(r.jobs()).toEqual([
      {
        kind: "settings",
        settings: {
          name: "nas2",
          workgroup: "HOME",
          smb2: true,
          discovery: false,
          interfaces: ["eth0", "wg0"],
        },
      },
    ]);
  });

  it("never sends a bridge, also not one that was chosen before", () => {
    const r = renderTab({ settings: settings({ interfaces: ["eth0", "docker0"] }) });
    expect(r.box("docker0").checked).toBe(false);
    fireEvent.click(r.save());
    expect(r.jobs()).toEqual([
      expect.objectContaining({ settings: expect.objectContaining({ interfaces: ["eth0"] }) }),
    ]);
  });

  describe("the discovery switch", () => {
    const note = /Keine gewählte Schnittstelle ist rein lokal \(nur private IPv4-Adressen\)/;

    it("says so when it is on and no chosen interface is in the discovery", () => {
      const r = renderTab({ settings: settings({ interfaces: ["eth2"] }) });
      expect(r.getByText(note)).toBeTruthy();
      fireEvent.click(r.box("eth2")); // none chosen
      expect(r.getByText(note)).toBeTruthy();
      fireEvent.click(r.box("eth0")); // one in the discovery
      expect(r.queryByText(note)).toBeNull();
      fireEvent.click(r.getByRole("checkbox", { name: DISCOVERY })); // off
      fireEvent.click(r.box("eth0"));
      expect(r.queryByText(note)).toBeNull();
    });

    it("is quiet when a chosen interface is in the discovery", () => {
      expect(renderTab().queryByText(note)).toBeNull();
    });
  });

  it("has no Save and no changeable field on a read-only page, and a Save that waits while a job runs", () => {
    const ro = renderTab({ readOnly: true });
    expect(ro.queryByRole("button", { name: "Speichern" })).toBeNull();
    expect(ro.field("Name im Netzwerk").disabled).toBe(true);
    expect(ro.box("eth0").disabled).toBe(true);
    cleanup();
    expect(renderTab({ busy: true }).save().disabled).toBe(true);
  });

  it("is in English too", () => {
    const r = renderTab({ locale: "en" });
    expect(r.getByLabelText("Name on the network")).toBeTruthy();
    expect(r.getByLabelText("Workgroup")).toBeTruthy();
    expect(r.getByRole("button", { name: "Save" })).toBeTruthy();
    expect(r.row("docker0").textContent).toContain("never");
    expect(r.row("eth1").textContent).toContain("no private address");
    expect(r.row("wg0").textContent).toContain("not shared");
  });

  it("keeps what is typed, the choices and the focus when a new view brings the same", () => {
    const r = renderTab();
    const name = r.field("Name im Netzwerk");
    type(name, "nas2");
    name.focus();
    fireEvent.click(r.box("wg0"));
    const wg0 = r.box("wg0");
    const eth0 = r.box("eth0");
    const rowEth0 = r.row("eth0");
    r.setState({ settings: settings(), ifaces: ifaces() }); // as new objects
    r.setState({ settings: settings(), ifaces: ifaces() });
    expect(r.field("Name im Netzwerk")).toBe(name);
    expect(name.value).toBe("nas2");
    expect(document.activeElement).toBe(name);
    expect(r.box("wg0")).toBe(wg0);
    expect(wg0.checked).toBe(true);
    expect(r.box("eth0")).toBe(eth0);
    expect(r.row("eth0")).toBe(rowEth0);
    expect(r.onSave).not.toHaveBeenCalled();
    // a new interface appears: the others stay, and it is not chosen
    r.setState((s) => ({ ...s, ifaces: [...s.ifaces, iface("eth7", [["10.1.0.2/24", true]])] }));
    expect(r.box("eth0")).toBe(eth0);
    expect(r.box("eth7").checked).toBe(false);
    expect(r.row("eth7").textContent).toContain("nicht freigegeben");
    expect(within(r.row("eth7")).getByText("nicht freigegeben")).toBeTruthy();
  });
});
