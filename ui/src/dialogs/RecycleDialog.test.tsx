import { cleanup, fireEvent, within } from "@solidjs/testing-library";
import { createSignal } from "solid-js";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { RecycleResponse, ViewResponse } from "../api";
import { job, recycleBin, recycleRoute, share, shares, view } from "../test/fixtures";
import { apiError, type Handler, json, renderExt, settle, stubApi } from "../test/render";
import { RecycleDialog } from "./RecycleDialog";

beforeEach(() => {
  vi.useFakeTimers();
  vi.setSystemTime(new Date("2026-11-04T12:00:00Z"));
});

afterEach(() => {
  cleanup();
  vi.useRealTimers();
  vi.unstubAllGlobals();
});

/** The job J9 of kind on Fotos: running at its first step, then done. */
const jobDone = (kind: string, steps: string[]): Record<string, Handler> => {
  const first = job({ id: "J9", kind, target: "Fotos", steps, step: 0 });
  return {
    "POST /jobs": () => json(first, 202),
    "GET /jobs/J9": () => json({ ...first, status: "done", step: steps.length - 1 }),
  };
};

function renderBin(
  opts: {
    bin?: RecycleResponse | (() => RecycleResponse);
    routes?: Record<string, Handler>;
    busy?: boolean;
  } = {},
) {
  const bin = opts.bin ?? recycleBin();
  const api = stubApi({
    [recycleRoute("Fotos")]: () => json(typeof bin === "function" ? bin() : bin),
    ...opts.routes,
  });
  const [v, setV] = createSignal<ViewResponse>(view());
  const onClose = vi.fn();
  const onChanged = vi.fn();
  const r = renderExt(
    () => (
      <RecycleDialog
        share={share()}
        view={v}
        busy={() => opts.busy ?? false}
        onClose={onClose}
        onChanged={onChanged}
      />
    ),
    undefined,
    "de",
  );
  const dialog = () => r.getByRole("dialog", { name: "Papierkorb von Fotos" });
  const button = (name: string) => within(dialog()).getByRole("button", { name }) as HTMLButtonElement;
  /** The row of a file of the bin, by the name of the file. */
  const row = (name: string) =>
    within(dialog())
      .getAllByRole("row")
      .find((tr) => tr.querySelector("td")?.textContent === name) as HTMLElement;
  const cells = (name: string) =>
    within(row(name))
      .getAllByRole("cell")
      .map((td) => td.textContent);
  const jobs = () => api.sent("POST", "/jobs").map((c) => c.body);
  const reads = () => api.sent("GET", "/recycle?share=Fotos").length;
  return { ...r, api, setV, onClose, onChanged, dialog, button, row, cells, jobs, reads };
}

