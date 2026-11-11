import { cleanup, fireEvent, waitFor, within } from "@solidjs/testing-library";
import { createSignal } from "solid-js";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { FoldersResponse, JobState, ShareView, ViewResponse } from "../api";
import { folders, foldersRoute, job, share, shares, users, view } from "../test/fixtures";
import { apiError, type Handler, json, noContent, renderExt, settle, stubApi } from "../test/render";
import { FolderRightsDialog } from "./FolderRightsDialog";

afterEach(() => {
  cleanup();
  vi.useRealTimers();
  vi.unstubAllGlobals();
});

const STEPS = ["config", "reload", "acl", "lbu"];

/** The folder-rights job J9 on Fotos/<folder>: running at its first step, then done. */
const jobDone = (folder = "Steuern", steps = STEPS): Record<string, Handler> => {
  const first = job({ id: "J9", kind: "folder-rights", target: `Fotos/${folder}`, steps, step: 0 });
  return {
    "POST /jobs": () => json(first, 202),
    "GET /jobs/J9": () => json({ ...first, status: "done", step: steps.length - 1 }),
  };
};

/** Steuern, a folder of Fotos with rights of its own: anna reads and writes, ben reads. */
const steuern = (over: Partial<FoldersResponse> = {}) =>
  folders("Steuern", ["2025"], {
    own: true,
    from: "Steuern",
    rights: { anna: "write", ben: "read" },
    ...over,
  });

function renderDialog(
  opts: {
    share?: ShareView;
    folder?: string;
    answer?: FoldersResponse;
    routes?: Record<string, Handler>;
    busy?: boolean;
    v?: ViewResponse;
  } = {},
) {
  const folder = opts.folder ?? "Steuern";
  const api = stubApi({
    [foldersRoute("Fotos", folder)]: () => json(opts.answer ?? steuern()),
    ...opts.routes,
  });
  const [v, setV] = createSignal(opts.v ?? view());
  const onClose = vi.fn();
  const onDone = vi.fn();
  const onReload = vi.fn();
  const onEnded = vi.fn();
  const onNoAcl = vi.fn();
  const r = renderExt(
    () => (
      <FolderRightsDialog
        share={opts.share ?? share({ perUser: true, folders: ["Steuern"] })}
        folder={folder}
        view={v}
        busy={() => opts.busy ?? false}
        onClose={onClose}
        onDone={onDone}
        onReload={onReload}
        onEnded={onEnded}
        onNoAcl={onNoAcl}
      />
    ),
    undefined,
    "de",
  );
  const dialog = () =>
    r.getByRole("dialog", { name: `Rechte für „${folder === "" ? "Fotos" : `Fotos/${folder}`}“` });
  const button = (name: string) => within(dialog()).getByRole("button", { name }) as HTMLButtonElement;
  const group = (user: string) => within(dialog()).getByRole("radiogroup", { name: user });
  const pick = (user: string, label: string) =>
    fireEvent.click(within(group(user)).getByRole("radio", { name: label }));
  const jobs = () => api.sent("POST", "/jobs").map((c) => c.body);
  const reads = () => api.sent("GET", `/folders?share=Fotos&dir=${encodeURIComponent(folder)}`).length;
  return {
    ...r,
    api,
    setV,
    onClose,
    onDone,
    onReload,
    onEnded,
    onNoAcl,
    dialog,
    button,
    group,
    pick,
    jobs,
    reads,
  };
}

