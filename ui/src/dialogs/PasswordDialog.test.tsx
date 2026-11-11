import { cleanup, fireEvent, waitFor, within } from "@solidjs/testing-library";
import { createSignal } from "solid-js";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { UserView, ViewResponse } from "../api";
import { users, view } from "../test/fixtures";
import { apiError, type Handler, json, renderExt, stubApi, type } from "../test/render";
import { PasswordDialog } from "./PasswordDialog";

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

const find = (name: string) => users().find((u) => u.name === name) as UserView;

function renderDialog(user: UserView, routes: Record<string, Handler> = {}) {
  const api = stubApi(routes);
  const [v, setV] = createSignal<ViewResponse>(view());
  const onClose = vi.fn();
  const onDone = vi.fn();
  const onReload = vi.fn();
  const r = renderExt(
    () => (
      <PasswordDialog
        user={user}
        view={v}
        busy={() => false}
        onClose={onClose}
        onDone={onDone}
        onReload={onReload}
      />
    ),
    undefined,
    "de",
  );
  const dialog = () => r.getByRole("dialog", { name: `Passwort ändern: ${user.name}` });
  const field = (label: string) => within(dialog()).getByLabelText(label) as HTMLInputElement;
  const button = (name: string) => within(dialog()).getByRole("button", { name }) as HTMLButtonElement;
  return { ...r, api, v, setV, onClose, onDone, onReload, dialog, field, button };
}

describe("PasswordDialog", () => {
  it("sends the password to the user's route, and clears it", async () => {
    let answer!: (res: Response) => void;
    const r = renderDialog(find("anna"), {
      "POST /users/anna/password": () => new Promise<Response>((res) => (answer = res)),
    });
    expect(r.field("Passwort").autocomplete).toBe("new-password");
    expect(r.field("Passwort wiederholen").autocomplete).toBe("new-password");
    type(r.field("Passwort"), "-neu ö:");
    type(r.field("Passwort wiederholen"), "-neu ö:");
    fireEvent.click(r.button("Ändern"));
    expect(r.field("Passwort").value).toBe("");
    expect(r.field("Passwort wiederholen").value).toBe("");
    answer(json({}));
    await waitFor(() => expect(r.onDone).toHaveBeenCalledWith("Passwort von anna geändert."));
    expect(r.api.sent("POST", "/users/anna/password")).toEqual([
      { method: "POST", path: "/users/anna/password", body: { password: "-neu ö:" } },
    ]);
    expect(r.api.calls.every((c) => !c.path.includes("neu"))).toBe(true);
  });

  it("sends only when the two passwords match", () => {
    const r = renderDialog(find("anna"));
    type(r.field("Passwort"), "a");
    type(r.field("Passwort wiederholen"), "b");
    expect(within(r.dialog()).getByText("Die Passwörter stimmen nicht überein.")).toBeTruthy();
    expect(r.button("Ändern").disabled).toBe(true);
    type(r.field("Passwort wiederholen"), "a");
    expect(r.button("Ändern").disabled).toBe(false);
  });

  it("clears the passwords when the request fails, and shows why", async () => {
    const r = renderDialog(find("anna"), {
      "POST /users/anna/password": () => apiError(500, "failed", "smbpasswd: boom"),
    });
    type(r.field("Passwort"), "geheim");
    type(r.field("Passwort wiederholen"), "geheim");
    fireEvent.click(r.button("Ändern"));
    expect((await within(r.dialog()).findByRole("alert")).textContent).toBe(
      "Das ist fehlgeschlagen: smbpasswd: boom",
    );
    expect(r.field("Passwort").value).toBe("");
    expect(r.field("Passwort wiederholen").value).toBe("");
    expect(r.button("Ändern").disabled).toBe(true); // nothing to send until it is typed again
  });

  it("is over when the user is gone: Close alone, and the page reads the view again", async () => {
    const r = renderDialog(find("anna"), { "POST /users/anna/password": () => apiError(404, "not_found") });
    type(r.field("Passwort"), "x");
    type(r.field("Passwort wiederholen"), "x");
    fireEvent.click(r.button("Ändern"));
    await r.findByText("Das gibt es nicht mehr – lade neu.");
    expect(r.onReload).toHaveBeenCalled();
    expect(within(r.dialog()).queryByRole("button", { name: "Ändern" })).toBeNull();
    fireEvent.click(r.button("Schließen"));
    expect(r.onClose).toHaveBeenCalled();
  });

  it("closes when the user leaves the view, and keeps what was typed over a poll that has the user", () => {
    const r = renderDialog(find("anna"));
    type(r.field("Passwort"), "geheim");
    const again = r.field("Passwort wiederholen");
    type(again, "gehe");
    again.focus();
    r.setV(view());
    expect(r.field("Passwort wiederholen")).toBe(again);
    expect(again.value).toBe("gehe");
    expect(r.field("Passwort").value).toBe("geheim");
    expect(document.activeElement).toBe(again);
    expect(r.api.calls).toEqual([]);
    expect(r.onClose).not.toHaveBeenCalled();
    r.setV(view({ users: users().filter((u) => u.name !== "anna") }));
    expect(r.onClose).toHaveBeenCalledOnce();
  });
});
