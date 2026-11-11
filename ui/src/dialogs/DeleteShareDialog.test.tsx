import { cleanup, fireEvent, within } from "@solidjs/testing-library";
import { createSignal } from "solid-js";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { ViewResponse } from "../api";
import { share, shares, view } from "../test/fixtures";
import { apiError, type Handler, jobRoutes, renderExt, settle, stubApi } from "../test/render";
import { DeleteShareDialog } from "./DeleteShareDialog";

afterEach(() => {
  cleanup();
  vi.useRealTimers();
  vi.unstubAllGlobals();
});

function renderDelete(opts: { routes?: Record<string, Handler>; job?: boolean; busy?: boolean } = {}) {
  const api = opts.job
    ? jobRoutes(
        "share-delete",
        "Fotos",
        ["prepare", "check", "config"],
        [{ status: "done", step: 2 }],
        opts.routes,
      )
    : stubApi(opts.routes ?? {});
  const [v, setV] = createSignal<ViewResponse>(view());
  const onClose = vi.fn();
  const onDone = vi.fn();
  const onReload = vi.fn();
  const r = renderExt(
    () => (
      <DeleteShareDialog
        share={share()}
        view={v}
        busy={() => opts.busy ?? false}
        onClose={onClose}
        onDone={onDone}
        onReload={onReload}
      />
    ),
    undefined,
    "de",
  );
  const dialog = () => r.getByRole("dialog", { name: "Freigabe Fotos löschen" });
  return { ...r, api, setV, onClose, onDone, onReload, dialog };
}

describe("DeleteShareDialog", () => {
  it("says that the folder and its files stay, and deletes the share as a job", async () => {
    vi.useFakeTimers();
    const r = renderDelete({ job: true });
    expect(r.dialog().textContent).toContain(
      "Nur die Freigabe geht: Der Ordner /mnt/daten/Fotos und seine Dateien bleiben.",
    );
    fireEvent.click(within(r.dialog()).getByRole("button", { name: "Löschen" }));
    await settle();
    expect(r.api.sent("POST", "/jobs").map((c) => c.body)).toEqual([{ kind: "share-delete", name: "Fotos" }]);
    await vi.advanceTimersByTimeAsync(1000);
    expect(r.onDone).toHaveBeenCalledWith("Freigabe Fotos gelöscht.");
  });

  it("closes when the share is gone from the view", () => {
    const r = renderDelete();
    r.setV(view({ config: { ...view().config, shares: shares().slice(1) } }));
    expect(r.onClose).toHaveBeenCalled();
  });

  it("is over when the server has no such share any more: Close alone, and the view is read again", async () => {
    const r = renderDelete({
      routes: {
        "POST /jobs": () => apiError(404, "not_found", 'there is no share "Fotos"', { share: "Fotos" }),
      },
    });
    fireEvent.click(within(r.dialog()).getByRole("button", { name: "Löschen" }));
    expect((await within(r.dialog()).findByRole("alert")).textContent).toBe(
      "Das gibt es nicht mehr – lade neu.",
    );
    expect(r.onReload).toHaveBeenCalled();
    expect(within(r.dialog()).queryByRole("button", { name: "Löschen" })).toBeNull();
    expect(within(r.dialog()).getByRole("button", { name: "Schließen" })).toBeTruthy();
  });

  it("waits while another change runs", () => {
    const r = renderDelete({ busy: true });
    expect((within(r.dialog()).getByRole("button", { name: "Löschen" }) as HTMLButtonElement).disabled).toBe(
      true,
    );
    expect(
      within(r.dialog()).getByText("Gerade läuft eine andere Änderung – warte, bis sie fertig ist."),
    ).toBeTruthy();
  });
});
