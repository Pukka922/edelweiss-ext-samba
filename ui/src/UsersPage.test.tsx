import { fakeSession } from "@edelweiss/sdk/testing";
import { cleanup, fireEvent, waitFor, within } from "@solidjs/testing-library";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { HostUserView } from "./api";
import { users, view } from "./test/fixtures";
import { jobHandlers, renderPage } from "./test/page";
import { adminOnRequest, apiError, json, settle, type } from "./test/render";

afterEach(() => {
  cleanup();
  vi.useRealTimers();
  vi.unstubAllGlobals();
});

const hostUsers = (): HostUserView[] => [{ name: "carla", uid: 1001, selectable: true }];

const open = async (r: ReturnType<typeof renderPage>, tabName: string) => {
  await r.findByRole("table");
  fireEvent.click(r.getByRole("tab", { name: tabName }));
};

describe("the users and the settings in the page", () => {
  it("opens the dialogs of the users tab, each with the view that was read", async () => {
    const r = renderPage({ v: view({ hostUsers: hostUsers() }) });
    await open(r, "Nutzer");
    fireEvent.click(r.getByRole("button", { name: "Samba-Nutzer anlegen" }));
    expect(await r.findByRole("dialog", { name: "Samba-Nutzer anlegen" })).toBeTruthy();
    fireEvent.click(r.getByRole("button", { name: "Abbrechen" }));
    fireEvent.click(r.getByRole("button", { name: "Host-Benutzer hinzufügen" }));
    expect(await r.findByRole("dialog", { name: "Host-Benutzer hinzufügen" })).toBeTruthy();
    fireEvent.click(r.getByRole("button", { name: "Abbrechen" }));
    fireEvent.click(r.getByRole("button", { name: "Passwort ändern oma" }));
    expect(await r.findByRole("dialog", { name: "Passwort ändern: oma" })).toBeTruthy();
    fireEvent.click(r.getByRole("button", { name: "Abbrechen" }));
    fireEvent.click(r.getByRole("button", { name: "Samba-Zugang entfernen ben" }));
    expect(await r.findByRole("dialog", { name: "Samba-Zugang entfernen: ben" })).toBeTruthy();
  });

  it("adds a host user, then reads the view again and closes", async () => {
    let added = false;
    const r = renderPage({
      v: () => view({ hostUsers: added ? [] : hostUsers() }),
      routes: {
        "POST /users/host": () => {
          added = true;
          return json({});
        },
      },
    });
    await open(r, "Nutzer");
    fireEvent.click(r.getByRole("button", { name: "Host-Benutzer hinzufügen" }));
    const d = await r.findByRole("dialog", { name: "Host-Benutzer hinzufügen" });
    type(within(d).getByLabelText("Sein Linux-Passwort"), "geheim");
    fireEvent.click(within(d).getByRole("button", { name: "Hinzufügen" }));
    await waitFor(() => expect(r.queryByRole("dialog")).toBeNull());
    expect(r.toasts.shown).toEqual([{ kind: "success", message: "carla hat jetzt Samba-Zugang." }]);
    expect(r.api.sent("GET", "/view").length).toBeGreaterThan(1);
  });

  it("keeps an open dialog, what is typed in it and its focus over the poll every 5 seconds", async () => {
    vi.useFakeTimers();
    const r = renderPage({ v: () => view({ hostUsers: hostUsers() }) });
    await settle();
    fireEvent.click(r.getByRole("tab", { name: "Nutzer" }));
    fireEvent.click(r.getByRole("button", { name: "Passwort ändern oma" }));
    await settle();
    const d = r.getByRole("dialog", { name: "Passwort ändern: oma" });
    const pw = within(d).getByLabelText("Passwort") as HTMLInputElement;
    const again = within(d).getByLabelText("Passwort wiederholen") as HTMLInputElement;
    type(pw, "geheim");
    type(again, "geh");
    again.focus();
    const reads = r.api.sent("GET", "/view").length;
    await vi.advanceTimersByTimeAsync(5000);
    await vi.advanceTimersByTimeAsync(5000);
    expect(r.api.sent("GET", "/view").length).toBe(reads + 2);
    expect(
      within(r.getByRole("dialog", { name: "Passwort ändern: oma" })).getByLabelText("Passwort wiederholen"),
    ).toBe(again);
    expect(pw.value).toBe("geheim");
    expect(again.value).toBe("geh");
    expect(document.activeElement).toBe(again);
  });

  it("closes the delete dialog of a user when the view no longer has the user", async () => {
    vi.useFakeTimers();
    let gone = false;
    const r = renderPage({
      v: () => view({ users: gone ? users().filter((u) => u.name !== "tv") : users() }),
    });
    await settle();
    fireEvent.click(r.getByRole("tab", { name: "Nutzer" }));
    fireEvent.click(r.getByRole("button", { name: "Löschen tv" }));
    await settle();
    expect(r.getByRole("dialog", { name: "Nutzer löschen: tv" })).toBeTruthy();
    gone = true;
    await vi.advanceTimersByTimeAsync(5000);
    expect(r.queryByRole("dialog")).toBeNull();
  });

  it("shows the hint to a user without admin mode, who cannot see the Samba users", async () => {
    const r = renderPage({
      v: view({ users: [], usersKnown: false }),
      session: fakeSession({ admin: false, canAdmin: true }),
    });
    await r.findByRole("tab", { name: "Nutzer" });
    fireEvent.click(r.getByRole("tab", { name: "Nutzer" }));
    expect(r.getByText("Die Samba-Nutzer sind nur im Admin-Modus sichtbar.")).toBeTruthy();
  });

  it("shows the settings of the view in the settings tab", async () => {
    const r = renderPage();
    await open(r, "Einstellungen");
    expect((r.getByLabelText("Name im Netzwerk") as HTMLInputElement).value).toBe("nas");
    expect((r.getByRole("checkbox", { name: "eth0" }) as HTMLInputElement).checked).toBe(true);
  });

  it("answers an add that cannot be done because the Samba users cannot be read: nothing opens, and it says why", async () => {
    // admin mode comes on request, and the view read after it still has no Samba users (pdbedit failed)
    const r = renderPage({ v: view({ users: [], usersKnown: false }), session: adminOnRequest() });
    await r.findByRole("tab", { name: "Nutzer" });
    fireEvent.click(r.getByRole("tab", { name: "Nutzer" }));
    fireEvent.click(r.getByRole("button", { name: "Samba-Nutzer anlegen" }));
    await waitFor(() =>
      expect(r.toasts.shown).toEqual([
        { kind: "error", message: "Die Samba-Nutzer konnten nicht gelesen werden." },
      ]),
    );
    expect(r.queryByRole("dialog")).toBeNull();
    // and now that admin mode is on, the buttons are disabled with that reason
    expect((r.getByRole("button", { name: "Samba-Nutzer anlegen" }) as HTMLButtonElement).disabled).toBe(
      true,
    );
  });

  it("opens Passwort angleichen, not Passwort ändern, for a host user", async () => {
    const r = renderPage();
    await open(r, "Nutzer");
    fireEvent.click(r.getByRole("button", { name: "Passwort angleichen ben" }));
    expect(await r.findByRole("dialog", { name: "Passwort angleichen: ben" })).toBeTruthy();
  });

  describe("saving the settings", () => {
    const save = async (r: ReturnType<typeof renderPage>) => {
      fireEvent.click(r.getByRole("tab", { name: "Einstellungen" }));
      type(r.getByLabelText("Name im Netzwerk"), "nas2");
      fireEvent.click(r.getByRole("button", { name: "Speichern" }));
      await settle();
    };

    it("follows the job at page level: it goes on, and its end shows, after a tab switch", async () => {
      vi.useFakeTimers();
      const r = renderPage({
        routes: jobHandlers("settings", "samba", ["prepare", "lbu"], [{ status: "done", step: 1 }]),
      });
      await settle();
      await save(r);
      expect(r.api.sent("POST", "/jobs")).toEqual([
        {
          method: "POST",
          path: "/jobs",
          body: {
            kind: "settings",
            settings: {
              name: "nas2",
              workgroup: "WORKGROUP",
              smb2: false,
              discovery: true,
              interfaces: ["eth0"],
            },
          },
        },
      ]);
      fireEvent.click(r.getByRole("tab", { name: "Freigaben" }));
      expect(r.getByText(/Schritt 1\/2/)).toBeTruthy(); // the step shows on another tab
      expect((r.getByRole("button", { name: "Freigabe anlegen" }) as HTMLButtonElement).disabled).toBe(true);
      await vi.advanceTimersByTimeAsync(1000);
      expect(r.toasts.shown).toEqual([{ kind: "success", message: "Einstellungen gespeichert." }]);
      expect(r.queryByText(/Schritt 1\/2/)).toBeNull();
    });

    it("shows the failure of the job on another tab, and lets it be dismissed", async () => {
      vi.useFakeTimers();
      const r = renderPage({
        routes: jobHandlers(
          "settings",
          "samba",
          ["prepare", "lbu"],
          [{ status: "failed", code: "failed", error: "testparm: boom", step: 1 }],
        ),
      });
      await settle();
      await save(r);
      fireEvent.click(r.getByRole("tab", { name: "Nutzer" }));
      await vi.advanceTimersByTimeAsync(1000);
      expect(r.queryAllByRole("alert").some((a) => a.textContent?.includes("testparm: boom"))).toBe(true);
      fireEvent.click(r.getByRole("button", { name: "Ausblenden" }));
      expect(r.queryByText(/testparm: boom/)).toBeNull();
    });

    it("shows the refusal of an interface, and reads the view again", async () => {
      vi.useFakeTimers();
      const r = renderPage({ routes: { "POST /jobs": () => apiError(404, "not_found", "no interface") } });
      await settle();
      const reads = r.api.sent("GET", "/view").length;
      await save(r);
      expect(r.getByText("Das gibt es nicht mehr – lade neu.")).toBeTruthy();
      expect(r.api.sent("GET", "/view").length).toBeGreaterThan(reads);
    });
  });
});
