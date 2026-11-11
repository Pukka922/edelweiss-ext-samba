import { cleanup, fireEvent, waitFor, within } from "@solidjs/testing-library";
import { createSignal } from "solid-js";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { MountView, ShareView } from "./api";
import { FolderTree, type Stale } from "./FolderTree";
import { folders, foldersRoute, mounts, share } from "./test/fixtures";
import { apiError, type Handler, json, renderExt, stubApi } from "./test/render";

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

/** Fotos with folder rights on Steuern, which has a folder 2025 in it. */
const perUser = () => share({ perUser: true, folders: ["Steuern"] });

function renderTree(
  opts: {
    routes?: Record<string, Handler>;
    share?: ShareView;
    mount?: MountView;
    aclReason?: string;
    busy?: boolean;
  } = {},
) {
  const api = stubApi({
    [foldersRoute("Fotos", "")]: () =>
      json(folders("", ["2024", { name: "Steuern", hasChildren: true, own: true }, "Urlaub Italien"])),
    [foldersRoute("Fotos", "Steuern")]: () =>
      json(
        folders("Steuern", ["2025"], { own: true, from: "Steuern", rights: { anna: "write", ben: "read" } }),
      ),
    ...opts.routes,
  });
  const [s, setShare] = createSignal(opts.share ?? perUser());
  const [stale, setStale] = createSignal<Stale>();
  const onFolder = vi.fn<(folder: string) => void>();
  const r = renderExt(
    () => (
      <FolderTree
        share={s()}
        mount={opts.mount ?? mounts()[0]}
        aclReason={opts.aclReason}
        stale={stale()}
        busy={opts.busy ?? false}
        onFolder={onFolder}
      />
    ),
    undefined,
    "de",
  );
  /** The line of a folder (path below the share), without the folders in it. */
  const line = (path: string) => r.container.querySelector<HTMLElement>(`[data-folder="${path}"]`);
  const reads = (dir: string) =>
    api.sent("GET", `/folders?share=Fotos&dir=${encodeURIComponent(dir)}`).length;
  return { ...r, api, setShare, setStale, onFolder, line, reads };
}

