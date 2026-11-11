import { cleanup, fireEvent, waitFor, within } from "@solidjs/testing-library";
import { createSignal } from "solid-js";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { UserView } from "../api";
import { users, view } from "../test/fixtures";
import { apiError, type Handler, json, renderExt, stubApi, type } from "../test/render";
import { SyncPasswordDialog } from "./SyncPasswordDialog";

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

const ben = () => users().find((u) => u.name === "ben") as UserView;

function renderDialog(routes: Record<string, Handler> = {}, locale: "en" | "de" = "de") {
  const api = stubApi(routes);
  const [v, setV] = createSignal(view());
  const onClose = vi.fn();
  const onDone = vi.fn();
  const onReload = vi.fn();
  const title = locale === "de" ? "Passwort angleichen: ben" : "Match the password: ben";
  const r = renderExt(
    () => (
      <SyncPasswordDialog
        user={ben()}
        view={v}
        busy={() => false}
        onClose={onClose}
        onDone={onDone}
        onReload={onReload}
      />
    ),
    undefined,
    locale,
  );
  const dialog = () => r.getByRole("dialog", { name: title });
  const field = () =>
    within(dialog()).getByLabelText(
      locale === "de" ? "Sein Linux-Passwort" : "Their Linux password",
    ) as HTMLInputElement;
  const button = (name: string) => within(dialog()).getByRole("button", { name }) as HTMLButtonElement;
  return { ...r, api, setV, onClose, onDone, onReload, dialog, field, button };
}

describe("SyncPasswordDialog", () => {
  it("asks for the Linux password once, and says that Samba gets the same", () => {
    const r = renderDialog();
    expect(r.field().type).toBe("password");
    expect(r.field().autocomplete).toBe("current-password");
    expect(within(r.dialog()).queryByLabelText("Passwort wiederholen")).toBeNull();
    expect(within(r.dialog()).getByText("Gib das Linux-Passwort ein; Samba bekommt dasselbe.")).toBeTruthy();
    expect(r.button("Angleichen").disabled).toBe(true);
    type(r.field(), "x");
    expect(r.button("Angleichen").disabled).toBe(false);
  });

  it("says it in English", () => {
    const r = renderDialog({}, "en");
    expect(within(r.dialog()).getByText("Enter the Linux password; Samba gets the same.")).toBeTruthy();
  });

  it("sends it to the user's route, clears the field at once, and tells the page", async () => {
    let answer!: (res: Response) => void;
    const r = renderDialog({
      "POST /users/ben/password": () => new Promise<Response>((res) => (answer = res)),
    });
    type(r.field(), "ge heim");
    fireEvent.click(r.button("Angleichen"));
    expect(r.field().value).toBe("");
    answer(json({}));
    await waitFor(() => expect(r.onDone).toHaveBeenCalledWith("Passwort von ben angeglichen."));
    expect(r.api.sent("POST", "/users/ben/password")).toEqual([
      { method: "POST", path: "/users/ben/password", body: { password: "ge heim" } },
    ]);
  });

  it("words a password that is not the Linux one, and the field stays empty", async () => {
    const r = renderDialog({ "POST /users/ben/password": () => apiError(409, "wrong_password", "x") });
    type(r.field(), "falsch");
    fireEvent.click(r.button("Angleichen"));
    expect((await within(r.dialog()).findByRole("alert")).textContent).toBe(
      "Das Linux-Passwort stimmt nicht.",
    );
    expect(r.field().value).toBe("");
    expect(r.onDone).not.toHaveBeenCalled();
  });
});
