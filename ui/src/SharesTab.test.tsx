import { cleanup, fireEvent, waitFor, within } from "@solidjs/testing-library";
import { createSignal } from "solid-js";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { ShareView } from "./api";
import { type Guard, PageGuard } from "./guard";
import type { ShareAction } from "./ShareRow";
import { SharesTab } from "./SharesTab";
import { folders, foldersRoute, GB, mounts, share, shares } from "./test/fixtures";
import { json, renderExt, stubApi } from "./test/render";

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

function renderTab(
  opts: { list?: ShareView[]; readOnly?: boolean; busy?: boolean; locale?: "en" | "de"; guard?: Guard } = {},
) {
  const onAction = vi.fn<(kind: ShareAction, s: ShareView) => void>();
  const onFolder = vi.fn<(s: ShareView, folder: string) => void>();
  const [list, setList] = createSignal(opts.list ?? shares());
  const tab = () => (
    <SharesTab
      shares={list()}
      mounts={mounts()}
      orphans={[]}
      readOnly={opts.readOnly ?? false}
      busy={opts.busy ?? false}
      stale={undefined}
      noAcl={{}}
      onAction={onAction}
      onFolder={onFolder}
      onDropOrphans={() => {}}
    />
  );
  const guard = opts.guard;
  const r = renderExt(
    () => (guard ? <PageGuard.Provider value={guard}>{tab()}</PageGuard.Provider> : tab()),
    undefined,
    opts.locale ?? "de",
  );
  /** The row of a share, found by its name. */
  const row = (name: string) =>
    r.getAllByRole("row").find((tr) => tr.querySelector("td .font-semibold")?.textContent === name)!;
  const cells = (name: string) => within(row(name)).getAllByRole("cell");
  const labels = (name: string) =>
    within(row(name))
      .queryAllByRole("button")
      .map((b) => b.getAttribute("aria-label"));
  return { ...r, onAction, onFolder, setList, row, cells, labels };
}