describe("FolderTree", () => {
  it("reads the folders of the share when it opens, and marks each: as the share, own rights (🔒) or inherited", async () => {
    const r = renderTree();
    await waitFor(() => expect(r.line("2024")).toBeTruthy());
    expect(r.line("2024")?.textContent).toContain("📁");
    expect(r.line("2024")?.textContent).toContain("wie die Freigabe");
    expect(r.line("Urlaub Italien")?.textContent).toContain("wie die Freigabe");
    expect(r.line("Steuern")?.textContent).toContain("🔒");
    expect(r.line("Steuern")?.textContent).toContain("eigene Rechte");
    // the rights of a folder with rights of its own are read with it
    await waitFor(() => expect(r.line("Steuern")?.textContent).toContain("anna ✎"));
    expect(r.line("Steuern")?.textContent).toContain("ben 👁");
    expect(r.api.calls.map((c) => c.path)).toEqual([
      "/folders?share=Fotos&dir=",
      "/folders?share=Fotos&dir=Steuern",
    ]);
    // a folder below one with rights of its own inherits them
    fireEvent.click(r.getByRole("button", { name: "Unterordner von Steuern" }));
    expect(r.line("Steuern/2025")?.textContent).toContain("📁");
    expect(r.line("Steuern/2025")?.textContent).toContain("erbt von Steuern");
    expect(r.api.calls).toHaveLength(2);
    expect(r.getByText("/mnt/daten/Fotos · ext4")).toBeTruthy();
    expect(
      r.getByText(
        "Klick auf einen Ordner, um seine Rechte zu setzen. Unterordner erben, solange sie keine eigenen haben.",
      ),
    ).toBeTruthy();
  });

  it("reads a folder only when it is opened, and once", async () => {
    const r = renderTree({
      routes: {
        [foldersRoute("Fotos", "")]: () =>
          json(folders("", [{ name: "Familie", hasChildren: true, own: false }])),
        [foldersRoute("Fotos", "Familie")]: () => json(folders("Familie", ["Oma"])),
      },
    });
    const toggle = await r.findByRole("button", { name: "Unterordner von Familie" });
    expect(toggle.getAttribute("aria-expanded")).toBe("false");
    expect(r.reads("Familie")).toBe(0);
    fireEvent.click(toggle);
    expect(toggle.getAttribute("aria-expanded")).toBe("true");
    await waitFor(() => expect(r.line("Familie/Oma")?.textContent).toContain("wie die Freigabe"));
    fireEvent.click(toggle);
    expect(r.line("Familie/Oma")).toBeNull();
    fireEvent.click(toggle);
    expect(r.line("Familie/Oma")).toBeTruthy();
    expect(r.reads("Familie")).toBe(1);
    // a folder without folders in it has nothing to open
    expect(r.queryByRole("button", { name: "Unterordner von Oma" })).toBeNull();
  });

  it("opens the rights of a folder on a click, and not while a job runs", async () => {
    const r = renderTree();
    fireEvent.click(await r.findByRole("button", { name: "Urlaub Italien" }));
    expect(r.onFolder).toHaveBeenCalledWith("Urlaub Italien");
    cleanup();
    const busy = renderTree({ busy: true });
    const folder = (await busy.findByRole("button", { name: "2024" })) as HTMLButtonElement;
    expect(folder.disabled).toBe(true);
  });

  it("is there to look at only on a disk without extended attributes, and says why", async () => {
    const r = renderTree({ mount: mounts()[1] });
    expect(
      await r.findByText(
        "Die Platte dieser Freigabe kann keine Ordner-Rechte halten: Das Dateisystem hält keine erweiterten Attribute. Der Baum ist nur zum Ansehen da.",
      ),
    ).toBeTruthy();
    await waitFor(() => expect(r.line("2024")).toBeTruthy());
    expect(within(r.line("2024") as HTMLElement).queryAllByRole("button")).toEqual([]);
    expect(r.queryByRole("button", { name: "Steuern" })).toBeNull();
    // what is in a folder can still be looked at
    expect(r.getByRole("button", { name: "Unterordner von Steuern" })).toBeTruthy();
  });

  it("is there to look at only once the server said that the disk cannot keep folder rights", async () => {
    const r = renderTree({ aclReason: "no_privilege" });
    expect(
      await r.findByText(
        "Die Platte dieser Freigabe kann keine Ordner-Rechte halten: Die Extension darf die Rechte dort nicht schreiben (Attribute security.*). Der Baum ist nur zum Ansehen da.",
      ),
    ).toBeTruthy();
    await waitFor(() => expect(r.line("2024")).toBeTruthy());
    expect(r.queryByRole("button", { name: "2024" })).toBeNull();
  });

  it("is there to look at only for a share with the warning system or root_owned, and says why", async () => {
    const UNSAFE =
      "Für Systemordner, Ordner von root und Home-Verzeichnisse gibt es keine Ordner-Rechte: Das Umstellen würde jede Datei darin für die Gruppe schreibbar machen – bei einem Home sperrt das die Anmeldung per SSH-Schlüssel.";
    for (const warnings of [["system"], ["root_owned"], ["system", "root_owned", "holds_mount"]]) {
      const r = renderTree({ share: share({ perUser: true, folders: ["Steuern"], warnings }) });
      expect(await r.findByText(UNSAFE)).toBeTruthy();
      await waitFor(() => expect(r.line("2024")).toBeTruthy());
      // a folder without rights of its own cannot be given any
      expect(r.queryByRole("button", { name: "2024" })).toBeNull();
      expect(r.queryByRole("button", { name: "Urlaub Italien" })).toBeNull();
      // what is in a folder can still be looked at
      expect(r.getByRole("button", { name: "Unterordner von Steuern" })).toBeTruthy();
      cleanup();
    }
    // the other warnings leave the tree as it is
    const plain = renderTree({ share: share({ perUser: true, warnings: ["holds_mount", "ram"] }) });
    expect(await plain.findByRole("button", { name: "2024" })).toBeTruthy();
    expect(plain.queryByText(/gibt es keine Ordner-Rechte/)).toBeNull();
  });

  it("keeps removing existing rights and erneut anwenden in a tree that is read-only for its warnings", async () => {
    const r = renderTree({
      share: share({ perUser: true, folders: ["Steuern"], warnings: ["root_owned"] }),
      routes: {
        [foldersRoute("Fotos", "")]: () =>
          json(
            folders("", ["2024", { name: "Steuern", hasChildren: false, own: true }], {
              disk: { rights: {}, mixed: true, partial: false },
            }),
          ),
        [foldersRoute("Fotos", "Steuern")]: () =>
          json(
            folders("Steuern", [], {
              own: true,
              from: "Steuern",
              rights: { anna: "write" },
              disk: { rights: { anna: "write" }, mixed: true, partial: false },
            }),
          ),
      },
    });
    // the folder with rights of its own opens its rights (to remove them) ...
    fireEvent.click(await r.findByRole("button", { name: "Steuern" }));
    expect(r.onFolder).toHaveBeenLastCalledWith("Steuern");
    // ... a mixed one has erneut anwenden, the share's own folder too
    await waitFor(() => expect(r.line("Steuern")?.textContent).toContain("gemischter Stand"));
    fireEvent.click(
      within(r.line("Steuern") as HTMLElement).getByRole("button", { name: "erneut anwenden" }),
    );
    expect(r.onFolder).toHaveBeenLastCalledWith("Steuern");
    const note = await r.findByText(/Gemischter Stand in der Freigabe selbst/);
    fireEvent.click(
      within(note.parentElement as HTMLElement).getByRole("button", { name: "erneut anwenden" }),
    );
    expect(r.onFolder).toHaveBeenLastCalledWith("");
    // a folder without rights of its own gets none
    expect(r.queryByRole("button", { name: "2024" })).toBeNull();
  });

  it("shows a mixed state with erneut anwenden, which opens the rights of the folder", async () => {
    const r = renderTree({
      routes: {
        [foldersRoute("Fotos", "Steuern")]: () =>
          json(
            folders("Steuern", [], {
              own: true,
              from: "Steuern",
              rights: { anna: "write" },
              disk: { rights: { anna: "write" }, mixed: true, partial: false },
            }),
          ),
      },
    });
    await waitFor(() => expect(r.line("Steuern")?.textContent).toContain("gemischter Stand"));
    fireEvent.click(
      within(r.line("Steuern") as HTMLElement).getByRole("button", { name: "erneut anwenden" }),
    );
    expect(r.onFolder).toHaveBeenLastCalledWith("Steuern");
    expect(r.line("2024")?.textContent).not.toContain("gemischter Stand");
  });

  it("says that the share's own folder is mixed, with erneut anwenden, which opens the rights of the share's folder", async () => {
    const r = renderTree({
      routes: {
        [foldersRoute("Fotos", "")]: () =>
          json(folders("", ["2024"], { disk: { rights: {}, mixed: true, partial: false } })),
      },
    });
    const note = await r.findByText(
      "Gemischter Stand in der Freigabe selbst: Nicht alle Dateien haben die Rechte der Freigabe – etwa nach einem Abbruch.",
    );
    fireEvent.click(
      within(note.parentElement as HTMLElement).getByRole("button", { name: "erneut anwenden" }),
    );
    expect(r.onFolder).toHaveBeenLastCalledWith("");
    cleanup();
    const view = renderTree({
      mount: mounts()[1],
      routes: {
        [foldersRoute("Fotos", "")]: () =>
          json(folders("", ["2024"], { disk: { rights: {}, mixed: true, partial: false } })),
      },
    });
    await view.findByText(/Gemischter Stand in der Freigabe selbst/);
    expect(view.queryByRole("button", { name: "erneut anwenden" })).toBeNull();
  });

  it("offers erneut anwenden for a mixed folder whose rights come from the share itself", async () => {
    const r = renderTree({
      routes: {
        [foldersRoute("Fotos", "")]: () =>
          json(folders("", [{ name: "Familie", hasChildren: true, own: false }])),
        [foldersRoute("Fotos", "Familie")]: () =>
          json(folders("Familie", [], { disk: { rights: {}, mixed: true, partial: false } })),
      },
    });
    fireEvent.click(await r.findByRole("button", { name: "Unterordner von Familie" }));
    await waitFor(() => expect(r.line("Familie")?.textContent).toContain("gemischter Stand"));
    expect(r.line("Familie")?.textContent).toContain("wie die Freigabe");
    fireEvent.click(
      within(r.line("Familie") as HTMLElement).getByRole("button", { name: "erneut anwenden" }),
    );
    expect(r.onFolder).toHaveBeenLastCalledWith("Familie");
  });

  it("reads each folder once after a folder-rights job, also when the next view brings the job's new rights", async () => {
    const r = renderTree();
    await waitFor(() => expect(r.reads("Steuern")).toBe(1));
    r.setStale({ share: "Fotos", folder: "Steuern" });
    // the view that the page reads after the job has the rights that the job wrote
    r.setShare(share({ perUser: true, folders: ["Steuern", "Urlaub Italien"] }));
    await new Promise((resolve) => setTimeout(resolve, 20));
    expect(r.reads("")).toBe(2);
    expect(r.reads("Steuern")).toBe(2);
    // a change of the rights after that is read again
    r.setShare(share({ perUser: true, folders: ["Steuern"] }));
    await waitFor(() => expect(r.reads("")).toBe(3));
    expect(r.reads("Steuern")).toBe(3);
  });

  it("reads a later change of the share's rights again after a job that changed none", async () => {
    const r = renderTree();
    await waitFor(() => expect(r.reads("Steuern")).toBe(1));
    r.setStale({ share: "Fotos", folder: "Steuern" });
    r.setShare(perUser());
    await new Promise((resolve) => setTimeout(resolve, 20));
    expect(r.reads("")).toBe(2);
    r.setShare(share({ perUser: true, folders: ["Steuern"], rights: { anna: "write" } }));
    await waitFor(() => expect(r.reads("")).toBe(3));
  });

  it("reads its folders again when a folder-rights job of the share ended, and the folder of the job too", async () => {
    let ended = false;
    const r = renderTree({
      routes: {
        [foldersRoute("Fotos", "")]: () =>
          json(
            folders("", ["2024", { name: "Urlaub Italien", hasChildren: false, own: ended }], {
              disk: { rights: {}, mixed: false, partial: false },
            }),
          ),
        [foldersRoute("Fotos", "Urlaub Italien")]: () =>
          json(
            folders("Urlaub Italien", [], {
              own: true,
              from: "Urlaub Italien",
              rights: { anna: "write" },
              disk: { rights: {}, mixed: true, partial: false },
            }),
          ),
      },
    });
    await waitFor(() => expect(r.line("2024")).toBeTruthy());
    const line = r.line("2024");
    r.setStale({ share: "Filme", folder: "" });
    await new Promise((resolve) => setTimeout(resolve, 20));
    expect(r.reads("")).toBe(1);
    ended = true;
    r.setStale({ share: "Fotos", folder: "Urlaub Italien" });
    await waitFor(() => expect(r.line("Urlaub Italien")?.textContent).toContain("gemischter Stand"));
    expect(r.line("Urlaub Italien")?.textContent).toContain("eigene Rechte");
    expect(
      within(r.line("Urlaub Italien") as HTMLElement).getByRole("button", { name: "erneut anwenden" }),
    ).toBeTruthy();
    expect(r.reads("")).toBe(2);
    expect(r.reads("Urlaub Italien")).toBe(1);
    expect(r.line("2024")).toBe(line);
  });

  it("keeps its lines, the open folders and the focus when the view gives the same share again", async () => {
    const r = renderTree();
    fireEvent.click(await r.findByRole("button", { name: "Unterordner von Steuern" }));
    await waitFor(() => expect(r.line("Steuern/2025")).toBeTruthy());
    const lines = ["2024", "Steuern", "Steuern/2025"].map(r.line);
    const button = r.getByRole("button", { name: "Urlaub Italien" });
    button.focus();
    r.setShare(perUser());
    r.setShare(perUser());
    await new Promise((resolve) => setTimeout(resolve, 20));
    expect(r.api.calls).toHaveLength(2);
    expect(["2024", "Steuern", "Steuern/2025"].map(r.line).every((l, i) => l === lines[i])).toBe(true);
    expect(button.isConnected).toBe(true);
    expect(document.activeElement).toBe(button);
  });

  it("reads its folders again when the rights of the share changed, and keeps its lines", async () => {
    const r = renderTree();
    await waitFor(() => expect(r.reads("Steuern")).toBe(1));
    const line = r.line("2024");
    r.setShare(share({ perUser: true, folders: ["Steuern"], rights: { anna: "write", ben: "read" } }));
    await waitFor(() => expect(r.reads("")).toBe(2));
    await waitFor(() => expect(r.reads("Steuern")).toBe(2));
    expect(r.line("2024")).toBe(line);
  });

  it("says why the folders cannot be read, that there are none, and that the list is cut short", async () => {
    const r = renderTree({
      routes: {
        [foldersRoute("Fotos", "")]: () =>
          apiError(409, "unavailable", 'the disk of the share "Fotos" is not there', { share: "Fotos" }),
      },
    });
    expect(await r.findByText("Die Platte der Freigabe Fotos ist nicht da.")).toBeTruthy();
    cleanup();
    const none = renderTree({ routes: { [foldersRoute("Fotos", "")]: () => json(folders("")) } });
    expect(await none.findByText("Hier sind keine Ordner.")).toBeTruthy();
    cleanup();
    const cut = renderTree({
      routes: { [foldersRoute("Fotos", "")]: () => json(folders("", ["a"], { truncated: true })) },
    });
    expect(await cut.findByText("Hier sind mehr Ordner, als die Liste zeigt.")).toBeTruthy();
  });
});
