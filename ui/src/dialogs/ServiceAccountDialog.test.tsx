import { cleanup, fireEvent, within } from "@solidjs/testing-library";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { OwnerView } from "../api";
import { browse, browseRoute, job, view } from "../test/fixtures";
import { apiError, type Handler, json, renderExt, settle, stubApi, type } from "../test/render";
import { ServiceAccountDialog } from "./ServiceAccountDialog";
import { ShareDialog } from "./ShareDialog";

afterEach(() => {
  cleanup();
  vi.useRealTimers();
  vi.unstubAllGlobals();
});

const PATH = "/mnt/usb1/alt";
const nobody: OwnerView = { uid: 1001, gid: 1001, user: "", group: "" };
const account: OwnerView = { uid: 1001, gid: 1001, user: "smb-1001", group: "smb-1001" };

/**
 * The share dialog on a folder whose owner has no account, until the service account is there (made): the owner of the
 * folder that GET /browse gives then.
 */
function renderFlow(serviceAccount: Handler, more: Record<string, Handler> = {}) {
  let made = false;
  const api = stubApi({
    [browseRoute(PATH)]: () => json(browse(PATH, [], { owner: made ? account : nobody })),
    "POST /service-accounts": (body) => {
      made = true;
      return serviceAccount(body);
    },
    ...more,
  });
  const onChanged = vi.fn();
  const onReload = vi.fn();
  const r = renderExt(
    () => (
      <ShareDialog
        view={() => view()}
        busy={() => false}
        onClose={() => {}}
        onDone={() => {}}
        onChanged={onChanged}
        onReload={onReload}
      />
    ),
    undefined,
    "de",
  );
  const share = () => r.getByRole("dialog", { name: "Freigabe anlegen" });
  return { ...r, api, onChanged, onReload, share };
}

describe("the service-account flow", () => {
  it("offers Dienstkonto anlegen for a folder whose owner has no account, makes it, and names the new owner", async () => {
    vi.useFakeTimers();
    const r = renderFlow(() => json({ user: "smb-1001", group: "smb-1001" }));
    type(within(r.share()).getByLabelText("Name (so heißt sie im Netzwerk)"), "Alt");
    type(within(r.share()).getByLabelText("Ordner"), PATH);
    await vi.advanceTimersByTimeAsync(300);
    expect(
      within(r.share()).getByText(
        "Der Ordner gehört UID 1001 und GID 1001, für die es auf diesem Host kein Konto gibt (etwa eine Platte von einem anderen Rechner).",
      ),
    ).toBeTruthy();
    expect((within(r.share()).getByRole("button", { name: "Anlegen" }) as HTMLButtonElement).disabled).toBe(
      true,
    );
    fireEvent.click(within(r.share()).getByRole("button", { name: "Dienstkonto anlegen" }));
    const dialog = r.getByRole("dialog", { name: "Dienstkonto anlegen" });
    expect(dialog.textContent).toContain(
      "Die Extension legt das Dienstkonto smb-1001 mit genau diesen IDs an: ohne Login, ohne Home-Verzeichnis, kein Samba-Nutzer. An den Dateien ändert sich nichts.",
    );
    fireEvent.click(within(dialog).getByRole("button", { name: "Anlegen" }));
    await settle();
    expect(r.api.sent("POST", "/service-accounts").map((c) => c.body)).toEqual([{ path: PATH }]);
    expect(r.onChanged).toHaveBeenCalledWith("Dienstkonto smb-1001 angelegt.");
    expect(r.queryByRole("dialog", { name: "Dienstkonto anlegen" })).toBeNull();
    await settle();
    expect(r.api.sent("GET", `/browse?dir=${encodeURIComponent(PATH)}`)).toHaveLength(2);
    expect(
      within(r.share()).getByText(
        "Samba greift als Besitzer des Ordners (smb-1001:smb-1001) auf die Dateien zu; an bestehenden Dateien ändert sich nichts.",
      ),
    ).toBeTruthy();
    expect((within(r.share()).getByRole("button", { name: "Anlegen" }) as HTMLButtonElement).disabled).toBe(
      false,
    );
  });

  it("follows the job that renders the shares of that folder again, and is done only at its end", async () => {
    vi.useFakeTimers();
    const first = job({
      id: "J9",
      kind: "service-account",
      target: PATH,
      steps: ["prepare", "check", "config"],
    });
    let polls = 0;
    const r = renderFlow(() => json({ job: first, user: "smb-1001", group: "smb-1001" }), {
      "GET /jobs/J9": () =>
        json(polls++ === 0 ? { ...first, step: 2 } : { ...first, status: "done", step: 2 }),
    });
    type(within(r.share()).getByLabelText("Ordner"), PATH);
    await vi.advanceTimersByTimeAsync(300);
    fireEvent.click(within(r.share()).getByRole("button", { name: "Dienstkonto anlegen" }));
    const dialog = r.getByRole("dialog", { name: "Dienstkonto anlegen" });
    fireEvent.click(within(dialog).getByRole("button", { name: "Anlegen" }));
    await settle();
    expect(within(dialog).getByText("Schritt 1/3: Dateien vorbereiten")).toBeTruthy();
    expect(r.onChanged).not.toHaveBeenCalled();
    await vi.advanceTimersByTimeAsync(1000);
    expect(within(dialog).getByText("Schritt 3/3: Konfiguration schreiben")).toBeTruthy();
    await vi.advanceTimersByTimeAsync(1000);
    expect(r.api.sent("GET", "/jobs/J9")).toHaveLength(2);
    expect(r.onChanged).toHaveBeenCalledWith("Dienstkonto smb-1001 angelegt.");
  });

  it("refreshes the owner even when the job fails, and shows why", async () => {
    vi.useFakeTimers();
    const first = job({ id: "J9", kind: "service-account", target: PATH, steps: ["prepare", "check"] });
    const r = renderFlow(() => json({ job: first, user: "smb-1001", group: "smb-1001" }), {
      "GET /jobs/J9": () =>
        json({ ...first, status: "failed", code: "failed", step: 1, error: "testparm: exit 1" }),
    });
    type(within(r.share()).getByLabelText("Ordner"), PATH);
    await vi.advanceTimersByTimeAsync(300);
    fireEvent.click(within(r.share()).getByRole("button", { name: "Dienstkonto anlegen" }));
    const dialog = r.getByRole("dialog", { name: "Dienstkonto anlegen" });
    fireEvent.click(within(dialog).getByRole("button", { name: "Anlegen" }));
    await vi.advanceTimersByTimeAsync(1000);
    expect(within(dialog).getByRole("alert").textContent).toContain(
      "Schritt 2/2 (Konfiguration prüfen (testparm)) ist fehlgeschlagen: testparm: exit 1",
    );
    expect(r.onReload).toHaveBeenCalled();
    expect(r.onChanged).not.toHaveBeenCalled();
    await settle();
    expect(r.api.sent("GET", `/browse?dir=${encodeURIComponent(PATH)}`)).toHaveLength(2);
    // the account is there: nothing is left to send
    expect(within(dialog).queryByRole("button", { name: "Anlegen" })).toBeNull();
  });
});

