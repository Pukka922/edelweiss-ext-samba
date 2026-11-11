import { cleanup, fireEvent, waitFor, within } from "@solidjs/testing-library";
import { createSignal } from "solid-js";
import { afterEach, describe, expect, it, vi } from "vitest";
import { job } from "../test/fixtures";
import { apiError, type Handler, json, renderExt, stubApi, type } from "../test/render";
import { AddSambaUserDialog } from "./AddSambaUserDialog";

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

function renderDialog(routes: Record<string, Handler> = {}) {
  const api = stubApi(routes);
  const [busy, setBusy] = createSignal(false);
  const onClose = vi.fn();
  const onDone = vi.fn();
  const onReload = vi.fn();
  const r = renderExt(
    () => <AddSambaUserDialog busy={busy} onClose={onClose} onDone={onDone} onReload={onReload} />,
    undefined,
    "de",
  );
  const dialog = () => r.getByRole("dialog", { name: "Samba-Nutzer anlegen" });
  const field = (label: string) => within(dialog()).getByLabelText(label) as HTMLInputElement;
  const button = (name: string) => within(dialog()).getByRole("button", { name }) as HTMLButtonElement;
  const fill = (pw = "geheim", again = pw) => {
    type(field("Name"), "oma");
    type(field("Passwort"), pw);
    type(field("Passwort wiederholen"), again);
  };
  return { ...r, api, setBusy, onClose, onDone, onReload, dialog, field, button, fill };
}

describe("AddSambaUserDialog", () => {
  it("sends the name and the password, and tells the page when it is done", async () => {
    const r = renderDialog({ "POST /users/samba": () => json({}) });
    r.fill("ge heim:ö");
    fireEvent.click(r.button("Anlegen"));
    await waitFor(() => expect(r.onDone).toHaveBeenCalledWith("Samba-Nutzer oma angelegt."));
    expect(r.api.sent("POST", "/users/samba")).toEqual([
      { method: "POST", path: "/users/samba", body: { name: "oma", password: "ge heim:ö" } },
    ]);
  });

  it("asks for the password twice, with new-password, and sends only when they match", () => {
    const r = renderDialog();
    for (const label of ["Passwort", "Passwort wiederholen"]) {
      expect(r.field(label).type).toBe("password");
      expect(r.field(label).autocomplete).toBe("new-password");
    }
    expect(r.getByText(/Nur für Freigaben: kein Login per SSH/)).toBeTruthy();
    expect(r.button("Anlegen").disabled).toBe(true);
    r.fill("geheim", "gehein");
    expect(within(r.dialog()).getByText("Die Passwörter stimmen nicht überein.")).toBeTruthy();
    expect(r.button("Anlegen").disabled).toBe(true);
    type(r.field("Passwort wiederholen"), "geheim");
    expect(within(r.dialog()).queryByText("Die Passwörter stimmen nicht überein.")).toBeNull();
    expect(r.button("Anlegen").disabled).toBe(false);
  });

  it("clears the passwords at once, when it succeeds and when it fails", async () => {
    let answer!: (res: Response) => void;
    const r = renderDialog({ "POST /users/samba": () => new Promise<Response>((res) => (answer = res)) });
    r.fill();
    fireEvent.click(r.button("Anlegen"));
    expect(r.field("Passwort").value).toBe("");
    expect(r.field("Passwort wiederholen").value).toBe("");
    answer(apiError(409, "exists", "oma has Samba access already"));
    await r.findByText("Es gibt schon eines mit diesem Namen.");
    expect(r.field("Passwort").value).toBe("");
    expect(r.field("Name").value).toBe("oma"); // the name stays for another try
    expect(r.onDone).not.toHaveBeenCalled();
  });

  it("never puts the password in the URL", async () => {
    const r = renderDialog({ "POST /users/samba": () => json({}) });
    r.fill("sehr-geheim");
    fireEvent.click(r.button("Anlegen"));
    await waitFor(() => expect(r.onDone).toHaveBeenCalled());
    expect(r.api.calls.every((c) => !c.path.includes("sehr-geheim"))).toBe(true);
  });

  it("shows the text of the job that dropped the old name, when that failed", async () => {
    const r = renderDialog({
      "POST /users/samba": () => apiError(500, "failed", "oma could not be dropped", { job: "J7" }),
      "GET /jobs/J7": () =>
        json(
          job({
            id: "J7",
            kind: "user-drop",
            target: "oma",
            status: "failed",
            code: "failed",
            error: "sharesec: boom",
            step: 1,
          }),
        ),
    });
    r.fill();
    fireEvent.click(r.button("Anlegen"));
    const alert = await within(r.dialog()).findByRole("alert");
    expect(alert.textContent).toContain("sharesec: boom");
    expect(r.api.sent("GET", "/jobs/J7").length).toBe(1);
  });

  it("reads a job only for a failed add, not for any refusal that names one", async () => {
    const r = renderDialog({
      "POST /users/samba": () => apiError(409, "exists", "oma has Samba access already", { job: "J7" }),
    });
    r.fill();
    fireEvent.click(r.button("Anlegen"));
    await r.findByText("Es gibt schon eines mit diesem Namen.");
    expect(r.api.sent("GET", "/jobs/J7")).toEqual([]);
  });

  it("falls back to the text of the refusal when that job cannot be read", async () => {
    const r = renderDialog({
      "POST /users/samba": () => apiError(500, "failed", "oma could not be dropped", { job: "J7" }),
    });
    r.fill();
    fireEvent.click(r.button("Anlegen"));
    expect((await within(r.dialog()).findByRole("alert")).textContent).toContain("oma could not be dropped");
  });

  it("shows an invalid name with the server's reason", async () => {
    const r = renderDialog({
      "POST /users/samba": () => apiError(400, "invalid_input", "bad name", { field: "name" }),
    });
    r.fill();
    fireEvent.click(r.button("Anlegen"));
    expect((await within(r.dialog()).findByRole("alert")).textContent).toBe(
      "Der Name geht so nicht: bad name",
    );
  });

  it("keeps what was typed, and the focus, when the page's state changes under it", () => {
    const r = renderDialog();
    r.fill("geheim", "gehe");
    const again = r.field("Passwort wiederholen");
    again.focus();
    r.setBusy(true);
    r.setBusy(false);
    expect(r.field("Passwort wiederholen")).toBe(again);
    expect(again.value).toBe("gehe");
    expect(r.field("Passwort").value).toBe("geheim");
    expect(document.activeElement).toBe(again);
    expect(r.api.calls).toEqual([]);
  });

  it("cannot be sent while a job runs in the page", () => {
    const r = renderDialog();
    r.fill();
    r.setBusy(true);
    expect(r.button("Anlegen").disabled).toBe(true);
  });
});