describe("RecycleDialog", () => {
  it("lists the files of the bin with the place they had, their size and when they were deleted", async () => {
    const r = renderBin();
    await settle();
    expect(r.dialog().textContent).toContain("1,2 GB · 340 Dateien · gelöscht wird nach 30 Tagen");
    expect(
      within(r.dialog())
        .getAllByRole("columnheader")
        .map((th) => th.textContent),
    ).toEqual(["Datei", "Alter Ort", "Größe", "Gelöscht", "Aktionen"]);
    expect(r.cells("IMG_2041.jpg").slice(0, 4)).toEqual([
      "IMG_2041.jpg",
      "2024/Sommer/",
      "4,1 MB",
      "vor 2 Tagen",
    ]);
    expect(r.cells("Kalender.pdf").slice(0, 4)).toEqual(["Kalender.pdf", "/", "1 MB", "vor 12 Tagen"]);
    expect(r.dialog().textContent).toContain(
      "Nichts wird überschrieben: Liegt am alten Ort schon eine Datei gleichen Namens, bekommt die zurückgeholte den Zusatz „(wiederhergestellt)“.",
    );
  });

  it("restores a file as a job, says where it went and what a name that is taken gets, and reads the bin again", async () => {
    const r = renderBin({ routes: jobDone("recycle-restore", ["restore"]) });
    await settle();
    fireEvent.click(r.button("Wiederherstellen 2024/Sommer/IMG_2041.jpg"));
    await settle();
    expect(r.jobs()).toEqual([{ kind: "recycle-restore", name: "Fotos", item: "2024/Sommer/IMG_2041.jpg" }]);
    expect(within(r.dialog()).getByText("Schritt 1/1: Wiederherstellen")).toBeTruthy();
    await vi.advanceTimersByTimeAsync(1000);
    expect(r.onChanged).toHaveBeenCalledWith(
      "„IMG_2041.jpg“ ist zurück in Fotos/2024/Sommer. Lag dort schon eine Datei gleichen Namens, heißt die zurückgeholte „IMG_2041 (wiederhergestellt).jpg“.",
    );
    expect(r.reads()).toBe(2);
    expect(r.onClose).not.toHaveBeenCalled();
  });

  it("names a file in the share's own folder, and one without an extension, as the server names them", async () => {
    const r = renderBin({
      bin: recycleBin([
        { path: "Kalender.pdf", size: 10, deleted: "2026-11-04T11:59:00Z" },
        { path: "a/.profile", size: 10, deleted: "2026-11-04T09:00:00Z" },
      ]),
      routes: jobDone("recycle-restore", ["restore"]),
    });
    await settle();
    expect(r.cells("Kalender.pdf")[3]).toBe("vor 1 Minute");
    expect(r.cells(".profile")[3]).toBe("vor 3 Stunden");
    fireEvent.click(r.button("Wiederherstellen Kalender.pdf"));
    await vi.advanceTimersByTimeAsync(1000);
    expect(r.onChanged).toHaveBeenLastCalledWith(
      "„Kalender.pdf“ ist zurück in Fotos. Lag dort schon eine Datei gleichen Namens, heißt die zurückgeholte „Kalender (wiederhergestellt).pdf“.",
    );
    fireEvent.click(r.button("Wiederherstellen a/.profile"));
    await vi.advanceTimersByTimeAsync(1000);
    expect(r.onChanged).toHaveBeenLastCalledWith(
      "„.profile“ ist zurück in Fotos/a. Lag dort schon eine Datei gleichen Namens, heißt die zurückgeholte „.profile (wiederhergestellt)“.",
    );
  });

  it("names a copy of recycle:versions by the name it goes back under", async () => {
    const r = renderBin({
      bin: recycleBin([{ path: "dir/Copy #1 of a.txt", size: 3, deleted: "2026-11-04T11:59:00Z" }]),
      routes: jobDone("recycle-restore", ["restore"]),
    });
    await settle();
    fireEvent.click(r.button("Wiederherstellen dir/Copy #1 of a.txt"));
    await vi.advanceTimersByTimeAsync(1000);
    expect(r.jobs()).toEqual([{ kind: "recycle-restore", name: "Fotos", item: "dir/Copy #1 of a.txt" }]);
    expect(r.onChanged).toHaveBeenLastCalledWith(
      "„a.txt“ ist zurück in Fotos/dir. Lag dort schon eine Datei gleichen Namens, heißt die zurückgeholte „a (wiederhergestellt).txt“.",
    );
  });

  it("words a restore that the file system cannot do without overwriting", async () => {
    const first = job({ id: "J9", kind: "recycle-restore", target: "Fotos", steps: ["restore"], step: 0 });
    const r = renderBin({
      routes: {
        "POST /jobs": () => json(first, 202),
        "GET /jobs/J9": () =>
          json({ ...first, status: "failed", code: "no_noreplace", error: "the file system cannot rename" }),
      },
    });
    await settle();
    fireEvent.click(r.button("Wiederherstellen Kalender.pdf"));
    await vi.advanceTimersByTimeAsync(1000);
    expect(within(r.dialog()).getByRole("alert").textContent).toContain(
      "Schritt 1/1 (Wiederherstellen): Das Dateisystem kann nicht sicher umbenennen. Nichts wurde zurückgeholt, damit nichts überschrieben wird.",
    );
  });

  it("says that a file is no longer in the bin, and reads the bin again", async () => {
    const r = renderBin({
      routes: {
        "POST /jobs": () =>
          apiError(404, "not_found", "there is no such item", { item: "2024/Sommer/IMG_2041.jpg" }),
      },
    });
    await settle();
    fireEvent.click(r.button("Wiederherstellen 2024/Sommer/IMG_2041.jpg"));
    await settle();
    expect(within(r.dialog()).getByRole("alert").textContent).toBe("Das gibt es im Papierkorb nicht mehr.");
    expect(r.reads()).toBe(2);
    // the other files can still be restored
    expect(r.button("Wiederherstellen Kalender.pdf").disabled).toBe(false);
  });

  it("empties the bin only after a confirm that says it cannot be undone", async () => {
    let emptied = false;
    const r = renderBin({
      bin: () => (emptied ? recycleBin([], { files: 0, bytes: 0 }) : recycleBin()),
      routes: {
        ...jobDone("recycle-empty", ["empty"]),
        "GET /jobs/J9": () => {
          emptied = true;
          return json(
            job({ id: "J9", kind: "recycle-empty", target: "Fotos", steps: ["empty"], status: "done" }),
          );
        },
      },
    });
    await settle();
    fireEvent.click(r.button("Papierkorb leeren …"));
    expect(r.jobs()).toEqual([]);
    const confirm = r.getByRole("dialog", { name: "Papierkorb von Fotos leeren" });
    expect(confirm.textContent).toContain(
      "Alle Dateien im Papierkorb von Fotos werden endgültig gelöscht. Das lässt sich nicht rückgängig machen.",
    );
    fireEvent.click(within(confirm).getByRole("button", { name: "Leeren" }));
    await settle();
    expect(r.jobs()).toEqual([{ kind: "recycle-empty", name: "Fotos" }]);
    await vi.advanceTimersByTimeAsync(1000);
    expect(r.onChanged).toHaveBeenCalledWith("Der Papierkorb von Fotos ist leer.");
    expect(r.queryByRole("dialog", { name: "Papierkorb von Fotos leeren" })).toBeNull();
    await settle();
    expect(r.reads()).toBe(2);
    expect(within(r.dialog()).getByText("Der Papierkorb ist leer.")).toBeTruthy();
    expect(r.button("Papierkorb leeren …").disabled).toBe(true);
  });

  it("closes the confirm on Abbrechen without emptying anything", async () => {
    const r = renderBin();
    await settle();
    fireEvent.click(r.button("Papierkorb leeren …"));
    const confirm = r.getByRole("dialog", { name: "Papierkorb von Fotos leeren" });
    fireEvent.click(within(confirm).getByRole("button", { name: "Abbrechen" }));
    expect(r.queryByRole("dialog", { name: "Papierkorb von Fotos leeren" })).toBeNull();
    expect(r.jobs()).toEqual([]);
  });

  it("says that the size was not counted to its end only when the count did not see the whole bin", async () => {
    const r = renderBin({ bin: recycleBin(undefined, { files: 5000, partial: true }) });
    await settle();
    expect(r.dialog().textContent).toContain("mehr als 1,2 GB · 5.000 Dateien · gelöscht wird nach 30 Tagen");
    expect(
      within(r.dialog()).getByText(
        "Nicht alles im Papierkorb ließ sich zählen: Größe und Anzahl sind ein Mindestwert.",
      ),
    ).toBeTruthy();
    expect(within(r.dialog()).queryByText(/Die Liste zeigt die neuesten/)).toBeNull();
  });

  it("says that the list holds only the newest files, with the exact size of the whole bin", async () => {
    const r = renderBin({ bin: recycleBin(undefined, { files: 6000 }, true) });
    await settle();
    expect(r.dialog().textContent).toContain("1,2 GB · 6.000 Dateien · gelöscht wird nach 30 Tagen");
    expect(r.dialog().textContent).not.toContain("mehr als");
    expect(within(r.dialog()).getByText("Die Liste zeigt die neuesten 3.")).toBeTruthy();
    expect(within(r.dialog()).queryByText(/Nicht alles im Papierkorb/)).toBeNull();
  });

  it("makes its own buttons inert behind the confirm of the empty, and gives them back after it", async () => {
    const r = renderBin();
    await settle();
    const own = ["Papierkorb leeren …", "Schließen", "Wiederherstellen Kalender.pdf"].map(r.button);
    fireEvent.click(r.button("Papierkorb leeren …"));
    const confirm = r.getByRole("dialog", { name: "Papierkorb von Fotos leeren" });
    expect(own.every((b) => b.closest("[inert]") !== null)).toBe(true);
    expect(within(confirm).getByRole("button", { name: "Leeren" }).closest("[inert]")).toBeNull();
    fireEvent.click(within(confirm).getByRole("button", { name: "Abbrechen" }));
    expect(own.every((b) => b.isConnected && b.closest("[inert]") === null)).toBe(true);
  });

  it("keeps its rows and the focus when the view is read again, and reads nothing", async () => {
    const r = renderBin();
    await settle();
    const rows = within(r.dialog()).getAllByRole("row");
    const restore = r.button("Wiederherstellen Kalender.pdf");
    restore.focus();
    r.setV(view());
    r.setV(view());
    await settle();
    expect(r.reads()).toBe(1);
    expect(
      within(r.dialog())
        .getAllByRole("row")
        .every((tr, i) => tr === rows[i]),
    ).toBe(true);
    expect(document.activeElement).toBe(restore);
  });

  it("keeps the rows of the files that stay when the bin is read again", async () => {
    let restored = false;
    const r = renderBin({
      bin: () => recycleBin(restored ? recycleBin().items.slice(1) : undefined),
      routes: {
        ...jobDone("recycle-restore", ["restore"]),
        "GET /jobs/J9": () => {
          restored = true;
          return json(
            job({ id: "J9", kind: "recycle-restore", target: "Fotos", steps: ["restore"], status: "done" }),
          );
        },
      },
    });
    await settle();
    const kalender = r.row("Kalender.pdf");
    fireEvent.click(r.button("Wiederherstellen 2024/Sommer/IMG_2041.jpg"));
    await vi.advanceTimersByTimeAsync(1000);
    await settle();
    expect(r.reads()).toBe(2);
    expect(
      within(r.dialog()).queryByRole("button", { name: "Wiederherstellen 2024/Sommer/IMG_2041.jpg" }),
    ).toBeNull();
    expect(r.row("Kalender.pdf")).toBe(kalender);
  });

  it("waits while another change runs, and closes when the share is gone", async () => {
    const r = renderBin({ busy: true });
    await settle();
    expect(r.button("Wiederherstellen Kalender.pdf").disabled).toBe(true);
    expect(r.button("Papierkorb leeren …").disabled).toBe(true);
    expect(
      within(r.dialog()).getByText("Gerade läuft eine andere Änderung – warte, bis sie fertig ist."),
    ).toBeTruthy();
    r.setV(view({ config: { ...view().config, shares: shares().slice(1) } }));
    expect(r.onClose).toHaveBeenCalled();
  });

  it("says why the bin cannot be read", async () => {
    const r = renderBin({
      routes: {
        [recycleRoute("Fotos")]: () =>
          apiError(409, "unavailable", 'the disk of the share "Fotos" is not there', { share: "Fotos" }),
      },
    });
    await settle();
    expect(within(r.dialog()).getByText("Die Platte der Freigabe Fotos ist nicht da.")).toBeTruthy();
  });
});
