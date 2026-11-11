import { cleanup, fireEvent } from "@solidjs/testing-library";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { JobRequest } from "../api";
import { apiError, jobRoutes, renderExt, settle, stubApi } from "../test/render";
import { POLL_MS } from "../useJob";
import { JobConfirmDialog } from "./JobConfirmDialog";

afterEach(() => {
  cleanup();
  vi.useRealTimers();
  vi.unstubAllGlobals();
});

const REQUEST: JobRequest = { kind: "setup" };

function renderDialog(locale: "en" | "de" = "de") {
  const onClose = vi.fn();
  const onDone = vi.fn<(message: string) => void>();
  const r = renderExt(
    () => (
      <JobConfirmDialog
        title="Samba einrichten"
        text="Die Einrichtung schreibt die Konfiguration."
        label="Einrichten"
        danger
        request={REQUEST}
        done="Samba ist eingerichtet."
        onClose={onClose}
        onDone={onDone}
      />
    ),
    undefined,
    locale,
  );
  return { ...r, onClose, onDone };
}

describe("JobConfirmDialog", () => {
  it("asks first, and sends nothing until it is confirmed", async () => {
    vi.useFakeTimers();
    const api = jobRoutes("setup", "samba", ["prepare", "lbu"], [{ status: "done" }]);
    const r = renderDialog();
    expect(r.getByRole("dialog", { name: "Samba einrichten" })).toBeTruthy();
    expect(r.getByText("Die Einrichtung schreibt die Konfiguration.")).toBeTruthy();
    await settle();
    expect(api.calls).toHaveLength(0);
    const confirm = r.getByRole("button", { name: "Einrichten" });
    expect(confirm.className).toContain("btn-error");
  });

  it("cancels without sending", () => {
    const api = stubApi({});
    const r = renderDialog();
    fireEvent.click(r.getByRole("button", { name: "Abbrechen" }));
    expect(r.onClose).toHaveBeenCalledOnce();
    expect(api.calls).toHaveLength(0);
  });

  it("sends the request, shows the step, and tells the page the text when the job is done", async () => {
    vi.useFakeTimers();
    const api = jobRoutes("setup", "samba", ["prepare", "lbu"], [{ status: "done" }]);
    const r = renderDialog();
    fireEvent.click(r.getByRole("button", { name: "Einrichten" }));
    await settle();
    expect(api.sent("POST", "/jobs").map((c) => c.body)).toEqual([REQUEST]);
    expect(r.getByText("Schritt 1/2: Dateien vorbereiten")).toBeTruthy();
    expect((r.getByRole("button", { name: "Einrichten" }) as HTMLButtonElement).disabled).toBe(true);
    expect((r.getByRole("button", { name: "Abbrechen" }) as HTMLButtonElement).disabled).toBe(true);
    await vi.advanceTimersByTimeAsync(POLL_MS);
    expect(r.onDone).toHaveBeenCalledExactlyOnceWith("Samba ist eingerichtet.");
  });

  it("does not close with Escape while the job runs, and does when it is over", async () => {
    vi.useFakeTimers();
    jobRoutes("setup", "samba", ["prepare", "lbu"], [{ status: "failed", code: "failed", error: "boom" }]);
    const r = renderDialog();
    fireEvent.click(r.getByRole("button", { name: "Einrichten" }));
    await settle();
    fireEvent.keyDown(document, { key: "Escape" });
    expect(r.onClose).not.toHaveBeenCalled();
    await vi.advanceTimersByTimeAsync(POLL_MS);
    fireEvent.keyDown(document, { key: "Escape" });
    expect(r.onClose).toHaveBeenCalledOnce();
  });

  it("shows why a job failed, with the output, and can try again", async () => {
    vi.useFakeTimers();
    jobRoutes(
      "setup",
      "samba",
      ["prepare", "lbu"],
      [
        {
          status: "failed",
          code: "failed",
          step: 0,
          error: "smbcontrol reload-config: exit status 1",
          output: "error: cannot reload",
        },
      ],
    );
    const r = renderDialog();
    fireEvent.click(r.getByRole("button", { name: "Einrichten" }));
    await vi.advanceTimersByTimeAsync(POLL_MS);
    expect(r.getByRole("alert").textContent).toContain(
      "Schritt 1/2 (Dateien vorbereiten) ist fehlgeschlagen: smbcontrol reload-config: exit status 1",
    );
    expect(r.getByText("error: cannot reload")).toBeTruthy();
    expect(r.onDone).not.toHaveBeenCalled();
    expect((r.getByRole("button", { name: "Einrichten" }) as HTMLButtonElement).disabled).toBe(false);
  });

  it("words a refusal by what it names", async () => {
    stubApi({ "POST /jobs": () => apiError(409, "in_use", "x", { shares: ["web", "db"] }) });
    const r = renderDialog();
    fireEvent.click(r.getByRole("button", { name: "Einrichten" }));
    expect((await r.findByRole("alert")).textContent).toBe("Das wird noch benutzt von: web, db.");
    expect(r.onDone).not.toHaveBeenCalled();
  });
});
