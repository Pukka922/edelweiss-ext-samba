import { cleanup, fireEvent, waitFor, within } from "@solidjs/testing-library";
import { createSignal } from "solid-js";
import { afterEach, describe, expect, it, vi } from "vitest";
import { type Guard, PageGuard } from "../guard";
import { browse, browseRoute, mounts } from "../test/fixtures";
import { apiError, type Handler, json, renderExt, stubApi, type } from "../test/render";
import { Navigator, useBrowse } from "./Navigator";

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

function renderNav(
  opts: { value?: string; routes?: Record<string, Handler>; allowMkdir?: boolean; guard?: Guard } = {},
) {
  const api = stubApi({
    [browseRoute("/mnt/daten")]: () => json(browse("/mnt/daten", ["Filme", "Fotos"])),
    [browseRoute("/mnt/daten/Fotos")]: () => json(browse("/mnt/daten/Fotos", ["2024", "2025"])),
    [browseRoute("/mnt/usb1")]: () => json(browse("/mnt/usb1", ["backup"])),
    ...opts.routes,
  });
  const [value, setValue] = createSignal(opts.value ?? "/mnt/daten");
  const [disks, setDisks] = createSignal(mounts());
  const onChange = vi.fn((dir: string) => setValue(dir));
  const nav = () => (
    <Navigator mounts={disks()} value={value()} onChange={onChange} allowMkdir={opts.allowMkdir ?? true} />
  );
  const guard = opts.guard;
  const r = renderExt(
    () => (guard ? <PageGuard.Provider value={guard}>{nav()}</PageGuard.Provider> : nav()),
    undefined,
    "de",
  );
  /** The names of the folders it offers, in order. */
  const folders = () =>
    within(r.getByRole("list", { name: "Unterordner" }))
      .queryAllByRole("button")
      .map((b) => b.textContent);
  return { ...r, api, onChange, value, setDisks, folders };
}

