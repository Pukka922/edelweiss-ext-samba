import { cleanup, fireEvent, render } from "@solidjs/testing-library";
import { createSignal, Show } from "solid-js";
import { afterEach, describe, expect, it } from "vitest";
import { Behind, Modal, modalLocked, modalOpen } from "./Modal";

afterEach(cleanup);

/** Whether the element is inert, or inside something that is. */
const inert = (el: Element | null) => el?.closest("[inert]") !== null && el?.closest("[inert]") !== undefined;

describe("Modal: what is behind it", () => {
  it("makes the page behind it inert while it is open, and only then", () => {
    const [open, setOpen] = createSignal(false);
    const r = render(() => (
      <div>
        <Behind>
          <button type="button">behind</button>
        </Behind>
        <Show when={open()}>
          <Modal title="A" onClose={() => setOpen(false)} actions={<button type="button">in front</button>}>
            x
          </Modal>
        </Show>
      </div>
    ));
    const behind = r.getByRole("button", { name: "behind" });
    expect(inert(behind)).toBe(false);
    expect(modalOpen()).toBe(false);
    setOpen(true);
    expect(inert(behind)).toBe(true);
    expect(inert(r.getByRole("button", { name: "in front" }))).toBe(false);
    expect(modalOpen()).toBe(true);
    // the wrapper is no box of its own: the layout of the page stays
    expect(behind.parentElement?.className).toBe("contents");
    fireEvent.keyDown(document, { key: "Escape" });
    expect(open()).toBe(false);
    expect(inert(behind)).toBe(false);
    expect(modalOpen()).toBe(false);
  });

  it("makes a modal inert while another one is open above it, and gives it back once that closes", () => {
    const [confirm, setConfirm] = createSignal(false);
    const r = render(() => (
      <>
        <Modal title="Content" onClose={() => {}} actions={<button type="button">delete</button>}>
          x
        </Modal>
        <Show when={confirm()}>
          <Modal
            title="Confirm"
            onClose={() => setConfirm(false)}
            actions={<button type="button">yes</button>}
          >
            y
          </Modal>
        </Show>
      </>
    ));
    const lower = r.getByRole("button", { name: "delete" });
    expect(inert(lower)).toBe(false);
    setConfirm(true);
    expect(inert(lower)).toBe(true);
    expect(inert(r.getByRole("button", { name: "yes" }))).toBe(false);
    setConfirm(false);
    expect(inert(lower)).toBe(false);
  });

  it("keeps a modal that has another inside it usable: the inner one is in it", () => {
    const r = render(() => (
      <Modal title="Outer" onClose={() => {}} actions={null}>
        <Modal title="Inner" onClose={() => {}} actions={<button type="button">inner</button>}>
          z
        </Modal>
      </Modal>
    ));
    expect(inert(r.getByRole("button", { name: "inner" }))).toBe(false);
  });

  it("says that an open modal is busy, so that the page does not replace it", () => {
    const [busy, setBusy] = createSignal(false);
    const [open, setOpen] = createSignal(true);
    render(() => (
      <Show when={open()}>
        <Modal title="Bridge" busy={busy()} onClose={() => {}} actions={null}>
          x
        </Modal>
      </Show>
    ));
    expect(modalLocked()).toBe(false);
    setBusy(true);
    expect(modalLocked()).toBe(true);
    setOpen(false);
    expect(modalLocked()).toBe(false);
  });
});
