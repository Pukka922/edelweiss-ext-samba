import { cleanup, fireEvent, waitFor, within } from "@solidjs/testing-library";
import { createSignal } from "solid-js";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { ShareView, ViewResponse } from "../api";
import { browse, browseRoute, share, shares, view } from "../test/fixtures";
import { apiError, type Handler, jobRoutes, json, renderExt, settle, stubApi, type } from "../test/render";
import { ShareDialog } from "./ShareDialog";

afterEach(() => {
  cleanup();
  vi.useRealTimers();
  vi.unstubAllGlobals();
});

/** The routes of the folders that the dialog reads the owner of. */
const folders = (): Record<string, Handler> => ({
  [browseRoute("/mnt/daten/Fotos")]: () => json(browse("/mnt/daten/Fotos", ["2024"])),
  [browseRoute("/mnt/daten")]: () => json(browse("/mnt/daten", ["Filme", "Fotos"])),
  [browseRoute("/mnt/daten/root")]: () =>
    json(browse("/mnt/daten/root", [], { owner: { uid: 0, gid: 0, user: "root", group: "root" } })),
});

const NAME = "Name (so heißt sie im Netzwerk)";

function renderDialog(
  opts: {
    share?: ShareView;
    v?: ViewResponse;
    routes?: Record<string, Handler>;
    job?: { states: Parameters<typeof jobRoutes>[3] };
    busy?: boolean;
  } = {},
) {
  const routes = { ...folders(), ...opts.routes };
  const api = opts.job
    ? jobRoutes(
        opts.share ? "share-edit" : "share-add",
        "Fotos",
        ["prepare", "check", "config"],
        opts.job.states,
        routes,
      )
    : stubApi(routes);
  const [v, setV] = createSignal(opts.v ?? view());
  const onClose = vi.fn();
  const onDone = vi.fn();
  const onChanged = vi.fn();
  const onReload = vi.fn();
  const r = renderExt(
    () => (
      <ShareDialog
        share={opts.share}
        view={v}
        busy={() => opts.busy ?? false}
        onClose={onClose}
        onDone={onDone}
        onChanged={onChanged}
        onReload={onReload}
      />
    ),
    undefined,
    "de",
  );
  const title = opts.share ? `Freigabe bearbeiten: ${opts.share.name}` : "Freigabe anlegen";
  const dialog = () => r.getByRole("dialog", { name: title });
  const field = (label: string) => within(dialog()).getByLabelText(label) as HTMLInputElement;
  const button = (name: string) => within(dialog()).getByRole("button", { name }) as HTMLButtonElement;
  /** The text that a field points to as its description: its error. */
  const errorOf = (el: HTMLElement) => {
    const id = el.getAttribute("aria-describedby");
    return id ? document.getElementById(id)?.textContent : undefined;
  };
  const pick = (user: string, label: string) =>
    fireEvent.click(
      within(within(dialog()).getByRole("radiogroup", { name: user })).getByRole("radio", { name: label }),
    );
  const jobs = () => api.sent("POST", "/jobs").map((c) => c.body);
  return {
    ...r,
    api,
    setV,
    onClose,
    onDone,
    onChanged,
    onReload,
    dialog,
    field,
    button,
    errorOf,
    pick,
    jobs,
  };
}