describe("SharesTab", () => {
  it("lists the shares with comment, folder, rights, recycle bin and status", () => {
    const r = renderTab();
    expect(r.getAllByRole("columnheader").map((th) => th.textContent)).toEqual([
      "Freigabe",
      "Ordner",
      "Rechte",
      "Papierkorb",
      "Status",
      "Aktionen",
    ]);
    const fotos = r.cells("Fotos");
    expect(fotos[0]?.textContent).toBe("FotosFamilienfotos");
    expect(fotos[1]?.textContent).toBe("/mnt/daten/Fotos");
    expect(fotos[2]?.textContent).toBe("anna ✎ben ✎oma 👁");
    expect(fotos[3]?.textContent).toBe("1,2 GB · 340 Dateien");
    expect(fotos[4]?.textContent).toBe("verfügbar");
    const filme = r.cells("Filme");
    expect(filme[0]?.textContent).toBe("Filmeversteckt");
    expect(filme[2]?.textContent).toBe("anna ✎tv 👁");
    expect(filme[3]?.textContent).toBe("aus");
  });

  it("marks write and read with their badges", () => {
    const r = renderTab();
    const badge = (text: string) => within(r.row("Fotos")).getByText(text);
    expect(badge("anna ✎").className).toContain("badge-success");
    expect(badge("oma 👁").className).toContain("badge-info");
  });

  it("shows the share whose disk is missing as Platte fehlt, and says so above the table", () => {
    const r = renderTab();
    const usb = r.cells("Backup-USB");
    expect(usb[3]?.textContent).toBe("–");
    expect(usb[4]?.textContent).toBe("Platte fehlt");
    expect(r.getByRole("alert").textContent).toBe(
      "Die Platte von „Backup-USB“ ist nicht eingehängt – die Freigabe ist abgeschaltet, bis sie wieder da ist.",
    );
  });

  it("shows the other reasons why a folder cannot be served", () => {
    const r = renderTab({
      list: [
        share({ name: "a", available: false, reason: "missing", owner: undefined }),
        share({ name: "b", available: false, reason: "link", owner: undefined }),
        share({ name: "c", available: false, reason: "no_owner", owner: undefined }),
      ],
    });
    expect(r.cells("a")[4]?.textContent).toBe("Ordner fehlt");
    expect(r.cells("b")[4]?.textContent).toBe("Ordner ist ein Link");
    expect(r.cells("c")[4]?.textContent).toBe("Besitzer fehlt");
    expect(r.queryByRole("alert")).toBeNull();
  });

  it("shows a share that smb.conf leaves out as not served, with the reason, and never as available", () => {
    const r = renderTab({
      list: [
        share({ name: "a", skipped: "no_users", rights: {} }),
        share({ name: "b", skipped: "bad_name" }),
        share({ name: "c", skipped: "no_owner", available: false, reason: "no_owner", owner: undefined }),
        share({ name: "d", skipped: "something_new" }),
      ],
    });
    expect(r.cells("a")[4]?.textContent).toBe("nicht bereitgestelltniemand hat Zugriff");
    expect(r.cells("b")[4]?.textContent).toBe("nicht bereitgestelltder Name geht für Samba nicht");
    expect(r.cells("c")[4]?.textContent).toBe("nicht bereitgestelltBesitzer fehlt");
    expect(r.cells("d")[4]?.textContent).toBe("nicht bereitgestelltsomething_new");
    for (const name of ["a", "b", "c", "d"]) expect(r.cells(name)[4]?.textContent).not.toContain("verfügbar");
  });

  it("says that a share needs a confirmation, and that a kernel interface below its folder locks it", () => {
    const r = renderTab({
      list: [
        share({ name: "a", skipped: "unconfirmed", unconfirmed: ["root_owned"], warnings: ["root_owned"] }),
        share({ name: "b", available: false, reason: "kernel", owner: undefined }),
      ],
    });
    expect(r.cells("a")[4]?.textContent).toBe(
      "nicht bereitgestelltBestätigung nötig: Der Ordner ist inzwischen ein Systemordner oder gehört root. Bestätige die Warnungen im Bearbeiten-Dialog, dann ist die Freigabe wieder erreichbar.",
    );
    expect(r.cells("b")[4]?.textContent).toBe(
      "Unter dem Ordner ist eine Kernel-Schnittstelle eingehängt (/proc, /sys oder /dev), deshalb ist die Freigabe gesperrt.",
    );
  });

  it("words a recycle bin of one file, and one that was not counted to its end, in English too", () => {
    const r = renderTab({
      list: [
        share({ name: "a", recycleBin: { files: 1, bytes: 4_100_000, partial: false } }),
        share({ name: "b", recycleBin: { files: 5000, bytes: 12 * GB, partial: true } }),
      ],
      locale: "en",
    });
    expect(r.cells("a")[3]?.textContent).toBe("4.1 MB · 1 file");
    expect(r.cells("b")[3]?.textContent).toBe("more than 12 GB · 5,000 files");
  });

  it("gives each share Bearbeiten and Löschen, and the recycle bin where it is on and its disk there, which tell the page", () => {
    const r = renderTab();
    expect(r.labels("Fotos")).toEqual([
      "Ordner-Rechte Fotos",
      "Bearbeiten Fotos",
      "Papierkorb öffnen Fotos",
      "Löschen Fotos",
    ]);
    expect(r.labels("Filme")).toEqual(["Ordner-Rechte Filme", "Bearbeiten Filme", "Löschen Filme"]);
    expect(r.labels("Backup-USB")).toEqual(["Bearbeiten Backup-USB", "Löschen Backup-USB"]);
    fireEvent.click(r.getByRole("button", { name: "Bearbeiten Fotos" }));
    expect(r.onAction).toHaveBeenLastCalledWith("edit", expect.objectContaining({ name: "Fotos" }));
    fireEvent.click(r.getByRole("button", { name: "Löschen Filme" }));
    expect(r.onAction).toHaveBeenLastCalledWith("delete", expect.objectContaining({ name: "Filme" }));
    fireEvent.click(r.getByRole("button", { name: "Papierkorb öffnen Fotos" }));
    expect(r.onAction).toHaveBeenLastCalledWith("recycle", expect.objectContaining({ name: "Fotos" }));
  });

  it("disables them while a job runs, but not the look into the folders", () => {
    const r = renderTab({ busy: true });
    for (const name of ["Bearbeiten Fotos", "Papierkorb öffnen Fotos", "Löschen Fotos"])
      expect((r.getByRole("button", { name }) as HTMLButtonElement).disabled).toBe(true);
    expect((r.getByRole("button", { name: "Ordner-Rechte Fotos" }) as HTMLButtonElement).disabled).toBe(
      false,
    );
  });

  it("opens the folder tree of a share below its row after the guard of the page, and closes it again", async () => {
    const api = stubApi({ [foldersRoute("Fotos", "")]: () => json(folders("", ["2024"])) });
    const guard = vi.fn<Guard>((open) => {
      open();
      return Promise.resolve();
    });
    const r = renderTab({ guard });
    const rows = r.getAllByRole("row");
    const toggle = r.getByRole("button", { name: "Ordner-Rechte Fotos" });
    expect(toggle.getAttribute("aria-expanded")).toBe("false");
    fireEvent.click(toggle);
    expect(guard).toHaveBeenCalledOnce();
    await waitFor(() => expect(r.container.querySelector('[data-folder="2024"]')).toBeTruthy());
    expect(toggle.getAttribute("aria-expanded")).toBe("true");
    // the tree is a row of its own, right below the share's
    const now = r.getAllByRole("row");
    expect(now[1]).toBe(rows[1]);
    expect(now[2]?.querySelector('[data-folder="2024"]')).toBeTruthy();
    expect(now[3]).toBe(rows[2]);
    fireEvent.click(r.getByRole("button", { name: "2024" }));
    expect(r.onFolder).toHaveBeenCalledWith(expect.objectContaining({ name: "Fotos" }), "2024");
    fireEvent.click(toggle);
    expect(guard).toHaveBeenCalledOnce();
    expect(r.container.querySelector('[data-folder="2024"]')).toBeNull();
    expect(api.sent("GET", "/folders?share=Fotos&dir=")).toHaveLength(1);
  });

  it("has no buttons for a read-only page", () => {
    const r = renderTab({ readOnly: true });
    expect(r.queryAllByRole("button")).toEqual([]);
  });

  it("explains the marks", () => {
    const r = renderTab();
    expect(
      r.getByText("✎ lesen und schreiben · 👁 nur lesen · Wer nicht genannt ist, sieht die Freigabe nicht."),
    ).toBeTruthy();
  });

  it("says that there is no share yet", () => {
    const r = renderTab({ list: [] });
    expect(r.queryByRole("table")).toBeNull();
    expect(r.getByText("Noch keine Freigabe. Lege die erste mit „Freigabe anlegen“ an.")).toBeTruthy();
    cleanup();
    const ro = renderTab({ list: [], readOnly: true });
    expect(ro.getByText("Noch keine Freigabe.")).toBeTruthy();
  });

  it("keeps its rows when a new list says the same, and updates a row in place", () => {
    const r = renderTab();
    const rows = r.getAllByRole("row");
    r.setList(shares());
    expect(r.getAllByRole("row").every((tr, i) => tr === rows[i])).toBe(true);
    const next = shares();
    next[0] = share({ comment: "Neu" });
    r.setList(next);
    expect(r.row("Fotos")).toBe(rows[1]);
    expect(r.cells("Fotos")[0]?.textContent).toBe("FotosNeu");
  });
});
