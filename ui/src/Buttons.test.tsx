import { cleanup, fireEvent } from "@solidjs/testing-library";
import { afterEach, describe, expect, it, vi } from "vitest";
import { Buttons } from "./Buttons";
import { renderExt } from "./test/render";

afterEach(cleanup);

const props = () => ({ label: "Stop", busy: false, onClose: vi.fn(), onConfirm: vi.fn() });

describe("Buttons", () => {
  it("has a Cancel that closes and a button of the change that confirms", () => {
    const p = props();
    const r = renderExt(() => <Buttons {...p} />);
    fireEvent.click(r.getByRole("button", { name: "Cancel" }));
    expect(p.onClose).toHaveBeenCalledOnce();
    expect(p.onConfirm).not.toHaveBeenCalled();
    fireEvent.click(r.getByRole("button", { name: "Stop" }));
    expect(p.onConfirm).toHaveBeenCalledOnce();
  });

  it("says Abbrechen in German", () => {
    const r = renderExt(() => <Buttons {...props()} label="Stoppen" />, undefined, "de");
    expect(r.getByRole("button", { name: "Abbrechen" })).toBeTruthy();
    expect(r.getByRole("button", { name: "Stoppen" })).toBeTruthy();
  });

  it("is red for a danger, blue for the rest", () => {
    const r = renderExt(() => (
      <>
        <Buttons {...props()} label="Plain" />
        <Buttons {...props()} label="Risky" danger />
      </>
    ));
    expect(r.getByRole("button", { name: "Plain" }).className).toContain("btn-primary");
    expect(r.getByRole("button", { name: "Risky" }).className).toContain("btn-error");
  });

  it("disables both buttons while busy", () => {
    const r = renderExt(() => <Buttons {...props()} busy />);
    expect((r.getByRole("button", { name: "Cancel" }) as HTMLButtonElement).disabled).toBe(true);
    expect((r.getByRole("button", { name: "Stop" }) as HTMLButtonElement).disabled).toBe(true);
  });

  it("disables the change alone when it is not possible yet, and leaves Cancel", () => {
    const r = renderExt(() => <Buttons {...props()} disabled />);
    expect((r.getByRole("button", { name: "Cancel" }) as HTMLButtonElement).disabled).toBe(false);
    expect((r.getByRole("button", { name: "Stop" }) as HTMLButtonElement).disabled).toBe(true);
  });
});