describe("FolderRightsDialog", () => {
  it("shows the rights of the folder, each at most what it may have, and says what Anwenden does", async () => {
    const r = renderDialog();
    await waitFor(() => expect(within(r.dialog()).queryAllByRole("radiogroup")).toHaveLength(3));
    expect(
      within(r.dialog())
        .getAllByRole("radiogroup")
        .map((g) => g.getAttribute("aria-label")),
    ).toEqual(["anna", "ben", "oma"]);
    expect(
      within(r.group("oma"))
        .getAllByRole("radio")
        .map((o) => o.getAttribute("aria-label")),
    ).toEqual(["kein Zugriff", "lesen"]);
    expect((within(r.group("ben")).getByRole("radio", { name: "lesen" }) as HTMLInputElement).checked).toBe(
      true,
    );
    const text = r.dialog().textContent;
    expect(text).toContain("eigene Rechte");
    // a folder in the share's own folder is capped by the share
    expect(within(r.dialog()).getAllByRole("row")[0]?.textContent).toContain("Freigabe: ✎");
    expect(within(r.dialog()).getAllByRole("row")[2]?.textContent).toContain("Freigabe: 👁");
    expect(text).toContain(
      "Rechte können hier nur enger sein als auf der Freigabe. Wer keine Rechte hat, sieht den Ordner nicht.",
    );
    expect(text).toContain(
      "Die Rechte werden an jede Datei und jeden Unterordner geschrieben (nur Samba-Rechte; Besitzer und Linux-Rechte bleiben) – das läuft als Vorgang mit Fortschritt, den du abbrechen kannst.",
    );
    expect(r.reads()).toBe(1);
  });

  it("names the folder above as the cap of a folder further down", async () => {
    const r = renderDialog({
      folder: "Steuern/2025",
      answer: folders("Steuern/2025", [], {
        from: "Steuern",
        rights: { anna: "write", ben: "read" },
        max: { anna: "write", ben: "read" },
      }),
    });
    await waitFor(() => expect(within(r.dialog()).queryAllByRole("radiogroup")).toHaveLength(2));
    const rows = within(r.dialog()).getAllByRole("row");
    expect(rows[0]?.textContent).toContain("Ordner darüber: ✎");
    expect(rows[1]?.textContent).toContain("Ordner darüber: 👁");
    expect(r.dialog().textContent).not.toContain("Freigabe: ");
    expect(r.dialog().textContent).toContain(
      "Rechte können hier nur enger sein als im Ordner darüber. Wer keine Rechte hat, sieht den Ordner nicht.",
    );
    expect(r.dialog().textContent).not.toContain("enger sein als auf der Freigabe");
  });

  it("sends only what it shows: every right at most its cap, and only users of the view", async () => {
    vi.useFakeTimers();
    const r = renderDialog({
      answer: steuern({
        rights: { anna: "write", ben: "write", oma: "write", gone: "read" },
        max: { anna: "write", ben: "write", oma: "read", gone: "read" },
      }),
      routes: jobDone(),
    });
    await settle();
    r.pick("ben", "lesen");
    fireEvent.click(r.button("Anwenden"));
    await settle();
    expect(r.jobs()).toEqual([
      {
        kind: "folder-rights",
        name: "Fotos",
        folder: "Steuern",
        rights: { anna: "write", ben: "read", oma: "read" },
      },
    ]);
    await vi.advanceTimersByTimeAsync(1000);
    expect(r.onDone).toHaveBeenCalledWith("Rechte für „Fotos/Steuern“ gesetzt.");
    expect(r.onEnded).toHaveBeenCalled();
  });

  it("sends no user that has left the view since the table was changed", async () => {
    vi.useFakeTimers();
    const r = renderDialog({ routes: jobDone() });
    await settle();
    r.pick("ben", "kein Zugriff");
    r.pick("oma", "lesen");
    r.setV(view({ users: users().filter((u) => u.name !== "oma") }));
    expect(within(r.dialog()).queryByRole("radiogroup", { name: "oma" })).toBeNull();
    fireEvent.click(r.button("Anwenden"));
    await settle();
    expect(r.jobs()).toEqual([
      { kind: "folder-rights", name: "Fotos", folder: "Steuern", rights: { anna: "write" } },
    ]);
  });

  it("asks first to convert a share that does not run per user, names the files, and sends again with convert", async () => {
    vi.useFakeTimers();
    let posts = 0;
    const done = jobDone("Steuern", ["convert", "acl", "config", "acl"]);
    const r = renderDialog({
      share: share(),
      answer: folders("Steuern"),
      routes: {
        ...done,
        "POST /jobs": (body) =>
          posts++ === 0
            ? apiError(409, "needs_convert", 'the share "Fotos" has to be converted', {
                share: "Fotos",
                files: 1240,
              })
            : (done["POST /jobs"] as Handler)(body),
      },
    });
    await settle();
    r.pick("oma", "kein Zugriff");
    fireEvent.click(r.button("Anwenden"));
    await settle();
    const dialog = within(r.dialog());
    expect(
      dialog.getByText(
        "Die vorhandenen Dateien dieser Freigabe werden für die Gruppe lesbar und beschreibbar gemacht, Besitzer und Inhalt bleiben.",
      ),
    ).toBeTruthy();
    expect(dialog.getByText("Das betrifft 1.240 Dateien und Ordner.")).toBeTruthy();
    expect(dialog.queryByRole("alert")).toBeNull();
    expect(dialog.queryByRole("button", { name: "Anwenden" })).toBeNull();
    fireEvent.click(r.button("Umstellen und anwenden"));
    await settle();
    expect(r.jobs()).toEqual([
      { kind: "folder-rights", name: "Fotos", folder: "Steuern", rights: { anna: "write", ben: "write" } },
      {
        kind: "folder-rights",
        name: "Fotos",
        folder: "Steuern",
        rights: { anna: "write", ben: "write" },
        convert: true,
      },
    ]);
    await vi.advanceTimersByTimeAsync(1000);
    expect(r.onDone).toHaveBeenCalledWith(
      "Fotos ist auf Ordner-Rechte umgestellt; Rechte für „Fotos/Steuern“ gesetzt.",
    );
  });

  it("says more than the count only when the server says that it stopped counting", async () => {
    for (const [capped, text] of [
      [true, "Das betrifft mehr als 10.000 Dateien und Ordner."],
      [false, "Das betrifft 10.000 Dateien und Ordner."],
    ] as const) {
      const r = renderDialog({
        share: share(),
        answer: folders("Steuern"),
        routes: {
          "POST /jobs": () =>
            apiError(409, "needs_convert", "converted first", { share: "Fotos", files: 10000, capped }),
        },
      });
      await waitFor(() => expect(within(r.dialog()).queryAllByRole("radiogroup")).toHaveLength(3));
      r.pick("oma", "kein Zugriff");
      fireEvent.click(r.button("Anwenden"));
      expect(await within(r.dialog()).findByText(text)).toBeTruthy();
      cleanup();
    }
  });

  it("shows how far the job is, cancels it, and then says that what was done stays", async () => {
    vi.useFakeTimers();
    let cancelled = false;
    const running = job({
      id: "J9",
      kind: "folder-rights",
      target: "Fotos/Steuern",
      steps: STEPS,
      step: 2,
      progress: { label: "acl", percent: 40 },
      cancellable: true,
    });
    const ended: JobState = {
      ...running,
      status: "failed",
      code: "cancelled",
      error: "acl: cancelled",
      progress: undefined,
      cancellable: false,
    };
    const r = renderDialog({
      routes: {
        "POST /jobs": () => json(running, 202),
        "GET /jobs/J9": () => json(cancelled ? ended : running),
        "POST /jobs/J9/cancel": () => {
          cancelled = true;
          return noContent();
        },
      },
    });
    await settle();
    r.pick("ben", "kein Zugriff");
    fireEvent.click(r.button("Anwenden"));
    await settle();
    const dialog = within(r.dialog());
    expect(dialog.getByText("Schritt 3/4: Ordner-Rechte schreiben")).toBeTruthy();
    expect((dialog.getByRole("progressbar") as HTMLProgressElement).value).toBe(40);
    expect(dialog.getByText("40 %")).toBeTruthy();
    fireEvent.click(r.button("Vorgang abbrechen"));
    await settle();
    expect(r.api.sent("POST", "/jobs/J9/cancel")).toHaveLength(1);
    expect(dialog.getByText("Wird abgebrochen …")).toBeTruthy();
    expect(r.button("Vorgang abbrechen").disabled).toBe(true);
    await vi.advanceTimersByTimeAsync(1000);
    expect(dialog.getByRole("alert").textContent).toBe(
      "Der Vorgang wurde bei Schritt 3/4 (Ordner-Rechte schreiben) abgebrochen. Schon erledigte Schritte bleiben bestehen; prüfe den Stand.",
    );
    expect(r.button("Schließen")).toBeTruthy();
    expect(dialog.queryByRole("button", { name: "Anwenden" })).toBeNull();
    expect(dialog.queryByRole("button", { name: "Vorgang abbrechen" })).toBeNull();
    expect(r.onEnded).toHaveBeenCalled();
    expect(r.onReload).toHaveBeenCalled();
    expect(r.onDone).not.toHaveBeenCalled();
  });

  it("says when the job cannot be cancelled now, and lets the cancel be tried again", async () => {
    vi.useFakeTimers();
    const running = job({
      id: "J9",
      kind: "folder-rights",
      target: "Fotos/Steuern",
      steps: STEPS,
      step: 2,
      cancellable: true,
    });
    const r = renderDialog({
      routes: {
        "POST /jobs": () => json(running, 202),
        "GET /jobs/J9": () => json(running),
        "POST /jobs/J9/cancel": () => apiError(409, "not_cancellable", "the job cannot be cancelled now"),
      },
    });
    await settle();
    r.pick("ben", "kein Zugriff");
    fireEvent.click(r.button("Anwenden"));
    await settle();
    fireEvent.click(r.button("Vorgang abbrechen"));
    await settle();
    expect(
      within(r.dialog()).getByText(
        "Das lässt sich gerade nicht abbrechen – der Vorgang ist in einem Schritt, den man nicht abbrechen kann, oder schon zu Ende.",
      ),
    ).toBeTruthy();
    expect(r.button("Vorgang abbrechen").disabled).toBe(false);
  });

  it("cannot cancel a step that cannot be cancelled", async () => {
    vi.useFakeTimers();
    const running = job({ id: "J9", kind: "folder-rights", target: "Fotos/Steuern", steps: STEPS, step: 0 });
    const r = renderDialog({
      routes: { "POST /jobs": () => json(running, 202), "GET /jobs/J9": () => json(running) },
    });
    await settle();
    r.pick("ben", "kein Zugriff");
    fireEvent.click(r.button("Anwenden"));
    await settle();
    expect(r.button("Vorgang abbrechen").disabled).toBe(true);
  });

  it("takes the rights from above again for a folder with rights of its own", async () => {
    vi.useFakeTimers();
    const r = renderDialog({ routes: jobDone() });
    await settle();
    fireEvent.click(r.button("Rechte der Freigabe übernehmen"));
    await settle();
    expect(r.jobs()).toEqual([{ kind: "folder-rights", name: "Fotos", folder: "Steuern", inherit: true }]);
    await vi.advanceTimersByTimeAsync(1000);
    expect(r.onDone).toHaveBeenCalledWith("„Fotos/Steuern“ hat wieder die Rechte von oben.");
  });

  it("has nothing to take back for a folder without rights of its own, and waits for a change", async () => {
    const r = renderDialog({ answer: folders("Steuern") });
    await waitFor(() => expect(within(r.dialog()).queryAllByRole("radiogroup")).toHaveLength(3));
    expect(r.dialog().textContent).toContain("wie die Freigabe");
    expect(within(r.dialog()).queryByRole("button", { name: "Rechte der Freigabe übernehmen" })).toBeNull();
    expect(r.button("Anwenden").disabled).toBe(true);
    r.pick("oma", "kein Zugriff");
    expect(r.button("Anwenden").disabled).toBe(false);
  });

  it("offers Erneut anwenden for a mixed state, which sends the rights of the folder again", async () => {
    vi.useFakeTimers();
    const r = renderDialog({
      answer: steuern({ disk: { rights: { anna: "write" }, mixed: true, partial: false } }),
      routes: jobDone(),
    });
    await settle();
    expect(r.dialog().textContent).toContain(
      "Gemischter Stand: Nicht alle Dateien in diesem Ordner haben die Rechte, die hier stehen – etwa nach einem Abbruch.",
    );
    r.pick("ben", "kein Zugriff");
    expect(r.button("Anwenden")).toBeTruthy();
    r.pick("ben", "lesen");
    fireEvent.click(r.button("Erneut anwenden"));
    await settle();
    expect(r.jobs()).toEqual([
      { kind: "folder-rights", name: "Fotos", folder: "Steuern", rights: { anna: "write", ben: "read" } },
    ]);
  });

  it("applies again the folder that a mixed folder inherits from, and its own rights once they are changed", async () => {
    vi.useFakeTimers();
    const answer = folders("Steuern/2025", [], {
      from: "Steuern",
      rights: { anna: "write", ben: "read" },
      max: { anna: "write", ben: "read" },
      disk: { rights: {}, mixed: true, partial: false },
    });
    const r = renderDialog({ folder: "Steuern/2025", answer, routes: jobDone("Steuern") });
    await settle();
    expect(r.dialog().textContent).toContain("erbt von Steuern");
    fireEvent.click(r.button("Erneut anwenden"));
    await settle();
    expect(r.jobs()).toEqual([
      { kind: "folder-rights", name: "Fotos", folder: "Steuern", rights: { anna: "write", ben: "read" } },
    ]);
    cleanup();
    const changed = renderDialog({ folder: "Steuern/2025", answer, routes: jobDone("Steuern/2025") });
    await settle();
    changed.pick("ben", "kein Zugriff");
    fireEvent.click(changed.button("Anwenden"));
    await settle();
    expect(changed.jobs()).toEqual([
      { kind: "folder-rights", name: "Fotos", folder: "Steuern/2025", rights: { anna: "write" } },
    ]);
  });

  it("applies the share's rights again from its own folder for a mixed folder whose rights come from the share", async () => {
    vi.useFakeTimers();
    const r = renderDialog({
      answer: folders("Steuern", [], { disk: { rights: {}, mixed: true, partial: false } }),
      routes: jobDone(),
    });
    await settle();
    expect(r.dialog().textContent).toContain("wie die Freigabe");
    expect(r.dialog().textContent).toContain(
      "Gemischter Stand: Nicht alle Dateien in diesem Ordner haben die Rechte, die hier stehen – etwa nach einem Abbruch.",
    );
    fireEvent.click(r.button("Erneut anwenden"));
    await settle();
    expect(r.jobs()).toEqual([{ kind: "folder-rights", name: "Fotos", folder: "" }]);
    await vi.advanceTimersByTimeAsync(1000);
    expect(r.onDone).toHaveBeenCalledWith("Rechte für „Fotos“ gesetzt.");
  });

  it("shows the share's own folder with the share's rights, which only a mixed state applies again", async () => {
    vi.useFakeTimers();
    const root = (mixed: boolean) =>
      folders("", ["Steuern"], { disk: { rights: {}, mixed, partial: false } });
    const r = renderDialog({ folder: "", answer: root(true), routes: jobDone("") });
    await settle();
    expect(r.dialog().textContent).toContain(
      "Hier stehen die Rechte der Freigabe; ändern lassen sie sich beim Bearbeiten der Freigabe.",
    );
    expect(r.dialog().textContent).not.toContain("wie die Freigabe");
    const radios = within(r.dialog()).getAllByRole("radio") as HTMLInputElement[];
    expect(radios.length).toBeGreaterThan(0);
    expect(radios.every((o) => o.disabled)).toBe(true);
    expect(within(r.dialog()).queryByRole("button", { name: "Rechte der Freigabe übernehmen" })).toBeNull();
    fireEvent.click(r.button("Erneut anwenden"));
    await settle();
    expect(r.jobs()).toEqual([{ kind: "folder-rights", name: "Fotos", folder: "" }]);
    cleanup();
    const whole = renderDialog({ folder: "", answer: root(false) });
    await settle();
    expect(whole.button("Anwenden").disabled).toBe(true);
  });

  it("says that a large folder was looked at in part", async () => {
    const r = renderDialog({ answer: steuern({ disk: { rights: {}, mixed: false, partial: true } }) });
    expect(
      await within(r.dialog()).findByText("Der Ordner ist groß: geprüft wurde nur ein Teil davon."),
    ).toBeTruthy();
  });

  it("is over when the disk cannot keep folder rights, and tells the page why", async () => {
    const r = renderDialog({
      routes: {
        "POST /jobs": () =>
          apiError(409, "no_acl", "the disk cannot keep folder rights", { reason: "no_posix_acl" }),
      },
    });
    await waitFor(() => expect(within(r.dialog()).queryAllByRole("radiogroup")).toHaveLength(3));
    r.pick("ben", "kein Zugriff");
    fireEvent.click(r.button("Anwenden"));
    expect((await within(r.dialog()).findByRole("alert")).textContent).toBe(
      "Die Platte dieser Freigabe kann keine Ordner-Rechte halten: Das Dateisystem hat keine POSIX-ACLs.",
    );
    expect(r.onNoAcl).toHaveBeenCalledWith("no_posix_acl");
    expect(within(r.dialog()).queryByRole("button", { name: "Anwenden" })).toBeNull();
    expect(r.button("Schließen")).toBeTruthy();
  });

  it("keeps a changed right, its rows and the focus when the view is read again, and reads nothing", async () => {
    const r = renderDialog();
    await waitFor(() => expect(within(r.dialog()).queryAllByRole("radiogroup")).toHaveLength(3));
    r.pick("ben", "kein Zugriff");
    const radio = within(r.group("oma")).getByRole("radio", { name: "lesen" }) as HTMLInputElement;
    const rows = within(r.dialog()).getAllByRole("row");
    radio.focus();
    r.setV(view());
    r.setV(view());
    await new Promise((resolve) => setTimeout(resolve, 20));
    expect(r.reads()).toBe(1);
    expect(radio.isConnected).toBe(true);
    expect(document.activeElement).toBe(radio);
    expect(
      within(r.dialog())
        .getAllByRole("row")
        .every((tr, i) => tr === rows[i]),
    ).toBe(true);
    expect(
      (within(r.group("ben")).getByRole("radio", { name: "kein Zugriff" }) as HTMLInputElement).checked,
    ).toBe(true);
  });

  it("waits while another change runs, and closes when the share is gone", async () => {
    const r = renderDialog({ answer: steuern(), busy: true });
    await waitFor(() => expect(within(r.dialog()).queryAllByRole("radiogroup")).toHaveLength(3));
    r.pick("ben", "kein Zugriff");
    expect(r.button("Anwenden").disabled).toBe(true);
    expect(
      within(r.dialog()).getByText("Gerade läuft eine andere Änderung – warte, bis sie fertig ist."),
    ).toBeTruthy();
    r.setV(view({ config: { ...view().config, shares: shares().slice(1) } }));
    expect(r.onClose).toHaveBeenCalled();
  });

  it("says why the folder cannot be read", async () => {
    const r = renderDialog({
      routes: {
        [foldersRoute("Fotos", "Steuern")]: () =>
          apiError(404, "not_found", "Steuern is not there", { dir: "Steuern" }),
      },
    });
    expect(await within(r.dialog()).findByText("Das gibt es nicht mehr – lade neu.")).toBeTruthy();
    expect(within(r.dialog()).queryByRole("button", { name: "Anwenden" })).toBeNull();
  });

  it("changes nothing while the Samba users cannot be read", async () => {
    const r = renderDialog({ v: view({ users: [], usersKnown: false }) });
    expect(
      await within(r.dialog()).findByText(
        "Die Samba-Nutzer lassen sich gerade nicht lesen – lade neu, bevor du die Rechte änderst.",
      ),
    ).toBeTruthy();
    expect(r.button("Anwenden").disabled).toBe(true);
  });
});