describe("ShareDialog", () => {
  it("sends the share-add body, and tells the page when the job is done", async () => {
    vi.useFakeTimers();
    const r = renderDialog({ job: { states: [{ status: "done", step: 2 }] } });
    type(r.field(NAME), " Fotos ");
    type(r.field("Ordner"), "/mnt/daten/Fotos");
    type(r.field("Beschreibung (optional)"), "Familienfotos");
    r.pick("anna", "lesen + schreiben");
    r.pick("oma", "lesen");
    fireEvent.click(within(r.dialog()).getByRole("checkbox", { name: /^versteckt/ }));
    fireEvent.click(within(r.dialog()).getByRole("checkbox", { name: /^Papierkorb/ }));
    type(r.field("Tage im Papierkorb"), "14");
    await vi.advanceTimersByTimeAsync(300);
    fireEvent.click(r.button("Anlegen"));
    await settle();
    expect(r.jobs()).toEqual([
      {
        kind: "share-add",
        share: {
          name: "Fotos",
          path: "/mnt/daten/Fotos",
          comment: "Familienfotos",
          rights: { anna: "write", oma: "read" },
          hidden: true,
          recycle: true,
          recycleDays: 14,
        },
      },
    ]);
    expect(r.getByText("Schritt 1/3: Dateien vorbereiten")).toBeTruthy();
    await vi.advanceTimersByTimeAsync(1000);
    expect(r.onDone).toHaveBeenCalledWith("Freigabe Fotos angelegt.");
  });

  it("starts an edit from the share, and sends share-edit with the name it had", async () => {
    vi.useFakeTimers();
    const r = renderDialog({ share: share(), job: { states: [{ status: "done" }] } });
    expect(r.field(NAME).value).toBe("Fotos");
    expect(r.field("Ordner").value).toBe("/mnt/daten/Fotos");
    expect(r.field("Beschreibung (optional)").value).toBe("Familienfotos");
    expect(r.field("Tage im Papierkorb").value).toBe("30");
    type(r.field(NAME), "Bilder");
    await vi.advanceTimersByTimeAsync(300);
    fireEvent.click(r.button("Speichern"));
    await settle();
    expect(r.jobs()).toEqual([
      {
        kind: "share-edit",
        name: "Fotos",
        share: {
          name: "Bilder",
          path: "/mnt/daten/Fotos",
          comment: "Familienfotos",
          rights: { anna: "write", ben: "write", oma: "read" },
          hidden: false,
          recycle: true,
          recycleDays: 30,
        },
      },
    ]);
    await vi.advanceTimersByTimeAsync(1000);
    expect(r.onDone).toHaveBeenCalledWith("Freigabe Bilder gespeichert.");
  });

  it("sends no right of a user who is no Samba user any more", async () => {
    vi.useFakeTimers();
    const r = renderDialog({
      share: share({ rights: { anna: "write", gone: "read" } }),
      job: { states: [{ status: "done" }] },
    });
    await vi.advanceTimersByTimeAsync(300);
    fireEvent.click(r.button("Speichern"));
    await settle();
    expect((r.jobs()[0] as { share: { rights: unknown } }).share.rights).toEqual({ anna: "write" });
  });

  it("names the owner of the folder, whom Samba accesses the files as", async () => {
    const r = renderDialog({ share: share() });
    expect(
      await r.findByText(
        "Samba greift als Besitzer des Ordners (ben:users) auf die Dateien zu; an bestehenden Dateien ändert sich nichts.",
      ),
    ).toBeTruthy();
  });

  it("warns of a folder of root, and does not send it", async () => {
    const r = renderDialog();
    type(r.field(NAME), "Root");
    type(r.field("Ordner"), "/mnt/daten/root");
    expect(
      await r.findByText(
        "Der Ordner gehört root. Wähle einen eigenen Ordner oder lege mit „Neuer Ordner“ einen an.",
      ),
    ).toBeTruthy();
    expect(r.button("Anlegen").disabled).toBe(true);
  });

  it("puts the days of the recycle bin at 30 when it is turned on, and takes 1 to 3650 only", async () => {
    const r = renderDialog();
    type(r.field(NAME), "Fotos");
    type(r.field("Ordner"), "/mnt/daten/Fotos");
    expect(within(r.dialog()).queryByLabelText("Tage im Papierkorb")).toBeNull();
    fireEvent.click(within(r.dialog()).getByRole("checkbox", { name: /^Papierkorb/ }));
    const days = r.field("Tage im Papierkorb");
    expect(days.value).toBe("30");
    await waitFor(() => expect(r.button("Anlegen").disabled).toBe(false));
    for (const bad of ["0", "3651", "", "2.5", "-1"]) {
      type(days, bad);
      expect(r.button("Anlegen").disabled, bad).toBe(true);
      expect(r.errorOf(days), bad).toBe("1 bis 3650 Tage");
    }
    for (const good of ["1", "3650"]) {
      type(days, good);
      expect(r.button("Anlegen").disabled, good).toBe(false);
    }
  });

  it("shows a refusal with a reason at its field, and forgets it when the field changes", async () => {
    vi.useFakeTimers();
    const r = renderDialog({
      share: share({ perUser: true }),
      routes: {
        "POST /jobs": () =>
          apiError(400, "invalid_input", "the folder is on another disk now", {
            field: "path",
            reason: "per_user_disk_changed",
          }),
      },
    });
    await vi.advanceTimersByTimeAsync(300);
    fireEvent.click(r.button("Speichern"));
    await settle();
    expect(r.errorOf(r.field("Ordner"))).toBe(
      "Diese Freigabe nutzt Ordner-Rechte und liegt jetzt auf einem anderen Datenträger. Entferne sie und lege sie neu an.",
    );
    expect(r.field("Ordner").getAttribute("aria-invalid")).toBe("true");
    expect(within(r.dialog()).queryByRole("alert")).toBeNull();
    // the dialog is not over: the request can be sent again
    expect(r.button("Speichern").disabled).toBe(false);
  });

  it("shows the refusals of the name, the days and the folder at their fields", async () => {
    vi.useFakeTimers();
    const answers = [
      apiError(409, "exists", 'there is a share "fotos" already', { field: "name" }),
      apiError(400, "invalid_input", "the recycle bin keeps files 1 to 3650 days", { field: "recycleDays" }),
      apiError(409, "in_use", "inside another share", { shares: ["Medien"] }),
      apiError(409, "no_owner", "no account", { uid: 1001, gid: 1001 }),
      apiError(404, "not_found", "the folder is not there", { path: "/mnt/daten/neu" }),
    ];
    let n = 0;
    const r = renderDialog({
      share: share(),
      routes: { "POST /jobs": () => answers[n++] as Response },
    });
    await vi.advanceTimersByTimeAsync(300);
    const send = async () => {
      fireEvent.click(r.button("Speichern"));
      await settle();
    };
    await send();
    expect(r.errorOf(r.field(NAME))).toBe("Es gibt schon eine Freigabe mit diesem Namen.");
    type(r.field(NAME), "Fotos2");
    expect(r.errorOf(r.field(NAME))).toBeUndefined();
    await send();
    expect(r.errorOf(r.field("Tage im Papierkorb"))).toBe("Der Papierkorb hält Dateien 1 bis 3650 Tage.");
    await send();
    expect(r.errorOf(r.field("Ordner"))).toBe(
      "Der Ordner liegt in einer anderen Freigabe oder enthält eine: Medien.",
    );
    await send();
    expect(r.errorOf(r.field("Ordner"))).toBe(
      "Der Besitzer 1001:1001 des Ordners hat kein Konto auf diesem Host – lege ein Dienstkonto an.",
    );
    await send();
    expect(r.errorOf(r.field("Ordner"))).toBe("Den Ordner gibt es nicht.");
    expect(r.onReload).not.toHaveBeenCalled();
  });

  it("shows a refusal without a field below the form", async () => {
    vi.useFakeTimers();
    const r = renderDialog({
      share: share(),
      routes: { "POST /jobs": () => apiError(409, "busy", "another change is running") },
    });
    await vi.advanceTimersByTimeAsync(300);
    fireEvent.click(r.button("Speichern"));
    await settle();
    expect(within(r.dialog()).getByRole("alert").textContent).toBe(
      "Gerade läuft schon eine andere Änderung – versuch es gleich noch einmal.",
    );
    expect(r.button("Speichern").disabled).toBe(false);
  });

  it("is over when the share is gone, or the job ended without success: Close alone, and the view is read again", async () => {
    vi.useFakeTimers();
    const r = renderDialog({
      share: share(),
      job: { states: [{ status: "failed", code: "failed", step: 1, error: "testparm: exit 1" }] },
    });
    await vi.advanceTimersByTimeAsync(300);
    fireEvent.click(r.button("Speichern"));
    await vi.advanceTimersByTimeAsync(1000);
    expect(within(r.dialog()).getByRole("alert").textContent).toContain(
      "Schritt 2/3 (Konfiguration prüfen (testparm)) ist fehlgeschlagen: testparm: exit 1",
    );
    expect(r.onReload).toHaveBeenCalled();
    expect(within(r.dialog()).queryByRole("button", { name: "Speichern" })).toBeNull();
    fireEvent.click(r.button("Schließen"));
    expect(r.onClose).toHaveBeenCalled();
  });

  it("offers Dienstkonto anlegen when the owner of the folder of a share with folder rights has no user name", async () => {
    const r = renderDialog({
      share: share({ perUser: true }),
      routes: {
        [browseRoute("/mnt/daten/Fotos")]: () =>
          json(browse("/mnt/daten/Fotos", [], { owner: { uid: 1000, gid: 100, user: "", group: "users" } })),
      },
    });
    expect(
      await within(r.dialog()).findByText(
        "Der Ordner gehört UID 1000 und GID 100, für die es auf diesem Host kein Konto gibt (etwa eine Platte von einem anderen Rechner).",
      ),
    ).toBeTruthy();
    expect(r.button("Dienstkonto anlegen")).toBeTruthy();
    expect(r.button("Speichern").disabled).toBe(true);
  });

  it("says Platte fehlt for a share whose disk is not mounted, saves nothing, and reads the folder again when it is back", async () => {
    let back = false;
    const usb = shares()[2] as ShareView;
    const r = renderDialog({
      share: usb,
      routes: {
        [browseRoute("/mnt/usb1/backup")]: () =>
          back
            ? json(browse("/mnt/usb1/backup", []))
            : apiError(400, "invalid_input", "/mnt/usb1/backup is on no data disk", {
                field: "dir",
                reason: "no_data_mount",
              }),
      },
    });
    const reads = () => r.api.sent("GET", `/browse?dir=${encodeURIComponent("/mnt/usb1/backup")}`).length;
    // the folder has been read, and refused, while the disk was away
    await waitFor(() => expect(reads()).toBe(1));
    await new Promise((resolve) => setTimeout(resolve, 20));
    expect(r.errorOf(r.field("Ordner"))).toBe("Platte fehlt");
    expect(r.button("Speichern").disabled).toBe(true);
    back = true;
    r.setV(
      view({
        config: {
          ...view().config,
          shares: [
            shares()[0] as ShareView,
            shares()[1] as ShareView,
            { ...usb, available: true, reason: "" },
          ],
        },
      }),
    );
    expect(
      await within(r.dialog()).findByText(
        "Samba greift als Besitzer des Ordners (ben:users) auf die Dateien zu; an bestehenden Dateien ändert sich nichts.",
      ),
    ).toBeTruthy();
    expect(reads()).toBe(2);
    expect(r.errorOf(r.field("Ordner"))).toBeUndefined();
    expect(r.button("Speichern").disabled).toBe(false);
  });

  it("says Platte fehlt also when the empty mount point can be read, and names no owner then", async () => {
    // the mount point is a folder on another disk: GET /browse reads it, empty, while the disk is away
    const r = renderDialog({
      share: shares()[2] as ShareView,
      routes: { [browseRoute("/mnt/usb1/backup")]: () => json(browse("/mnt/usb1/backup", [])) },
    });
    await waitFor(() =>
      expect(r.api.sent("GET", `/browse?dir=${encodeURIComponent("/mnt/usb1/backup")}`)).toHaveLength(1),
    );
    await new Promise((resolve) => setTimeout(resolve, 20));
    expect(r.errorOf(r.field("Ordner"))).toBe("Platte fehlt");
    expect(r.button("Speichern").disabled).toBe(true);
    expect(within(r.dialog()).queryByText(/Samba greift als Besitzer/)).toBeNull();
  });

  it("saves nothing while the folder is refused with a reason", async () => {
    const r = renderDialog({
      routes: {
        [browseRoute("/mnt/daten/link")]: () =>
          apiError(400, "invalid_input", "/mnt/daten/link is a symbolic link", {
            field: "dir",
            reason: "link",
          }),
      },
    });
    type(r.field(NAME), "Link");
    type(r.field("Ordner"), "/mnt/daten/link");
    await waitFor(() =>
      expect(r.errorOf(r.field("Ordner"))).toBe(
        "Der Ordner ist ein Link oder liegt hinter einem – Links werden nicht verfolgt.",
      ),
    );
    expect(r.button("Anlegen").disabled).toBe(true);
  });

  it("keeps the folder of a share with folder rights", async () => {
    const r = renderDialog({ share: share({ perUser: true }) });
    expect(r.field("Ordner").readOnly).toBe(true);
    expect(within(r.dialog()).queryByRole("button", { name: "Durchsuchen …" })).toBeNull();
    expect(
      within(r.dialog()).getByText("Diese Freigabe nutzt Ordner-Rechte und behält ihren Ordner."),
    ).toBeTruthy();
  });

  it("does not change a share while the Samba users cannot be read", async () => {
    const r = renderDialog({ share: share(), v: view({ users: [], usersKnown: false }) });
    expect(
      within(r.dialog()).getByText(
        "Die Samba-Nutzer lassen sich gerade nicht lesen – lade neu, bevor du die Freigabe änderst.",
      ),
    ).toBeTruthy();
    expect(r.button("Speichern").disabled).toBe(true);
    expect(within(r.dialog()).queryByRole("radiogroup")).toBeNull();
  });

  it("says that there are no Samba users yet, and that nobody has access then", () => {
    const r = renderDialog({ v: view({ users: [] }) });
    expect(
      within(r.dialog()).getByText("Noch keine Samba-Nutzer – lege sie im Reiter „Nutzer“ an."),
    ).toBeTruthy();
    expect(
      within(r.dialog()).getByText(
        "Niemand hat Zugriff: Samba stellt die Freigabe erst bereit, wenn jemand Rechte hat.",
      ),
    ).toBeTruthy();
  });

  it("says that another change runs, and waits for it", () => {
    const r = renderDialog({ share: share(), busy: true });
    expect(
      within(r.dialog()).getByText("Gerade läuft eine andere Änderung – warte, bis sie fertig ist."),
    ).toBeTruthy();
    expect(r.button("Speichern").disabled).toBe(true);
  });

  it("chooses the folder with the navigator", async () => {
    const r = renderDialog();
    fireEvent.click(r.button("Durchsuchen …"));
    const nav = r.getByRole("dialog", { name: "Ordner wählen" });
    fireEvent.click(await within(nav).findByRole("button", { name: "Fotos" }));
    const choose = await within(nav).findByRole("button", { name: "„/mnt/daten/Fotos“ wählen" });
    fireEvent.click(choose);
    expect(r.queryByRole("dialog", { name: "Ordner wählen" })).toBeNull();
    expect(r.field("Ordner").value).toBe("/mnt/daten/Fotos");
  });

  it("makes its own fields and buttons inert behind the navigator, and gives them back after it", async () => {
    const r = renderDialog();
    const own = [r.button("Durchsuchen …"), r.button("Abbrechen"), r.field(NAME)];
    fireEvent.click(r.button("Durchsuchen …"));
    const nav = r.getByRole("dialog", { name: "Ordner wählen" });
    expect(own.every((el) => el.closest("[inert]") !== null)).toBe(true);
    const cancel = within(nav).getByRole("button", { name: "Abbrechen" });
    expect(cancel.closest("[inert]")).toBeNull();
    fireEvent.click(cancel);
    expect(r.queryByRole("dialog", { name: "Ordner wählen" })).toBeNull();
    expect(own.every((el) => el.isConnected && el.closest("[inert]") === null)).toBe(true);
  });

  it("makes its own buttons inert behind the service account", async () => {
    const r = renderDialog({
      share: share({ perUser: true }),
      routes: {
        [browseRoute("/mnt/daten/Fotos")]: () =>
          json(browse("/mnt/daten/Fotos", [], { owner: { uid: 1000, gid: 100, user: "", group: "users" } })),
      },
    });
    const create = await within(r.dialog()).findByRole("button", { name: "Dienstkonto anlegen" });
    const cancel = r.button("Abbrechen");
    fireEvent.click(create);
    const account = r.getByRole("dialog", { name: "Dienstkonto anlegen" });
    expect(cancel.closest("[inert]")).not.toBeNull();
    expect(create.closest("[inert]")).not.toBeNull();
    expect(within(account).getByRole("button", { name: "Anlegen" }).closest("[inert]")).toBeNull();
  });

  it("names the address of a hidden share", () => {
    const r = renderDialog({ share: share() });
    expect(
      within(r.dialog()).getByRole("checkbox", {
        name: "versteckt – taucht für niemanden in der Liste auf, nur über \\\\nas\\Fotos erreichbar",
      }),
    ).toBeTruthy();
  });
});