describe("Navigator", () => {
  it("offers the data disks, and lists the folders of the one it shows", async () => {
    const r = renderNav();
    expect(
      r.getByRole("button", { name: "/mnt/daten · ext4 · 1,2 TB frei" }).getAttribute("aria-pressed"),
    ).toBe("true");
    expect(
      r.getByRole("button", { name: "/mnt/usb1 · exfat · 400 GB frei" }).getAttribute("aria-pressed"),
    ).toBe("false");
    await waitFor(() => expect(r.folders()).toEqual(["Filme", "Fotos"]));
    expect(r.api.sent("GET", "/browse?dir=%2Fmnt%2Fdaten")).toHaveLength(1);
    expect(r.getByText("nur Ordner · Systemordner werden nicht angeboten · keine Links")).toBeTruthy();
  });

  it("enters a folder, goes up with .., and back along the path", async () => {
    const r = renderNav();
    await waitFor(() => expect(r.folders()).toEqual(["Filme", "Fotos"]));
    // at the mount point there is nothing above
    expect(r.queryByRole("button", { name: "eine Ebene höher" })).toBeNull();
    fireEvent.click(r.getByRole("button", { name: "Fotos" }));
    expect(r.onChange).toHaveBeenLastCalledWith("/mnt/daten/Fotos");
    await waitFor(() => expect(r.folders()).toEqual(["..", "2024", "2025"]));
    const path = r.getByRole("navigation", { name: "Pfad" });
    expect(path.textContent).toBe("/mnt/daten›Fotos");
    fireEvent.click(r.getByRole("button", { name: "eine Ebene höher" }));
    expect(r.onChange).toHaveBeenLastCalledWith("/mnt/daten");
    await waitFor(() => expect(r.folders()).toEqual(["Filme", "Fotos"]));
    fireEvent.click(r.getByRole("button", { name: "Fotos" }));
    await waitFor(() => expect(r.folders()).toEqual(["..", "2024", "2025"]));
    fireEvent.click(
      within(r.getByRole("navigation", { name: "Pfad" })).getByRole("button", { name: "/mnt/daten" }),
    );
    expect(r.onChange).toHaveBeenLastCalledWith("/mnt/daten");
    await waitFor(() => expect(r.folders()).toEqual(["Filme", "Fotos"]));
  });

  it("switches to another data disk", async () => {
    const r = renderNav();
    fireEvent.click(r.getByRole("button", { name: "/mnt/usb1 · exfat · 400 GB frei" }));
    expect(r.onChange).toHaveBeenLastCalledWith("/mnt/usb1");
    await waitFor(() => expect(r.folders()).toEqual(["backup"]));
  });

  it("shows the folder it is at, when the answer for the one before comes later", async () => {
    let answerDaten: () => void = () => {};
    const r = renderNav({
      routes: {
        [browseRoute("/mnt/daten")]: () =>
          new Promise<Response>((resolve) => {
            answerDaten = () => resolve(json(browse("/mnt/daten", ["Filme", "Fotos"])));
          }),
      },
    });
    fireEvent.click(r.getByRole("button", { name: "/mnt/usb1 · exfat · 400 GB frei" }));
    await waitFor(() => expect(r.folders()).toEqual(["backup"]));
    answerDaten();
    await new Promise((resolve) => setTimeout(resolve, 20));
    expect(r.folders()).toEqual(["backup"]);
  });

  it("asks for a data disk first when it shows none, and reads nothing", () => {
    const r = renderNav({ value: "" });
    expect(r.getByText("Wähle eine Datenplatte.")).toBeTruthy();
    expect(r.api.calls).toEqual([]);
  });

  it("makes a folder where it is, after the guard of the page, and goes into it", async () => {
    const guard = vi.fn<Guard>((open) => {
      open();
      return Promise.resolve();
    });
    const r = renderNav({
      value: "/mnt/daten/Fotos",
      guard,
      routes: { "POST /browse/mkdir": () => json({ path: "/mnt/daten/Fotos/2026" }) },
    });
    await waitFor(() => expect(r.folders()).toEqual(["..", "2024", "2025"]));
    fireEvent.click(r.getByRole("button", { name: "Neuer Ordner …" }));
    type(r.getByLabelText("Name des neuen Ordners"), "2026");
    fireEvent.click(r.getByRole("button", { name: "Anlegen" }));
    await waitFor(() => expect(r.onChange).toHaveBeenLastCalledWith("/mnt/daten/Fotos/2026"));
    expect(guard).toHaveBeenCalledOnce();
    expect(r.api.sent("POST", "/browse/mkdir").map((c) => c.body)).toEqual([
      { dir: "/mnt/daten/Fotos", name: "2026" },
    ]);
    expect(r.queryByLabelText("Name des neuen Ordners")).toBeNull();
  });

  it("sends the name of a new folder trimmed", async () => {
    const r = renderNav({ routes: { "POST /browse/mkdir": () => json({ path: "/mnt/daten/2026" }) } });
    await waitFor(() => expect(r.folders()).toEqual(["Filme", "Fotos"]));
    fireEvent.click(r.getByRole("button", { name: "Neuer Ordner …" }));
    type(r.getByLabelText("Name des neuen Ordners"), "  2026 ");
    fireEvent.click(r.getByRole("button", { name: "Anlegen" }));
    await waitFor(() => expect(r.onChange).toHaveBeenLastCalledWith("/mnt/daten/2026"));
    expect(r.api.sent("POST", "/browse/mkdir").map((c) => c.body)).toEqual([
      { dir: "/mnt/daten", name: "2026" },
    ]);
  });

  it("reads nothing again for an equal list of disks, and keeps its buttons, its folders and Neuer Ordner", async () => {
    const r = renderNav();
    await waitFor(() => expect(r.folders()).toEqual(["Filme", "Fotos"]));
    const disk = r.getByRole("button", { name: "/mnt/usb1 · exfat · 400 GB frei" });
    const folder = r.getByRole("button", { name: "Fotos" });
    fireEvent.click(r.getByRole("button", { name: "Neuer Ordner …" }));
    const name = r.getByLabelText("Name des neuen Ordners") as HTMLInputElement;
    type(name, "neu");
    r.setDisks(mounts());
    r.setDisks(mounts());
    await new Promise((resolve) => setTimeout(resolve, 20));
    expect(r.api.sent("GET", "/browse?dir=%2Fmnt%2Fdaten")).toHaveLength(1);
    expect(disk.isConnected).toBe(true);
    expect(folder.isConnected).toBe(true);
    expect(name.isConnected).toBe(true);
    expect(name.value).toBe("neu");
  });

  it("shows the free space of a disk as a new list of disks gives it", async () => {
    const r = renderNav();
    const disk = r.getByRole("button", { name: "/mnt/usb1 · exfat · 400 GB frei" });
    r.setDisks(mounts().map((m) => (m.point === "/mnt/usb1" ? { ...m, free: 300 * 1000 ** 3 } : m)));
    expect(disk.textContent).toBe("/mnt/usb1 · exfat · 300 GB frei");
  });

  it("says that the folder is there already", async () => {
    const r = renderNav({
      routes: {
        "POST /browse/mkdir": () =>
          apiError(409, "exists", "there is a Fotos in /mnt/daten already", { field: "name" }),
      },
    });
    await waitFor(() => expect(r.folders()).toEqual(["Filme", "Fotos"]));
    fireEvent.click(r.getByRole("button", { name: "Neuer Ordner …" }));
    type(r.getByLabelText("Name des neuen Ordners"), "Fotos");
    fireEvent.click(r.getByRole("button", { name: "Anlegen" }));
    expect(await r.findByText("Diesen Ordner gibt es hier schon.")).toBeTruthy();
    expect(r.onChange).not.toHaveBeenCalled();
  });

  it("offers no Neuer Ordner in the folder of a share, and says why", async () => {
    const r = renderNav({
      value: "/mnt/daten/Fotos",
      routes: {
        [browseRoute("/mnt/daten/Fotos")]: () =>
          json(browse("/mnt/daten/Fotos", ["2024"], { inShare: "Fotos" })),
      },
    });
    await waitFor(() => expect(r.folders()).toContain("2024"));
    expect((r.getByRole("button", { name: "Neuer Ordner …" }) as HTMLButtonElement).disabled).toBe(true);
    expect(
      r.getByText(
        "Hier liegt die Freigabe Fotos: Ein neuer Ordner darin ließe sich nicht als eigene Freigabe anlegen.",
      ),
    ).toBeTruthy();
  });

  it("has no Neuer Ordner without allowMkdir", async () => {
    const r = renderNav({ allowMkdir: false });
    await waitFor(() => expect(r.folders()).toEqual(["Filme", "Fotos"]));
    expect(r.queryByRole("button", { name: "Neuer Ordner …" })).toBeNull();
  });

  it("has no link entries: it offers what the server lists alone, and goes through no link", async () => {
    const r = renderNav({
      value: "/mnt/daten/Fotos",
      routes: {
        [browseRoute("/mnt/daten/Fotos")]: () =>
          apiError(400, "invalid_input", "/mnt/daten/Fotos is a symbolic link", {
            field: "dir",
            reason: "link",
          }),
      },
    });
    expect(
      await r.findByText("Der Ordner ist ein Link oder liegt hinter einem – Links werden nicht verfolgt."),
    ).toBeTruthy();
    // nothing below the link is offered, and no folder is made there; the way up stays
    expect(r.folders()).toEqual([".."]);
    expect(r.queryByRole("button", { name: "Neuer Ordner …" })).toBeNull();
  });

  it("says that a folder is empty, and that a list is cut short", async () => {
    const r = renderNav({
      routes: { [browseRoute("/mnt/daten")]: () => json(browse("/mnt/daten", [])) },
    });
    expect(await r.findByText("Hier sind keine Ordner.")).toBeTruthy();
    cleanup();
    const cut = renderNav({
      routes: { [browseRoute("/mnt/daten")]: () => json(browse("/mnt/daten", ["a"], { truncated: true })) },
    });
    expect(await cut.findByText("Hier sind mehr Ordner, als die Liste zeigt.")).toBeTruthy();
  });

  it("says that no data disk is mounted", () => {
    stubApi({});
    const r = renderExt(
      () => <Navigator mounts={[]} value="" onChange={() => {}} allowMkdir />,
      undefined,
      "de",
    );
    expect(r.getByText("Keine Datenplatte ist eingehängt.")).toBeTruthy();
  });
});

