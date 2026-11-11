import { cleanup, fireEvent, waitFor, within } from "@solidjs/testing-library";
import { createSignal } from "solid-js";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { UserView, ViewResponse } from "../api";
import { job, users, view } from "../test/fixtures";
import { apiError, type Handler, json, renderExt, settle, stubApi } from "../test/render";
import { DeleteUserDialog } from "./DeleteUserDialog";

afterEach(() => {
  cleanup();
  vi.useRealTimers();
  vi.unstubAllGlobals();
});

const find = (name: string) => users().find((u) => u.name === name) as UserView;

function renderDialog(user: UserView, routes: Record<string, Handler> = {}, v: ViewResponse = view()) {
  const api = stubApi(routes);
  const [now, setV] = createSignal(v);
  const onClose = vi.fn();
  const onDone = vi.fn();
  const onReload = vi.fn();
  const r = renderExt(
    () => (
      <DeleteUserDialog
        user={user}
        view={now}
        busy={() => false}
        onClose={onClose}
        onDone={onDone}
        onReload={onReload}
      />
    ),
    undefined,
    "de",
  );
  const dialog = (title: string) => r.getByRole("dialog", { name: title });
  return { ...r, api, setV, onClose, onDone, onReload, dialog };
}

describe("DeleteUserDialog", () => {
  it("names the shares of a pure Samba user, and deletes", async () => {
    const r = renderDialog(find("anna"), { "DELETE /users/anna": () => json({}) });
    const d = r.dialog("Nutzer löschen: anna");
    expect(
      within(d).getByText("Seine Rechte in diesen Freigaben gehen verloren: Fotos und Filme."),
    ).toBeTruthy();
    expect(within(d).getByText("Der Nutzer und sein Konto auf diesem Host werden gelöscht.")).toBeTruthy();
    fireEvent.click(within(d).getByRole("button", { name: "Löschen" }));
    await waitFor(() => expect(r.onDone).toHaveBeenCalledWith("anna gelöscht."));
    expect(r.api.sent("DELETE", "/users/anna").length).toBe(1);
  });

  it("removes only the Samba access of a host user, and says the Linux account stays", async () => {
    const r = renderDialog(find("ben"), { "DELETE /users/ben": () => json({}) });
    const d = r.dialog("Samba-Zugang entfernen: ben");
    expect(within(d).getByText("Das Linux-Konto ben bleibt; nur der Zugang zu Samba geht.")).toBeTruthy();
    expect(
      within(d).getByText("Seine Rechte in diesen Freigaben gehen verloren: Fotos und Backup-USB."),
    ).toBeTruthy();
    fireEvent.click(within(d).getByRole("button", { name: "Samba-Zugang entfernen" }));
    await waitFor(() => expect(r.onDone).toHaveBeenCalledWith("Samba-Zugang von ben entfernt."));
  });

  it("says when the user has no rights anywhere", () => {
    const r = renderDialog(find("anna"), {}, view({ config: { ...view().config, shares: [] } }));
    expect(r.getByText("Er hat in keiner Freigabe Rechte.")).toBeTruthy();
  });

  it("follows the job that drops the user from the shares, and is done at its end", async () => {
    vi.useFakeTimers();
    const first = job({ id: "J9", kind: "user-drop", target: "anna", steps: ["prepare", "lbu"] });
    const r = renderDialog(find("anna"), {
      "DELETE /users/anna": () => json({ job: first }),
      "GET /jobs/J9": () => json({ ...first, status: "done", step: 1 }),
    });
    fireEvent.click(within(r.dialog("Nutzer löschen: anna")).getByRole("button", { name: "Löschen" }));
    await settle();
    expect(r.onDone).not.toHaveBeenCalled();
    expect(within(r.dialog("Nutzer löschen: anna")).getByText(/Schritt 1\/2/)).toBeTruthy();
    await vi.advanceTimersByTimeAsync(1000);
    expect(r.onDone).toHaveBeenCalledWith("anna gelöscht.");
  });

  it("is over when that job fails: the user is gone already, so Close alone and a reload", async () => {
    vi.useFakeTimers();
    const first = job({ id: "J9", kind: "user-drop", target: "anna", steps: ["prepare", "lbu"] });
    const r = renderDialog(find("anna"), {
      "DELETE /users/anna": () => json({ job: first }),
      "GET /jobs/J9": () => json({ ...first, status: "failed", code: "failed", error: "lbu: boom", step: 1 }),
    });
    const d = r.dialog("Nutzer löschen: anna");
    fireEvent.click(within(d).getByRole("button", { name: "Löschen" }));
    await settle();
    await vi.advanceTimersByTimeAsync(1000);
    expect(within(d).getByRole("alert").textContent).toContain("lbu: boom");
    expect(r.onDone).not.toHaveBeenCalled();
    expect(r.onReload).toHaveBeenCalled();
    expect(within(d).queryByRole("button", { name: "Löschen" })).toBeNull();
    // the user leaves the view while the failure stands: the dialog stays to say it
    r.setV(view({ users: users().filter((u) => u.name !== "anna") }));
    expect(r.onClose).not.toHaveBeenCalled();
  });

  it("says so when the delete worked and the job that drops the name could not start: over, with the way out", async () => {
    vi.useFakeTimers();
    const r = renderDialog(find("anna"), {
      "DELETE /users/anna": () => json({ jobError: "the job could not be started: no runner" }),
    });
    const d = r.dialog("Nutzer löschen: anna");
    fireEvent.click(within(d).getByRole("button", { name: "Löschen" }));
    await settle();
    expect(within(d).getByText(/^Erledigt, aber anna steht noch in Freigaben/).textContent).toBe(
      "Erledigt, aber anna steht noch in Freigaben, ohne Zugriff: Der Vorgang, der den Namen herausnimmt, konnte nicht starten (the job could not be started: no runner). Entferne ihn auf der Seite mit „Verwaiste Namen entfernen“.",
    );
    expect(r.onDone).not.toHaveBeenCalled();
    expect(r.onReload).toHaveBeenCalled();
    expect(within(d).queryByRole("button", { name: "Löschen" })).toBeNull();
  });

  it("shows a refusal and can be sent again", async () => {
    const r = renderDialog(find("anna"), { "DELETE /users/anna": () => apiError(409, "busy") });
    const d = r.dialog("Nutzer löschen: anna");
    fireEvent.click(within(d).getByRole("button", { name: "Löschen" }));
    await within(d).findByText("Gerade läuft schon eine andere Änderung – versuch es gleich noch einmal.");
    expect((within(d).getByRole("button", { name: "Löschen" }) as HTMLButtonElement).disabled).toBe(false);
  });

  it("closes when the user leaves the view; a poll with the same content changes nothing", () => {
    const r = renderDialog(find("anna"));
    const text = r.getByText(/Seine Rechte in diesen Freigaben/);
    r.setV(view());
    expect(r.getByText(/Seine Rechte in diesen Freigaben/)).toBe(text);
    expect(r.api.calls).toEqual([]);
    r.setV(view({ users: users().filter((u) => u.name !== "anna") }));
    expect(r.onClose).toHaveBeenCalledOnce();
  });
});
