import { cleanup, within } from "@solidjs/testing-library";
import { createSignal, Show } from "solid-js";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { ConnectionView } from "./api";
import { ConnectionsTab } from "./ConnectionsTab";
import { connections } from "./test/fixtures";
import { apiError, type Handler, json, renderExt, settle, stubApi } from "./test/render";

const RELOAD_MS = 5000;

beforeEach(() => {
  vi.useFakeTimers();
  vi.setSystemTime(new Date(2026, 10, 4, 12, 0));
});

afterEach(() => {
  cleanup();
  vi.useRealTimers();
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

function renderTab(
  opts: {
    admin?: boolean;
    list?: () => ConnectionView[];
    routes?: Record<string, Handler>;
    locale?: "en" | "de";
  } = {},
) {
  const api = stubApi({
    "GET /connections": () => json(opts.list ? opts.list() : connections()),
    ...opts.routes,
  });
  const [open, setOpen] = createSignal(true);
  const [reload, setReload] = createSignal(0);
  const r = renderExt(
    () => (
      <Show when={open()}>
        <ConnectionsTab admin={opts.admin ?? true} reload={reload()} />
      </Show>
    ),
    undefined,
    opts.locale ?? "de",
  );
  const reads = () => api.sent("GET", "/connections").length;
  /** The texts of the cells of the row of a user. */
  const cells = (user: string) =>
    within(r.getAllByRole("row").find((tr) => tr.querySelector("td")?.textContent === user) as HTMLElement)
      .getAllByRole("cell")
      .map((td) => td.textContent);
  return { ...r, api, setOpen, setReload, reads, cells };
}

describe("ConnectionsTab", () => {
  it("lists who is connected: user, device, protocol, share, since when, and the open files", async () => {
    const r = renderTab();
    await settle();
    expect(r.getAllByRole("columnheader").map((th) => th.textContent)).toEqual([
      "Nutzer",
      "Gerät",
      "Protokoll",
      "Freigabe",
      "Seit",
      "Offene Dateien",
    ]);
    expect(r.cells("anna")).toEqual([
      "anna",
      "ANNA-LAPTOP · 192.168.1.23",
      "SMB 3.1.1",
      "Fotos",
      "10:42",
      "2024/Sommer/IMG_2050.jpg",
    ]);
    expect(r.cells("tv")).toEqual([
      "tv",
      "192.168.1.40",
      "SMB 3.0",
      "Filme",
      "gestern 20:15",
      "Serie/S01E03.mkv",
    ]);
    expect(r.getByText("wird alle paar Sekunden neu gelesen, solange der Reiter offen ist")).toBeTruthy();
  });

  it("names the other protocols, a connection of an earlier day, and one without open files", async () => {
    const [anna, tv] = connections() as [ConnectionView, ConnectionView];
    const r = renderTab({
      locale: "en",
      list: () => [
        {
          user: "scanner",
          machine: "",
          address: "fe80::1",
          protocol: "SMB2_10",
          share: "Scans",
          since: new Date(2026, 10, 1, 8, 5).toISOString(),
          files: [],
        },
        { ...anna, protocol: "SMB2_02", files: ["a.txt", "b.txt"] },
        { ...tv, user: "tv2", protocol: "SMB3_02" },
        { ...tv, user: "tv3", protocol: "NT1" },
      ],
    });
    await settle();
    const day = new Intl.DateTimeFormat("en", { dateStyle: "short", timeStyle: "short" }).format(
      new Date(2026, 10, 1, 8, 5),
    );
    expect(r.cells("scanner")).toEqual(["scanner", "fe80::1", "SMB 2.1", "Scans", day, "–"]);
    expect(r.cells("anna")[2]).toBe("SMB 2.0.2");
    expect(r.cells("anna")[5]).toBe("a.txtb.txt");
    expect(r.cells("tv2")[2]).toBe("SMB 3.0.2");
    const time = new Intl.DateTimeFormat("en", { timeStyle: "short" }).format(new Date(2026, 10, 3, 20, 15));
    expect(r.cells("tv2")[4]).toBe(`yesterday ${time}`);
    expect(r.cells("tv3")[2]).toBe("NT1");
  });

  it("reads them again every 5 seconds while it is open and the page is visible, and never once it is closed", async () => {
    const visibility = vi.spyOn(document, "visibilityState", "get").mockReturnValue("visible");
    const r = renderTab();
    await settle();
    expect(r.reads()).toBe(1);
    await vi.advanceTimersByTimeAsync(RELOAD_MS);
    expect(r.reads()).toBe(2);
    visibility.mockReturnValue("hidden");
    await vi.advanceTimersByTimeAsync(3 * RELOAD_MS);
    expect(r.reads()).toBe(2);
    visibility.mockReturnValue("visible");
    await vi.advanceTimersByTimeAsync(RELOAD_MS);
    expect(r.reads()).toBe(3);
    r.setOpen(false);
    await vi.advanceTimersByTimeAsync(3 * RELOAD_MS);
    expect(r.reads()).toBe(3);
  });

  it("reads them again on Reload", async () => {
    const r = renderTab();
    await settle();
    r.setReload(1);
    await settle();
    expect(r.reads()).toBe(2);
  });

  it("keeps its rows when a read gives the same, and when a connection goes", async () => {
    vi.spyOn(document, "visibilityState", "get").mockReturnValue("visible");
    let list = connections();
    const r = renderTab({ list: () => list });
    await settle();
    const rows = r.getAllByRole("row");
    await vi.advanceTimersByTimeAsync(RELOAD_MS);
    expect(r.reads()).toBe(2);
    expect(r.getAllByRole("row").every((tr, i) => tr === rows[i])).toBe(true);
    list = connections().slice(1);
    await vi.advanceTimersByTimeAsync(RELOAD_MS);
    const now = r.getAllByRole("row");
    expect(now).toHaveLength(2);
    expect(now[0]).toBe(rows[0]);
    expect(now[1]).toBe(rows[2]);
  });

  it("shows the hint without admin mode, and reads nothing", async () => {
    const r = renderTab({ admin: false });
    await vi.advanceTimersByTimeAsync(2 * RELOAD_MS);
    expect(r.getByText("Die Verbindungen sind nur im Admin-Modus sichtbar.")).toBeTruthy();
    expect(r.reads()).toBe(0);
    expect(r.queryByRole("table")).toBeNull();
  });

  it("says that nobody is connected", async () => {
    const r = renderTab({ list: () => [] });
    await settle();
    expect(r.getByText("Gerade ist niemand verbunden.")).toBeTruthy();
  });

  it("says why they cannot be read, and keeps what it read before", async () => {
    vi.spyOn(document, "visibilityState", "get").mockReturnValue("visible");
    let fail = false;
    const r = renderTab({
      routes: {
        "GET /connections": () => (fail ? apiError(500, "failed", "smbstatus: exit 1") : json(connections())),
      },
    });
    await settle();
    fail = true;
    await vi.advanceTimersByTimeAsync(RELOAD_MS);
    expect(r.getByRole("alert").textContent).toContain("Das ist fehlgeschlagen: smbstatus: exit 1");
    expect(r.cells("anna")[0]).toBe("anna");
  });
});
