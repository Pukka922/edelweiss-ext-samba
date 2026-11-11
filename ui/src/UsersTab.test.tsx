import { cleanup, fireEvent, within } from "@solidjs/testing-library";
import { createSignal } from "solid-js";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { ShareView, UsersExtView, UserView } from "./api";
import { shares, users } from "./test/fixtures";
import { renderExt } from "./test/render";
import { UsersTab } from "./UsersTab";

afterEach(cleanup);

interface State {
  users: UserView[];
  shares: ShareView[];
  usersKnown: boolean;
  usersExt: UsersExtView;
  admin: boolean;
}

function renderTab(opts: Partial<State> & { readOnly?: boolean; busy?: boolean; locale?: "en" | "de" } = {}) {
  const [state, setState] = createSignal<State>({
    users: users(),
    shares: shares(),
    usersKnown: true,
    usersExt: { installed: true, version: "0.2.0" },
    admin: true,
    ...opts,
  });
  const on = {
    addSamba: vi.fn(),
    addHost: vi.fn(),
    password: vi.fn<(u: UserView) => void>(),
    remove: vi.fn<(u: UserView) => void>(),
  };
  const r = renderExt(
    () => (
      <UsersTab
        users={state().users}
        shares={state().shares}
        usersKnown={state().usersKnown}
        admin={state().admin}
        usersExt={state().usersExt}
        readOnly={opts.readOnly ?? false}
        busy={opts.busy ?? false}
        onAddSamba={on.addSamba}
        onAddHost={on.addHost}
        onPassword={on.password}
        onDelete={on.remove}
      />
    ),
    undefined,
    opts.locale ?? "de",
  );
  const row = (name: string) =>
    r.getAllByRole("row").find((tr) => tr.querySelector("td .font-semibold")?.textContent === name)!;
  const cells = (name: string) => within(row(name)).getAllByRole("cell");
  const labels = (name: string) =>
    within(row(name))
      .queryAllByRole("button")
      .map((b) => b.getAttribute("aria-label"));
  return { ...r, state, setState, on, row, cells, labels };
}

