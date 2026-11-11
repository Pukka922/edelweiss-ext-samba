import { cleanup, fireEvent, waitFor, within } from "@solidjs/testing-library";
import { createSignal } from "solid-js";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { HostUserView, ViewResponse } from "../api";
import { view } from "../test/fixtures";
import { apiError, type Handler, json, renderExt, stubApi, type } from "../test/render";
import { AddHostUserDialog } from "./AddHostUserDialog";

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

const hostUsers = (): HostUserView[] => [
  { name: "carla", uid: 1001, selectable: true },
  { name: "Dora", uid: 1002, selectable: false, reason: "name" },
  { name: "emil", uid: 1003, selectable: true },
];

const withHosts = (hosts: HostUserView[] = hostUsers()): ViewResponse => view({ hostUsers: hosts });

function renderDialog(
  opts: { v?: ViewResponse; routes?: Record<string, Handler>; locale?: "en" | "de" } = {},
) {
  const api = stubApi(opts.routes ?? {});
  const [v, setV] = createSignal(opts.v ?? withHosts());
  const onClose = vi.fn();
  const onDone = vi.fn();
  const onReload = vi.fn();
  const r = renderExt(
    () => (
      <AddHostUserDialog view={v} busy={() => false} onClose={onClose} onDone={onDone} onReload={onReload} />
    ),
    undefined,
    opts.locale ?? "de",
  );
  const dialog = () => r.getByRole("dialog", { name: "Host-Benutzer hinzufügen" });
  const select = () => within(dialog()).getByLabelText("Benutzer") as HTMLSelectElement;
  const password = () => within(dialog()).getByLabelText("Sein Linux-Passwort") as HTMLInputElement;
  const button = (name: string) => within(dialog()).getByRole("button", { name }) as HTMLButtonElement;
  return { ...r, api, setV, onClose, onDone, onReload, dialog, select, password, button };
}

describe("AddHostUserDialog", () => {
  it("lists the host users, and shows one whose name Samba does not take as disabled, with the reason", () => {
    const r = renderDialog();
    const options = within(r.select()).getAllByRole("option") as HTMLOptionElement[];
    expect(options.map((o) => [o.value, o.disabled, o.textContent])).toEqual([
      ["carla", false, "carla"],
      ["Dora", true, "Dora – Name nicht zulässig"],
      ["emil", false, "emil"],
    ]);
    expect(r.select().value).toBe("carla");
    expect(r.password().autocomplete).toBe("new-password");
    expect(r.getByText(/Samba bekommt dasselbe Passwort/)).toBeTruthy();
  });

  it("sends the chosen user and the Linux password, and clears the password", async () => {
    let answer!: (res: Response) => void;
    const r = renderDialog({
      routes: { "POST /users/host": () => new Promise<Response>((res) => (answer = res)) },
    });
    fireEvent.change(r.select(), { target: { value: "emil" } });
    type(r.password(), "ge heim");
    fireEvent.click(r.button("Hinzufügen"));
    expect(r.password().value).toBe("");
    answer(json({}));
    await waitFor(() => expect(r.onDone).toHaveBeenCalledWith("emil hat jetzt Samba-Zugang."));
    expect(r.api.sent("POST", "/users/host")).toEqual([
      { method: "POST", path: "/users/host", body: { name: "emil", password: "ge heim" } },
    ]);
  });

  it("words a wrong Linux password, and clears the field", async () => {
    const r = renderDialog({
      routes: { "POST /users/host": () => apiError(409, "wrong_password", "that is not the Linux password") },
    });
    type(r.password(), "falsch");
    fireEvent.click(r.button("Hinzufügen"));
    expect((await within(r.dialog()).findByRole("alert")).textContent).toBe(
      "Das Linux-Passwort stimmt nicht.",
    );
    expect(r.password().value).toBe("");
    expect(r.select().value).toBe("carla");
    expect(r.onDone).not.toHaveBeenCalled();
  });

  it("words it in English as well", async () => {
    const r = renderDialog({
      locale: "en",
      routes: { "POST /users/host": () => apiError(409, "wrong_password", "x") },
    });
    const dialog = r.getByRole("dialog", { name: "Add a host user" });
    type(within(dialog).getByLabelText("Their Linux password"), "x");
    fireEvent.click(within(dialog).getByRole("button", { name: "Add" }));
    expect((await within(dialog).findByRole("alert")).textContent).toBe("The Linux password is wrong.");
  });

  it("needs a password and a user that can be chosen", () => {
    const r = renderDialog();
    expect(r.button("Hinzufügen").disabled).toBe(true);
    type(r.password(), "x");
    expect(r.button("Hinzufügen").disabled).toBe(false);
    cleanup();
    const none = renderDialog({ v: withHosts([hostUsers()[1] as HostUserView]) });
    type(none.password(), "x");
    expect(none.button("Hinzufügen").disabled).toBe(true);
    cleanup();
    const empty = renderDialog({ v: withHosts([]) });
    expect(empty.getByText("Kein Host-Benutzer ohne Samba-Zugang.")).toBeTruthy();
  });

  it("keeps the choice, the password and the focus over polls, and keeps its options", () => {
    const r = renderDialog();
    fireEvent.change(r.select(), { target: { value: "emil" } });
    type(r.password(), "geheim");
    r.password().focus();
    const select = r.select();
    const option = within(select).getByRole("option", { name: "emil" });
    r.setV(withHosts()); // the same, as new objects
    r.setV(withHosts());
    expect(r.select()).toBe(select);
    expect(within(select).getByRole("option", { name: "emil" })).toBe(option);
    expect(r.select().value).toBe("emil");
    expect(r.password().value).toBe("geheim");
    expect(document.activeElement).toBe(r.password());
    expect(r.api.calls).toEqual([]);
  });

  it("clears the choice and the password when the picked user leaves the view, and never switches to another user", () => {
    const r = renderDialog();
    fireEvent.change(r.select(), { target: { value: "emil" } });
    type(r.password(), "emils-passwort");
    r.setV(withHosts(hostUsers().filter((u) => u.name !== "emil")));
    expect(r.select().value).toBe(""); // not carla: nobody is chosen
    expect(r.password().value).toBe("");
    expect(r.button("Hinzufügen").disabled).toBe(true);
    expect(r.getByText("emil ist nicht mehr verfügbar.")).toBeTruthy();
    // a password typed now is for the user that is picked next
    type(r.password(), "x");
    expect(r.button("Hinzufügen").disabled).toBe(true);
    fireEvent.change(r.select(), { target: { value: "carla" } });
    expect(r.queryByText("emil ist nicht mehr verfügbar.")).toBeNull();
    expect(r.button("Hinzufügen").disabled).toBe(false);
    expect(r.api.calls).toEqual([]);
  });

  it("clears the choice also when the picked user can no longer be chosen", () => {
    const r = renderDialog();
    r.setV(
      withHosts([
        { name: "carla", uid: 1001, selectable: false, reason: "name" },
        hostUsers()[2] as HostUserView,
      ]),
    );
    expect(r.select().value).toBe("");
    expect(r.button("Hinzufügen").disabled).toBe(true);
  });
});
