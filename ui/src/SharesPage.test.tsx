import { fakeSession } from "@edelweiss/sdk/testing";
import { cleanup, fireEvent, waitFor, within } from "@solidjs/testing-library";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { JobState, ViewResponse } from "./api";
import {
  connections,
  folders,
  foldersRoute,
  items,
  job,
  notSetUp,
  recycleBin,
  recycleRoute,
  share,
  shares,
  view,
} from "./test/fixtures";
import { alertWith, jobHandlers, labels, names, renderPage, row } from "./test/page";
import { adminOnRequest, apiError, json, noContent, settle } from "./test/render";

afterEach(() => {
  cleanup();
  vi.useRealTimers();
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

const RELOAD_MS = 5000;

const tab = (r: ReturnType<typeof renderPage>, name: string) =>
  r.getByRole("tab", { name }) as HTMLButtonElement;

describe("SharesPage", () => {
  describe("the head", () => {
    it("says that Samba runs, its name in the network and the protocol, with Reload and Freigabe anlegen", async () => {
      const r = renderPage();
      expect(r.getByRole("heading", { name: "Freigaben" })).toBeTruthy();
      expect(await r.findByText("Samba läuft · im Netzwerk als \\\\nas · nur SMB3")).toBeTruthy();
      expect(r.getByRole("button", { name: "Neu laden" })).toBeTruthy();
      expect(r.getByRole("button", { name: "Freigabe anlegen" })).toBeTruthy();
    });

    it("says that Samba is stopped, takes the host name when the settings have none, and speaks English", async () => {
      const v = view();
      v.config.settings.name = "";
      v.config.settings.smb2 = true;
      v.services.samba = false;
      const r = renderPage({ v, locale: "en" });
      expect(r.getByRole("heading", { name: "Shares" })).toBeTruthy();
      expect(
        await r.findByText("Samba is stopped · on the network as \\\\alpine · SMB2 and SMB3"),
      ).toBeTruthy();
    });

    it("shows that it loads, until the view is there", async () => {
      vi.useFakeTimers();
      const r = renderPage();
      expect(r.getByRole("status").textContent).toContain("Lade …");
      await settle();
      expect(r.queryByText("Lade …")).toBeNull();
      expect(r.getByRole("table")).toBeTruthy();
    });
  });

  describe("the tabs", () => {
    it("has Freigaben, Nutzer, Verbindungen and Einstellungen, with the shares open", async () => {
      const r = renderPage();
      await r.findByRole("table");
      expect(r.getAllByRole("tab").map((t) => t.textContent)).toEqual([
        "Freigaben",
        "Nutzer",
        "Verbindungen",
        "Einstellungen",
      ]);
      expect(tab(r, "Freigaben").getAttribute("aria-selected")).toBe("true");
      fireEvent.click(tab(r, "Einstellungen"));
      expect(tab(r, "Einstellungen").getAttribute("aria-selected")).toBe("true");
      expect(r.queryByRole("columnheader", { name: "Papierkorb" })).toBeNull();
      expect(r.getByLabelText("Name im Netzwerk")).toBeTruthy();
      fireEvent.click(tab(r, "Nutzer"));
      expect(r.getByRole("columnheader", { name: "Art" })).toBeTruthy();
      expect(r.queryByLabelText("Name im Netzwerk")).toBeNull();
      fireEvent.click(tab(r, "Freigaben"));
      expect(r.getByRole("table")).toBeTruthy();
    });
  });

  describe("the list", () => {
    it("shows the shares of the view, with the disk that is missing", async () => {
      const r = renderPage();
      await r.findByRole("table");
      expect(names(r)).toEqual(["Fotos", "Filme", "Backup-USB"]);
      expect(row(r, "Fotos").textContent).toContain("1,2 GB · 340 Dateien");
      expect(row(r, "Backup-USB").textContent).toContain("Platte fehlt");
      expect(alertWith(r, "Die Platte von „Backup-USB“ ist nicht eingehängt")).toBeTruthy();
    });

    it("takes the rights of the shares from the config, and makes no claim about the Samba users it cannot read", async () => {
      const r = renderPage({
        v: view({ users: [], usersKnown: false }),
        session: fakeSession({ admin: false }),
      });
      await r.findByRole("table");
      expect(row(r, "Fotos").textContent).toContain("anna ✎");
      expect(row(r, "Fotos").textContent).toContain("oma 👁");
      expect(r.queryByText(/nur Samba|Host-Benutzer|niemand hat Zugriff|Noch keine Samba-Nutzer/)).toBeNull();
    });
  });

  describe("orphaned names", () => {
    /** bob and eve were deleted in edelweiss-ext-users: Fotos still names them, and they have no Samba entry. */
    const orphaned = () =>
      view({
        config: {
          ...view().config,
          shares: shares().map((s) =>
            s.name === "Fotos" ? { ...s, rights: { ...s.rights, bob: "read", eve: "write" } } : s,
          ),
        },
      });

    it("marks the names without a Samba entry as deleted, and removes them, one job each, on a click alone", async () => {
      vi.useFakeTimers();
      let n = 0;
      const drop = (body: unknown) => {
        n++;
        return json(
          job({ id: `J${n}`, kind: "user-drop", target: (body as { user: string }).user, steps: ["config"] }),
          202,
        );
      };
      const done = (id: string, user: string) => () =>
        json(job({ id, kind: "user-drop", target: user, steps: ["config"], status: "done" }));
      const r = renderPage({
        v: orphaned,
        routes: { "POST /jobs": drop, "GET /jobs/J1": done("J1", "bob"), "GET /jobs/J2": done("J2", "eve") },
      });
      await settle();
      expect(within(row(r, "Fotos")).getByText(/^bob/).textContent).toBe("bob · gelöscht");
      expect(within(row(r, "Fotos")).getByText(/^eve/).textContent).toBe("eve · gelöscht");
      expect(row(r, "Fotos").textContent).toContain("anna ✎");
      const alert = alertWith(r, "keinen Samba-Eintrag mehr") as HTMLElement;
      expect(alert.textContent).toContain("bob und eve");
      // the page starts no job by itself
      await vi.advanceTimersByTimeAsync(6000);
      expect(r.api.sent("POST", "/jobs")).toEqual([]);
      fireEvent.click(within(alert).getByRole("button", { name: "Verwaiste Namen entfernen" }));
      await vi.advanceTimersByTimeAsync(3000);
      expect(r.api.sent("POST", "/jobs").map((c) => c.body)).toEqual([
        { kind: "user-drop", user: "bob" },
        { kind: "user-drop", user: "eve" },
      ]);
      expect(r.toasts.shown).toEqual([
        { kind: "success", message: "bob und eve aus allen Freigaben entfernt." },
      ]);
    });

    it("marks nothing and offers nothing without admin mode, or while the Samba users are not known", async () => {
      const r = renderPage({ v: orphaned, session: fakeSession({ admin: false, canAdmin: true }) });
      await r.findByRole("table");
      expect(row(r, "Fotos").textContent).toContain("bob 👁");
      expect(r.queryByText(/gelöscht/)).toBeNull();
      expect(alertWith(r, "keinen Samba-Eintrag mehr")).toBeUndefined();
      cleanup();
      const known = renderPage({ v: () => ({ ...orphaned(), usersKnown: false, users: [] }) });
      await known.findByRole("table");
      expect(known.queryByText(/gelöscht/)).toBeNull();
    });
  });

  describe("polling", () => {
    it("reads the view again every 5 seconds while the page is visible", async () => {
      vi.useFakeTimers();
      const visibility = vi.spyOn(document, "visibilityState", "get").mockReturnValue("visible");
      const r = renderPage();
      await vi.advanceTimersByTimeAsync(RELOAD_MS - 10);
      expect(r.api.sent("GET", "/view")).toHaveLength(1);
      await vi.advanceTimersByTimeAsync(10);
      expect(r.api.sent("GET", "/view")).toHaveLength(2);
      visibility.mockReturnValue("hidden");
      await vi.advanceTimersByTimeAsync(3 * RELOAD_MS);
      expect(r.api.sent("GET", "/view")).toHaveLength(2);
    });

    it("keeps its rows, its buttons and the focus when the view is read again", async () => {
      vi.useFakeTimers();
      const r = renderPage();
      await settle();
      const rows = r.getAllByRole("row");
      const edit = r.getByRole("button", { name: "Bearbeiten Filme" });
      edit.focus();
      await vi.advanceTimersByTimeAsync(RELOAD_MS);
      expect(r.api.sent("GET", "/view")).toHaveLength(2);
      expect(edit.isConnected).toBe(true);
      expect(document.activeElement).toBe(edit);
      expect(r.getAllByRole("row").every((tr, i) => tr === rows[i])).toBe(true);
    });

    it("reads it again on Reload", async () => {
      const r = renderPage();
      await r.findByRole("table");
      fireEvent.click(r.getByRole("button", { name: "Neu laden" }));
      await waitFor(() => expect(r.api.sent("GET", "/view")).toHaveLength(2));
    });
  });

  describe("not set up", () => {
    it("shows the setup in place of the tabs, and runs it as a job", async () => {
      vi.useFakeTimers();
      let ready = false;
      const r = renderPage({
        v: () => (ready ? view() : notSetUp()),
        routes: jobHandlers(
          "setup",
          "samba",
          ["backup", "config", "service", "lbu"],
          [{ status: "done", step: 3 }],
        ),
      });
      await settle();
      expect(r.queryByRole("tablist")).toBeNull();
      expect(r.getByText("Samba ist noch nicht eingerichtet")).toBeTruthy();
      expect(r.queryByRole("button", { name: "Freigabe anlegen" })).toBeNull();
      fireEvent.click(r.getByRole("button", { name: "Samba einrichten" }));
      await settle();
      expect(r.api.sent("POST", "/jobs").map((c) => c.body)).toEqual([{ kind: "setup" }]);
      expect(r.getByText("Schritt 1/4: Altes Samba sichern (.edelweiss-bak)")).toBeTruthy();
      ready = true;
      await vi.advanceTimersByTimeAsync(1000);
      expect(r.toasts.shown).toEqual([{ kind: "success", message: "Samba ist eingerichtet." }]);
      await settle();
      expect(r.getByRole("tablist")).toBeTruthy();
    });

    it("names the Samba that is there before the setup", async () => {
      const r = renderPage({ v: notSetUp({ shares: ["public", "scans"], users: ["root", "scanner"] }) });
      expect(
        await r.findByText(
          "Es gibt schon Samba: /etc/samba/smb.conf mit den Freigaben „public“ und „scans“ und 2 Samba-Nutzer (root, scanner). Konfiguration und Nutzerdatenbank werden mit .edelweiss-bak gesichert; die Seite fängt leer an und übernimmt nichts.",
        ),
      ).toBeTruthy();
    });

    it("asks for admin mode first, and shows a Samba that admin mode reveals before it sets up anything", async () => {
      const session = adminOnRequest();
      let admin = false;
      const foreign = { shares: ["public"], users: [] };
      const r = renderPage({
        session,
        v: () => (admin ? notSetUp(foreign) : notSetUp()),
        routes: jobHandlers("setup", "samba", ["backup"], [{ status: "done" }]),
      });
      fireEvent.click(await r.findByRole("button", { name: "Samba einrichten" }));
      admin = true;
      expect(
        await r.findByText(/Es gibt schon Samba: \/etc\/samba\/smb.conf mit der Freigabe „public“\./),
      ).toBeTruthy();
      await new Promise((resolve) => setTimeout(resolve, 20));
      expect(r.api.sent("POST", "/jobs")).toEqual([]);
      fireEvent.click(r.getByRole("button", { name: "Samba einrichten" }));
      await waitFor(() => expect(r.api.sent("POST", "/jobs")).toHaveLength(1));
    });

    it("has no button for a user who can never get admin mode", async () => {
      const r = renderPage({ v: notSetUp(), session: fakeSession({ admin: false, canAdmin: false }) });
      expect(await r.findByText("Samba einrichten kann nur ein Benutzer mit Admin-Modus.")).toBeTruthy();
      expect(r.queryByRole("button", { name: "Samba einrichten" })).toBeNull();
    });
  });

  describe("the service alert", () => {
    it("says which service is stopped, and starts the services as a job", async () => {
      vi.useFakeTimers();
      let started = false;
      const r = renderPage({
        v: () => view({ setup: { setUp: true, items: started ? items() : items({ samba: "not_started" }) } }),
        routes: jobHandlers("start", "samba", ["service"], [{ status: "done" }]),
      });
      await settle();
      expect(alertWith(r, "Dieser Dienst ist gestoppt: Samba.")).toBeTruthy();
      fireEvent.click(r.getByRole("button", { name: "Starten" }));
      await settle();
      expect(r.api.sent("POST", "/jobs").map((c) => c.body)).toEqual([{ kind: "start" }]);
      started = true;
      await vi.advanceTimersByTimeAsync(1000);
      expect(r.toasts.shown).toEqual([{ kind: "success", message: "Die Dienste laufen." }]);
      await settle();
      expect(r.queryByRole("button", { name: "Starten" })).toBeNull();
    });
  });

  describe("read-only", () => {
    it("gives a user who can never get admin mode no button that changes something, and says why", async () => {
      const r = renderPage({
        v: view({ setup: { setUp: true, items: items({ samba: "not_started" }) } }),
        session: fakeSession({ admin: false, canAdmin: false }),
      });
      await r.findByRole("table");
      expect(r.queryByRole("button", { name: "Freigabe anlegen" })).toBeNull();
      expect(r.queryByRole("button", { name: "Starten" })).toBeNull();
      expect(labels(row(r, "Fotos"))).toEqual([]);
      expect(alertWith(r, "Dieser Dienst ist gestoppt: Samba.")).toBeTruthy();
      expect(
        r.getByText(
          "Du kannst alles ansehen; Änderungen brauchen den Admin-Modus, den dein Benutzer nicht hat.",
        ),
      ).toBeTruthy();
    });

    it("keeps the buttons for a user who can get admin mode, and says that they ask for it", async () => {
      const r = renderPage({ session: fakeSession({ admin: false, canAdmin: true }) });
      await r.findByRole("table");
      expect(labels(row(r, "Fotos"))).toEqual([
        "Ordner-Rechte Fotos",
        "Bearbeiten Fotos",
        "Papierkorb öffnen Fotos",
        "Löschen Fotos",
      ]);
      expect(
        r.getByText("Ohne Admin-Modus ist alles lesbar; die Knöpfe fragen zuerst nach dem Admin-Modus."),
      ).toBeTruthy();
    });
  });

  describe("admin-first", () => {
    it("asks for admin mode, reads the view again, and only then opens the dialog", async () => {
      const session = adminOnRequest();
      const r = renderPage({ session });
      fireEvent.click(await r.findByRole("button", { name: "Freigabe anlegen" }));
      expect(r.queryByRole("dialog")).toBeNull();
      await waitFor(() => expect(r.api.sent("GET", "/view")).toHaveLength(2));
      expect(await r.findByRole("dialog", { name: "Freigabe anlegen" })).toBeTruthy();
    });

    it("opens nothing while admin mode stays off", async () => {
      const session = fakeSession({ admin: false, canAdmin: true });
      const r = renderPage({ session });
      fireEvent.click(await r.findByRole("button", { name: "Bearbeiten Fotos" }));
      await waitFor(() => expect(session.calls.requestAdmin).toBe(1));
      await new Promise((resolve) => setTimeout(resolve, 20));
      expect(r.queryByRole("dialog")).toBeNull();
    });

    it("asks for admin mode before Starten, reads the view again, and only then starts the services", async () => {
      const session = adminOnRequest();
      const r = renderPage({
        session,
        v: view({ setup: { setUp: true, items: items({ samba: "not_started" }) } }),
        routes: jobHandlers("start", "samba", ["service"], [{ status: "done" }]),
      });
      fireEvent.click(await r.findByRole("button", { name: "Starten" }));
      expect(r.api.sent("POST", "/jobs")).toEqual([]);
      await waitFor(() =>
        expect(r.api.sent("POST", "/jobs").map((c) => c.body)).toEqual([{ kind: "start" }]),
      );
      const order = r.api.calls.map((c) => `${c.method} ${c.path}`);
      expect(order.indexOf("POST /jobs")).toBeGreaterThan(order.lastIndexOf("GET /view"));
      expect(r.api.sent("GET", "/view")).toHaveLength(2);
    });

    it("starts nothing while admin mode stays off", async () => {
      const session = fakeSession({ admin: false, canAdmin: true });
      const r = renderPage({
        session,
        v: view({ setup: { setUp: true, items: items({ samba: "not_started" }) } }),
      });
      fireEvent.click(await r.findByRole("button", { name: "Starten" }));
      await waitFor(() => expect(session.calls.requestAdmin).toBe(1));
      await new Promise((resolve) => setTimeout(resolve, 20));
      expect(r.api.sent("POST", "/jobs")).toEqual([]);
    });

    it("opens the edit of the share as the view read after admin mode has it", async () => {
      const session = adminOnRequest();
      let reads = 0;
      const r = renderPage({
        session,
        v: () =>
          reads++ === 0
            ? view()
            : view({ config: { ...view().config, shares: [share({ comment: "neu" })] } }),
      });
      fireEvent.click(await r.findByRole("button", { name: "Bearbeiten Fotos" }));
      const dialog = await r.findByRole("dialog", { name: "Freigabe bearbeiten: Fotos" });
      expect((within(dialog).getByLabelText("Beschreibung (optional)") as HTMLInputElement).value).toBe(
        "neu",
      );
    });
  });

  describe("the dialogs", () => {
    it("opens the delete of a share", async () => {
      const r = renderPage();
      fireEvent.click(await r.findByRole("button", { name: "Löschen Filme" }));
      expect(await r.findByRole("dialog", { name: "Freigabe Filme löschen" })).toBeTruthy();
    });

    it("opens nothing while a job runs, and disables what would change something", async () => {
      const r = renderPage({ v: view({ job: job({ kind: "share-edit", target: "Fotos" }) }) });
      await r.findByRole("table");
      expect(r.getByText("Läuft: Freigabe bearbeiten Fotos")).toBeTruthy();
      expect((r.getByRole("button", { name: "Freigabe anlegen" }) as HTMLButtonElement).disabled).toBe(true);
      expect((r.getByRole("button", { name: "Bearbeiten Fotos" }) as HTMLButtonElement).disabled).toBe(true);
    });
  });

  describe("the folder rights", () => {
    /** The line of a folder in an open tree. */
    const line = (r: ReturnType<typeof renderPage>, path: string) =>
      r.container.querySelector<HTMLElement>(`[data-folder="${path}"]`);

    it("opens the tree of a share and the rights of a folder in it, and reads the tree again after the job", async () => {
      vi.useFakeTimers();
      let set = false;
      const r = renderPage({
        routes: {
          [foldersRoute("Fotos", "")]: () =>
            json(folders("", [{ name: "Steuern", hasChildren: false, own: set }])),
          [foldersRoute("Fotos", "Steuern")]: () =>
            json(
              folders("Steuern", [], set ? { own: true, from: "Steuern", rights: { anna: "write" } } : {}),
            ),
          ...jobHandlers("folder-rights", "Fotos/Steuern", ["config", "acl"], [{ status: "done", step: 1 }]),
        },
      });
      await settle();
      fireEvent.click(r.getByRole("button", { name: "Ordner-Rechte Fotos" }));
      await settle();
      expect(line(r, "Steuern")?.textContent).toContain("wie die Freigabe");
      fireEvent.click(r.getByRole("button", { name: "Steuern" }));
      await settle();
      const dialog = r.getByRole("dialog", { name: "Rechte für „Fotos/Steuern“" });
      fireEvent.click(
        within(within(dialog).getByRole("radiogroup", { name: "ben" })).getByRole("radio", {
          name: "kein Zugriff",
        }),
      );
      fireEvent.click(within(dialog).getByRole("button", { name: "Anwenden" }));
      await settle();
      expect(r.api.sent("POST", "/jobs").map((c) => c.body)).toEqual([
        { kind: "folder-rights", name: "Fotos", folder: "Steuern", rights: { anna: "write", oma: "read" } },
      ]);
      set = true;
      await vi.advanceTimersByTimeAsync(1000);
      expect(r.toasts.shown).toEqual([{ kind: "success", message: "Rechte für „Fotos/Steuern“ gesetzt." }]);
      expect(r.queryByRole("dialog")).toBeNull();
      await settle();
      expect(line(r, "Steuern")?.textContent).toContain("eigene Rechte");
    });

    it("keeps an open tree, its lines and the focus when the view is read again", async () => {
      vi.useFakeTimers();
      vi.spyOn(document, "visibilityState", "get").mockReturnValue("visible");
      const r = renderPage({
        routes: { [foldersRoute("Fotos", "")]: () => json(folders("", ["2024", "Steuern"])) },
      });
      await settle();
      fireEvent.click(r.getByRole("button", { name: "Ordner-Rechte Fotos" }));
      await settle();
      const lines = [line(r, "2024"), line(r, "Steuern")];
      const folder = r.getByRole("button", { name: "Steuern" });
      folder.focus();
      await vi.advanceTimersByTimeAsync(RELOAD_MS);
      expect(r.api.sent("GET", "/view")).toHaveLength(2);
      expect(r.api.sent("GET", "/folders?share=Fotos&dir=")).toHaveLength(1);
      expect(line(r, "2024")).toBe(lines[0]);
      expect(line(r, "Steuern")).toBe(lines[1]);
      expect(document.activeElement).toBe(folder);
    });

    it("gives the tree of a share no edit once the server said that its disk cannot keep folder rights", async () => {
      const r = renderPage({
        routes: {
          [foldersRoute("Fotos", "")]: () => json(folders("", ["Steuern"])),
          [foldersRoute("Fotos", "Steuern")]: () => json(folders("Steuern")),
          "POST /jobs": () => apiError(409, "no_acl", "no folder rights there", { reason: "no_privilege" }),
        },
      });
      fireEvent.click(await r.findByRole("button", { name: "Ordner-Rechte Fotos" }));
      fireEvent.click(await r.findByRole("button", { name: "Steuern" }));
      const dialog = await r.findByRole("dialog", { name: "Rechte für „Fotos/Steuern“" });
      fireEvent.click(
        within(await within(dialog).findByRole("radiogroup", { name: "ben" })).getByRole("radio", {
          name: "kein Zugriff",
        }),
      );
      fireEvent.click(within(dialog).getByRole("button", { name: "Anwenden" }));
      fireEvent.click(await within(dialog).findByRole("button", { name: "Schließen" }));
      await waitFor(() => expect(r.queryByRole("dialog")).toBeNull());
      expect(
        r.getByText(
          "Die Platte dieser Freigabe kann keine Ordner-Rechte halten: Die Extension darf die Rechte dort nicht schreiben (Attribute security.*). Der Baum ist nur zum Ansehen da.",
        ),
      ).toBeTruthy();
      expect(r.queryByRole("button", { name: "Steuern" })).toBeNull();
    });

    /** The view with Fotos running per user, with the folders that have rights of their own. */
    const perUser = (own: string[]): ViewResponse =>
      view({
        config: { ...view().config, shares: [share({ perUser: true, folders: own }), ...shares().slice(1)] },
      });
    /** The folder-rights job J9 on Fotos/Steuern, running in a step that can be cancelled, and as a cancel ends it. */
    const cancellable = () => {
      const running = job({
        id: "J9",
        kind: "folder-rights",
        target: "Fotos/Steuern",
        steps: ["config", "reload", "acl", "lbu"],
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
      return { running, ended };
    };
    /** Cancels the job of the open dialog, and closes the dialog once the job has ended. */
    const cancelAndClose = async (dialog: HTMLElement) => {
      fireEvent.click(within(dialog).getByRole("button", { name: "Vorgang abbrechen" }));
      await settle();
      await vi.advanceTimersByTimeAsync(1000);
      expect(within(dialog).getByRole("alert").textContent).toContain("abgebrochen");
      fireEvent.click(within(dialog).getByRole("button", { name: "Schließen" }));
      await settle();
    };

    it("shows a cancelled folder mixed in the tree, and erneut anwenden sends its rights again", async () => {
      vi.useFakeTimers();
      let cancelled = false;
      const { running, ended } = cancellable();
      const r = renderPage({
        v: perUser(["Steuern"]),
        routes: {
          [foldersRoute("Fotos", "")]: () =>
            json(
              folders("", [{ name: "Steuern", hasChildren: false, own: true }], {
                disk: { rights: {}, mixed: false, partial: false },
              }),
            ),
          [foldersRoute("Fotos", "Steuern")]: () =>
            json(
              folders("Steuern", [], {
                own: true,
                from: "Steuern",
                rights: { anna: "write" },
                disk: { rights: { anna: "write" }, mixed: cancelled, partial: false },
              }),
            ),
          "POST /jobs": () => json(running, 202),
          "GET /jobs/J9": () => json(cancelled ? ended : running),
          "POST /jobs/J9/cancel": () => {
            cancelled = true;
            return noContent();
          },
        },
      });
      await settle();
      fireEvent.click(r.getByRole("button", { name: "Ordner-Rechte Fotos" }));
      await settle();
      fireEvent.click(r.getByRole("button", { name: "Steuern" }));
      await settle();
      const dialog = r.getByRole("dialog", { name: "Rechte für „Fotos/Steuern“" });
      fireEvent.click(
        within(within(dialog).getByRole("radiogroup", { name: "ben" })).getByRole("radio", { name: "lesen" }),
      );
      fireEvent.click(within(dialog).getByRole("button", { name: "Anwenden" }));
      await settle();
      await cancelAndClose(dialog);
      const steuern = line(r, "Steuern") as HTMLElement;
      expect(steuern.textContent).toContain("gemischter Stand");
      fireEvent.click(within(steuern).getByRole("button", { name: "erneut anwenden" }));
      await settle();
      const again = r.getByRole("dialog", { name: "Rechte für „Fotos/Steuern“" });
      fireEvent.click(within(again).getByRole("button", { name: "Erneut anwenden" }));
      await settle();
      expect(r.api.sent("POST", "/jobs").map((c) => c.body)).toEqual([
        { kind: "folder-rights", name: "Fotos", folder: "Steuern", rights: { anna: "write", ben: "read" } },
        { kind: "folder-rights", name: "Fotos", folder: "Steuern", rights: { anna: "write" } },
      ]);
    });

    it("applies the share's rights again from its folder after a cancelled take-back of a folder's own rights", async () => {
      vi.useFakeTimers();
      let cancelled = false;
      const { running, ended } = cancellable();
      const r = renderPage({
        v: () => perUser(cancelled ? [] : ["Steuern"]),
        routes: {
          [foldersRoute("Fotos", "")]: () =>
            json(
              folders("", [{ name: "Steuern", hasChildren: false, own: !cancelled }], {
                disk: { rights: {}, mixed: cancelled, partial: false },
              }),
            ),
          [foldersRoute("Fotos", "Steuern")]: () =>
            json(
              cancelled
                ? folders("Steuern", [], { disk: { rights: {}, mixed: true, partial: false } })
                : folders("Steuern", [], { own: true, from: "Steuern", rights: { anna: "write" } }),
            ),
          "POST /jobs": () => json(running, 202),
          "GET /jobs/J9": () => json(cancelled ? ended : running),
          "POST /jobs/J9/cancel": () => {
            cancelled = true;
            return noContent();
          },
        },
      });
      await settle();
      fireEvent.click(r.getByRole("button", { name: "Ordner-Rechte Fotos" }));
      await settle();
      fireEvent.click(r.getByRole("button", { name: "Steuern" }));
      await settle();
      const dialog = r.getByRole("dialog", { name: "Rechte für „Fotos/Steuern“" });
      fireEvent.click(within(dialog).getByRole("button", { name: "Rechte der Freigabe übernehmen" }));
      await settle();
      await cancelAndClose(dialog);
      expect(r.getByText(/Gemischter Stand in der Freigabe selbst/)).toBeTruthy();
      const steuern = line(r, "Steuern") as HTMLElement;
      expect(steuern.textContent).toContain("gemischter Stand");
      expect(steuern.textContent).toContain("wie die Freigabe");
      fireEvent.click(within(steuern).getByRole("button", { name: "erneut anwenden" }));
      await settle();
      const again = r.getByRole("dialog", { name: "Rechte für „Fotos/Steuern“" });
      fireEvent.click(within(again).getByRole("button", { name: "Erneut anwenden" }));
      await settle();
      expect(r.api.sent("POST", "/jobs").map((c) => c.body)).toEqual([
        { kind: "folder-rights", name: "Fotos", folder: "Steuern", inherit: true },
        { kind: "folder-rights", name: "Fotos", folder: "" },
      ]);
    });

    it("reads each folder that the tree knows once after a folder-rights job", async () => {
      vi.useFakeTimers();
      vi.spyOn(document, "visibilityState", "get").mockReturnValue("visible");
      let set = false;
      const first = job({
        id: "J9",
        kind: "folder-rights",
        target: "Fotos/2024",
        steps: ["config", "acl"],
        step: 0,
      });
      const r = renderPage({
        v: () => perUser(set ? ["2024", "Steuern"] : ["Steuern"]),
        routes: {
          [foldersRoute("Fotos", "")]: () =>
            json(
              folders("", [
                { name: "2024", hasChildren: false, own: set },
                { name: "Steuern", hasChildren: false, own: true },
              ]),
            ),
          [foldersRoute("Fotos", "Steuern")]: () =>
            json(folders("Steuern", [], { own: true, from: "Steuern", rights: { anna: "write" } })),
          [foldersRoute("Fotos", "2024")]: () =>
            json(folders("2024", [], set ? { own: true, from: "2024", rights: { anna: "write" } } : {})),
          "POST /jobs": () => json(first, 202),
          "GET /jobs/J9": () => {
            set = true;
            return json({ ...first, status: "done", step: 1 });
          },
        },
      });
      const reads = (dir: string) =>
        r.api.sent("GET", `/folders?share=Fotos&dir=${encodeURIComponent(dir)}`).length;
      await settle();
      fireEvent.click(r.getByRole("button", { name: "Ordner-Rechte Fotos" }));
      await settle();
      fireEvent.click(r.getByRole("button", { name: "2024" }));
      await settle();
      const dialog = r.getByRole("dialog", { name: "Rechte für „Fotos/2024“" });
      fireEvent.click(
        within(within(dialog).getByRole("radiogroup", { name: "oma" })).getByRole("radio", {
          name: "kein Zugriff",
        }),
      );
      const before = { root: reads(""), steuern: reads("Steuern"), folder: reads("2024") };
      fireEvent.click(within(dialog).getByRole("button", { name: "Anwenden" }));
      await settle();
      await vi.advanceTimersByTimeAsync(1000);
      await settle();
      await vi.advanceTimersByTimeAsync(RELOAD_MS);
      expect(r.api.sent("GET", "/view").length).toBeGreaterThanOrEqual(3);
      expect(reads("")).toBe(before.root + 1);
      expect(reads("Steuern")).toBe(before.steuern + 1);
      expect(reads("2024")).toBe(before.folder + 1);
      expect(line(r, "2024")?.textContent).toContain("eigene Rechte");
    });

    it("has no tree for a share whose disk is missing, nor for a read-only page", async () => {
      const r = renderPage();
      await r.findByRole("table");
      expect(r.queryByRole("button", { name: "Ordner-Rechte Backup-USB" })).toBeNull();
      cleanup();
      const ro = renderPage({ session: fakeSession({ admin: false, canAdmin: false }) });
      await ro.findByRole("table");
      expect(ro.queryByRole("button", { name: "Ordner-Rechte Fotos" })).toBeNull();
    });
  });

  describe("the recycle bin", () => {
    it("opens the bin of a share, after admin mode, and tells a restore and reads the view again", async () => {
      const session = adminOnRequest();
      const r = renderPage({
        session,
        routes: {
          [recycleRoute("Fotos")]: () => json(recycleBin()),
          ...jobHandlers("recycle-restore", "Fotos", ["restore"], [{ status: "done" }]),
        },
      });
      fireEvent.click(await r.findByRole("button", { name: "Papierkorb öffnen Fotos" }));
      const dialog = await r.findByRole("dialog", { name: "Papierkorb von Fotos" });
      const reads = r.api.sent("GET", "/view").length;
      fireEvent.click(await within(dialog).findByRole("button", { name: "Wiederherstellen Kalender.pdf" }));
      await waitFor(() => expect(r.toasts.shown).toHaveLength(1), { timeout: 3000 });
      expect(r.toasts.shown[0]?.message).toContain("„Kalender.pdf“ ist zurück in Fotos.");
      await waitFor(() => expect(r.api.sent("GET", "/view").length).toBeGreaterThan(reads));
      expect(r.getByRole("dialog", { name: "Papierkorb von Fotos" })).toBeTruthy();
    });

    it("has no bin button for a share without a recycle bin or without its disk", async () => {
      const r = renderPage();
      await r.findByRole("table");
      expect(r.queryByRole("button", { name: "Papierkorb öffnen Filme" })).toBeNull();
      expect(r.queryByRole("button", { name: "Papierkorb öffnen Backup-USB" })).toBeNull();
    });
  });

  describe("the connections", () => {
    it("reads the connections only while their tab is open, and again on Neu laden", async () => {
      vi.useFakeTimers();
      vi.spyOn(document, "visibilityState", "get").mockReturnValue("visible");
      const r = renderPage({ routes: { "GET /connections": () => json(connections()) } });
      await settle();
      await vi.advanceTimersByTimeAsync(RELOAD_MS);
      expect(r.api.sent("GET", "/connections")).toHaveLength(0);
      fireEvent.click(tab(r, "Verbindungen"));
      await settle();
      expect(r.api.sent("GET", "/connections")).toHaveLength(1);
      expect(r.getByText("ANNA-LAPTOP · 192.168.1.23")).toBeTruthy();
      await vi.advanceTimersByTimeAsync(RELOAD_MS);
      expect(r.api.sent("GET", "/connections")).toHaveLength(2);
      fireEvent.click(r.getByRole("button", { name: "Neu laden" }));
      await settle();
      expect(r.api.sent("GET", "/connections")).toHaveLength(3);
      fireEvent.click(tab(r, "Freigaben"));
      await vi.advanceTimersByTimeAsync(3 * RELOAD_MS);
      expect(r.api.sent("GET", "/connections")).toHaveLength(3);
    });

    it("keeps the rows of the connections when the view and the connections are read again", async () => {
      vi.useFakeTimers();
      vi.spyOn(document, "visibilityState", "get").mockReturnValue("visible");
      const r = renderPage({ routes: { "GET /connections": () => json(connections()) } });
      await settle();
      fireEvent.click(tab(r, "Verbindungen"));
      await settle();
      const rows = r.getAllByRole("row");
      expect(rows).toHaveLength(3);
      await vi.advanceTimersByTimeAsync(RELOAD_MS);
      expect(r.api.sent("GET", "/view")).toHaveLength(2);
      expect(r.api.sent("GET", "/connections")).toHaveLength(2);
      const now = r.getAllByRole("row");
      expect(now).toHaveLength(3);
      expect(now.every((tr, i) => tr === rows[i])).toBe(true);
    });

    it("shows the hint without admin mode", async () => {
      const r = renderPage({ session: fakeSession({ admin: false, canAdmin: true }) });
      await r.findByRole("table");
      fireEvent.click(tab(r, "Verbindungen"));
      expect(r.getByText("Die Verbindungen sind nur im Admin-Modus sichtbar.")).toBeTruthy();
      expect(r.api.sent("GET", "/connections")).toHaveLength(0);
    });
  });

  it("closes the recycle bin of a share that is gone from the view", async () => {
    let gone = false;
    const r = renderPage({
      v: () => (gone ? view({ config: { ...view().config, shares: shares().slice(1) } }) : view()),
      routes: { [recycleRoute("Fotos")]: () => json(recycleBin()) },
    });
    fireEvent.click(await r.findByRole("button", { name: "Papierkorb öffnen Fotos" }));
    await r.findByRole("dialog", { name: "Papierkorb von Fotos" });
    gone = true;
    fireEvent.click(r.getByRole("button", { name: "Neu laden", hidden: true }));
    await waitFor(() => expect(r.queryByRole("dialog")).toBeNull());
  });
});