describe("ServiceAccountDialog", () => {
  function renderDialog(routes: Record<string, Handler>) {
    const api = stubApi(routes);
    const onClose = vi.fn();
    const onDone = vi.fn();
    const onChanged = vi.fn();
    const r = renderExt(
      () => (
        <ServiceAccountDialog
          path={PATH}
          owner={nobody}
          onClose={onClose}
          onDone={onDone}
          onChanged={onChanged}
        />
      ),
      undefined,
      "de",
    );
    return { ...r, api, onClose, onDone, onChanged };
  }

  it("names the folder and its IDs, and says that new users do not get the ID afterwards", () => {
    const r = renderDialog({});
    expect(r.getByRole("dialog").textContent).toContain(
      `Der Ordner ${PATH} gehört UID 1001 und GID 1001, für die es auf diesem Host kein Konto gibt.`,
    );
    expect(r.getByRole("dialog").textContent).toContain(
      "Das Konto bekommt genau diese ID; neue Benutzer bekommen sie danach nicht mehr.",
    );
  });

  it("names a group of the system that the account cannot join", async () => {
    const r = renderDialog({
      "POST /service-accounts": () =>
        apiError(409, "system_account", 'the group "wheel" (GID 10) is a group of the system', {
          field: "gid",
          group: "wheel",
        }),
    });
    fireEvent.click(r.getByRole("button", { name: "Anlegen" }));
    expect((await r.findByRole("alert")).textContent).toBe(
      "Die Gruppe wheel ist eine Gruppe des Systems: Ein Dienstkonto kommt nur in die Gruppe users oder in eine Gruppe mit einer GID von 1000 bis 65532.",
    );
    expect(r.onChanged).not.toHaveBeenCalled();
  });

  it("shows a refusal, and the request can be sent again", async () => {
    const r = renderDialog({
      "POST /service-accounts": () => apiError(409, "root_owned", `${PATH} belongs to root`),
    });
    fireEvent.click(r.getByRole("button", { name: "Anlegen" }));
    expect((await r.findByRole("alert")).textContent).toBe(
      "Der Ordner gehört root, und root hat schon ein Konto: Dafür wird kein Dienstkonto angelegt.",
    );
    expect(r.onChanged).not.toHaveBeenCalled();
    expect((r.getByRole("button", { name: "Anlegen" }) as HTMLButtonElement).disabled).toBe(false);
  });

  it("says that the owner has an account already, reads the owner again, and leaves Close alone", async () => {
    const r = renderDialog({
      "POST /service-accounts": () =>
        apiError(409, "exists", `the owner ben:users of ${PATH} has an account`, { field: "path" }),
    });
    fireEvent.click(r.getByRole("button", { name: "Anlegen" }));
    expect((await r.findByRole("alert")).textContent).toBe("Der Besitzer des Ordners hat schon ein Konto.");
    expect(r.onChanged).toHaveBeenCalledOnce();
    expect(r.onDone).not.toHaveBeenCalled();
    expect(r.queryByRole("button", { name: "Anlegen" })).toBeNull();
    fireEvent.click(r.getByRole("button", { name: "Schließen" }));
    expect(r.onClose).toHaveBeenCalledOnce();
  });

  it("closes with Abbrechen", () => {
    const r = renderDialog({});
    fireEvent.click(r.getByRole("button", { name: "Abbrechen" }));
    expect(r.onClose).toHaveBeenCalledOnce();
  });
});