describe("ShareDialog: a view poll while it is open", () => {
  /** The reads of GET /browse for dir. */
  const reads = (r: ReturnType<typeof renderDialog>, dir: string) =>
    r.api.sent("GET", `/browse?dir=${encodeURIComponent(dir)}`).length;
  const poll = async (r: ReturnType<typeof renderDialog>) => {
    r.setV(view()); // the same view, as new objects
    await new Promise((resolve) => setTimeout(resolve, 20));
  };

  it("keeps the navigator: no folder is read again, and Neuer Ordner keeps its name and its focus", async () => {
    const r = renderDialog();
    fireEvent.click(r.button("Durchsuchen …"));
    const nav = r.getByRole("dialog", { name: "Ordner wählen" });
    await within(nav).findByRole("button", { name: "Fotos" });
    const disk = within(nav).getByRole("button", { name: "/mnt/daten · ext4 · 1,2 TB frei" });
    const folder = within(nav).getByRole("button", { name: "Fotos" });
    fireEvent.click(within(nav).getByRole("button", { name: "Neuer Ordner …" }));
    const name = within(nav).getByLabelText("Name des neuen Ordners") as HTMLInputElement;
    type(name, "2026");
    name.focus();
    const before = reads(r, "/mnt/daten");
    await poll(r);
    await poll(r);
    expect(reads(r, "/mnt/daten")).toBe(before);
    expect(name.isConnected).toBe(true);
    expect(name.value).toBe("2026");
    expect(document.activeElement).toBe(name);
    expect(disk.isConnected).toBe(true);
    expect(folder.isConnected).toBe(true);
  });

  it("keeps the rights table: a focused right stays the same element, with the focus", async () => {
    const r = renderDialog();
    const radio = within(within(r.dialog()).getByRole("radiogroup", { name: "oma" })).getByRole("radio", {
      name: "lesen",
    }) as HTMLInputElement;
    radio.focus();
    await poll(r);
    expect(radio.isConnected).toBe(true);
    expect(document.activeElement).toBe(radio);
  });

  it("does not read the owner of the folder again", async () => {
    const r = renderDialog({ share: share() });
    await r.findByText(/Samba greift als Besitzer des Ordners \(ben:users\)/);
    const before = reads(r, "/mnt/daten/Fotos");
    await poll(r);
    expect(reads(r, "/mnt/daten/Fotos")).toBe(before);
    expect(r.getByText(/Samba greift als Besitzer des Ordners \(ben:users\)/)).toBeTruthy();
  });
});