describe("useBrowse", () => {
  it("reads a folder once, however often its accessor gives the same one, and again for another one or a refresh", async () => {
    const api = stubApi({
      [browseRoute("/mnt/daten")]: () => json(browse("/mnt/daten", ["Fotos"])),
      [browseRoute("/mnt/usb1")]: () => json(browse("/mnt/usb1", ["backup"])),
    });
    const [at, setAt] = createSignal({ dir: "/mnt/daten" });
    let refresh: () => void = () => {};
    const r = renderExt(() => {
      const b = useBrowse(() => at().dir);
      refresh = b.refresh;
      return <output>{b.answer()?.dir}</output>;
    });
    await waitFor(() => expect(r.container.querySelector("output")?.textContent).toBe("/mnt/daten"));
    setAt({ dir: "/mnt/daten" });
    setAt({ dir: "/mnt/daten" });
    await new Promise((resolve) => setTimeout(resolve, 20));
    expect(api.sent("GET", "/browse?dir=%2Fmnt%2Fdaten")).toHaveLength(1);
    setAt({ dir: "/mnt/usb1" });
    await waitFor(() => expect(r.container.querySelector("output")?.textContent).toBe("/mnt/usb1"));
    refresh();
    await waitFor(() => expect(api.sent("GET", "/browse?dir=%2Fmnt%2Fusb1")).toHaveLength(2));
  });
});