describe("UsersTab", () => {
  it("lists the users with their kind, their shares and their actions", () => {
    const r = renderTab();
    expect(r.getAllByRole("columnheader").map((th) => th.textContent)).toEqual([
      "Name",
      "Art",
      "Freigaben",
      "Aktionen",
    ]);
    expect(r.cells("anna")[1]?.textContent).toBe("nur Sambakein Login, kein Home");
    expect(r.cells("anna")[2]?.textContent).toBe("Fotos ✎, Filme ✎");
    expect(r.cells("ben")[1]?.textContent).toBe("Host-BenutzerPasswort wie beim Login");
    expect(r.cells("ben")[2]?.textContent).toBe("Fotos ✎, Backup-USB ✎");
    expect(r.cells("oma")[2]?.textContent).toBe("Fotos 👁");
    expect(r.cells("tv")[2]?.textContent).toBe("Filme 👁");
    expect(r.labels("anna")).toEqual(["Passwort ändern anna", "Löschen anna"]);
    // a host user has one password, the Linux one: no "Passwort ändern", but "Passwort angleichen"
    expect(r.labels("ben")).toEqual(["Passwort angleichen ben", "Samba-Zugang entfernen ben"]);
    expect(r.queryByRole("button", { name: "Passwort ändern ben" })).toBeNull();
  });

  it("says none for a user without rights, and marks a disabled one", () => {
    const r = renderTab({ users: [{ name: "gast", kind: "samba", disabled: true }], shares: [] });
    expect(r.cells("gast")[1]?.textContent).toContain("deaktiviert");
    expect(r.cells("gast")[2]?.textContent).toBe("keine");
  });

  it("asks the page for the dialogs", () => {
    const r = renderTab();
    fireEvent.click(r.getByRole("button", { name: "Samba-Nutzer anlegen" }));
    fireEvent.click(r.getByRole("button", { name: "Host-Benutzer hinzufügen" }));
    fireEvent.click(r.getByRole("button", { name: "Passwort ändern oma" }));
    fireEvent.click(r.getByRole("button", { name: "Passwort angleichen ben" }));
    fireEvent.click(r.getByRole("button", { name: "Löschen tv" }));
    expect(r.on.addSamba).toHaveBeenCalledOnce();
    expect(r.on.addHost).toHaveBeenCalledOnce();
    expect(r.on.password).toHaveBeenCalledWith(expect.objectContaining({ name: "oma" }));
    expect(r.on.password).toHaveBeenCalledWith(expect.objectContaining({ name: "ben" }));
    expect(r.on.remove).toHaveBeenCalledWith(expect.objectContaining({ name: "tv" }));
  });

  it("lists no users when they are not known, and says why", () => {
    const r = renderTab({ usersKnown: false, users: [], admin: false });
    expect(r.getByText("Die Samba-Nutzer sind nur im Admin-Modus sichtbar.")).toBeTruthy();
    expect(r.queryAllByRole("row")).toEqual([]);
    // the buttons ask for admin mode first, so they stay
    expect((r.getByRole("button", { name: "Samba-Nutzer anlegen" }) as HTMLButtonElement).disabled).toBe(
      false,
    );
    cleanup();
    const en = renderTab({ usersKnown: false, users: [], admin: false, locale: "en" });
    expect(en.getByText("The Samba users are only visible in admin mode.")).toBeTruthy();
  });

  it("says in admin mode that the Samba users could not be read, and disables the add buttons with that reason", () => {
    const r = renderTab({ usersKnown: false, users: [], admin: true });
    expect(r.getByText("Die Samba-Nutzer konnten nicht gelesen werden.")).toBeTruthy();
    expect(r.queryByText(/nur im Admin-Modus sichtbar/)).toBeNull();
    for (const name of ["Samba-Nutzer anlegen", "Host-Benutzer hinzufügen"]) {
      const b = r.getByRole("button", { name }) as HTMLButtonElement;
      expect(b.disabled).toBe(true);
      expect(b.title).toBe("Die Samba-Nutzer konnten nicht gelesen werden.");
    }
    cleanup();
    const en = renderTab({ usersKnown: false, users: [], admin: true, locale: "en" });
    expect(en.getByText("The Samba users could not be read.")).toBeTruthy();
  });

  it("has no buttons on a read-only page, and disables them while a job runs", () => {
    const ro = renderTab({ readOnly: true });
    expect(ro.queryAllByRole("button")).toEqual([]);
    expect(ro.getAllByRole("row").length).toBe(5);
    cleanup();
    const busy = renderTab({ busy: true });
    for (const b of busy.getAllByRole("button")) expect((b as HTMLButtonElement).disabled).toBe(true);
  });

  it("says that nobody is there yet", () => {
    const r = renderTab({ users: [] });
    expect(r.getByText(/Noch keine Samba-Nutzer/)).toBeTruthy();
  });

  describe("the warning about the users extension", () => {
    const warning = (r: ReturnType<typeof renderTab>) =>
      r
        .queryAllByRole("alert")
        .find(
          (a) => a.textContent?.includes("Benutzer-Extension") || a.textContent?.includes("users extension"),
        );

    it("shows for a version below 0.2.0", () => {
      const r = renderTab({ usersExt: { installed: true, version: "0.1.0" } });
      expect(warning(r)?.textContent).toBe(
        "Die installierte Benutzer-Extension ist älter als 0.2.0: Sie erkennt reine Samba-Nutzer nicht, schützt sie nicht und zieht das Samba-Passwort nicht mit. Aktualisiere sie.",
      );
    });

    it("shows when the version cannot be read", () => {
      const r = renderTab({ usersExt: { installed: true, version: "" } });
      expect(warning(r)?.textContent).toContain("älter als 0.2.0");
    });

    it("shows in English", () => {
      const r = renderTab({ usersExt: { installed: true, version: "0.1.0" }, locale: "en" });
      expect(warning(r)?.textContent).toBe(
        "The installed users extension is older than 0.2.0: it does not recognise pure Samba users, does not protect them and does not carry the Samba password along. Update it.",
      );
    });

    it("does not show for 0.2.0 or newer, nor when it is not installed", () => {
      expect(warning(renderTab())).toBeUndefined();
      cleanup();
      expect(warning(renderTab({ usersExt: { installed: true, version: "0.10.1" } }))).toBeUndefined();
      cleanup();
      expect(warning(renderTab({ usersExt: { installed: false, version: "" } }))).toBeUndefined();
    });
  });

  it("keeps its rows and the focus when a new view brings the same users", () => {
    const r = renderTab();
    const anna = r.row("anna");
    const button = within(anna).getByRole("button", { name: "Passwort ändern anna" });
    button.focus();
    r.setState((s) => ({ ...s, users: users(), shares: shares() })); // as new objects
    expect(r.row("anna")).toBe(anna);
    expect(button.isConnected).toBe(true);
    expect(document.activeElement).toBe(button);
    // a user comes: the others stay
    r.setState((s) => ({ ...s, users: [...users(), { name: "zoe", kind: "samba", disabled: false }] }));
    expect(r.row("anna")).toBe(anna);
    expect(r.row("zoe")).toBeTruthy();
    // and a right changes in place
    r.setState((s) => ({
      ...s,
      shares: shares().map((sh) =>
        sh.name === "Fotos" ? { ...sh, rights: { ...sh.rights, anna: "read" } } : sh,
      ),
    }));
    expect(r.row("anna")).toBe(anna);
    expect(r.cells("anna")[2]?.textContent).toBe("Fotos 👁, Filme ✎");
  });

  it("lists an account of the extension without a Samba entry as such, with Delete alone", () => {
    const r = renderTab({
      users: [...users(), { name: "lost", kind: "samba", disabled: false, noEntry: true }],
    });
    expect(r.cells("lost")[1]?.textContent).toBe("nur Sambaohne Samba-Eintragkein Login, kein Home");
    expect(r.labels("lost")).toEqual(["Löschen lost"]);
    fireEvent.click(within(r.row("lost")).getByRole("button", { name: "Löschen lost" }));
    expect(r.on.remove).toHaveBeenCalledWith(expect.objectContaining({ name: "lost", noEntry: true }));
  });
});
