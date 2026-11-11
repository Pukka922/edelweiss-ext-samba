import { cleanup, fireEvent, render, within } from "@solidjs/testing-library";
import { createSignal, Show } from "solid-js";
import { afterEach, describe, expect, it, vi } from "vitest";
import { Callout, Check, Field, JobStatus, Modal, Radio } from "./Modal";

afterEach(cleanup);

const pressEscape = () => fireEvent.keyDown(document, { key: "Escape" });

describe("Modal", () => {
  it("is a dialog named by its title, with its content and its actions", () => {
    const r = render(() => (
      <Modal title="Details: docs" onClose={() => {}} actions={<button type="button">Close</button>}>
        <p>the content</p>
      </Modal>
    ));
    const dialog = r.getByRole("dialog", { name: "Details: docs" });
    expect(dialog.getAttribute("aria-modal")).toBe("true");
    expect(within(dialog).getByRole("heading", { name: "Details: docs" })).toBeTruthy();
    expect(within(dialog).getByText("the content")).toBeTruthy();
    expect(within(dialog).getByRole("button", { name: "Close" })).toBeTruthy();
  });

  it("closes with Escape and with the backdrop, and with no other key", () => {
    const onClose = vi.fn();
    const r = render(() => (
      <Modal title="A" onClose={onClose} actions={null}>
        x
      </Modal>
    ));
    fireEvent.keyDown(document, { key: "Enter" });
    fireEvent.keyDown(document, { key: "a" });
    expect(onClose).not.toHaveBeenCalled();
    pressEscape();
    expect(onClose).toHaveBeenCalledTimes(1);
    fireEvent.click(r.container.querySelector(".modal-backdrop")!);
    expect(onClose).toHaveBeenCalledTimes(2);
  });

  it("is wider when asked to be", () => {
    const r = render(() => (
      <>
        <Modal title="Narrow" onClose={() => {}} actions={null}>
          x
        </Modal>
        <Modal title="Wide" wide onClose={() => {}} actions={null}>
          x
        </Modal>
      </>
    ));
    expect(
      within(r.getByRole("dialog", { name: "Narrow" }))
        .getByText("x")
        .closest(".modal-box")?.className,
    ).toBe("modal-box");
    expect(
      within(r.getByRole("dialog", { name: "Wide" }))
        .getByText("x")
        .closest(".modal-box")?.className,
    ).toContain("max-w-3xl");
  });

  describe("above another one", () => {
    /** Two modals: Above is on top, until its own onClose shuts it. */
    function Stack(props: { below: () => void; above: () => void }) {
      const [open, setOpen] = createSignal(true);
      return (
        <>
          <Modal title="Below" onClose={props.below} actions={null}>
            below
          </Modal>
          <Show when={open()}>
            <Modal
              title="Above"
              onClose={() => {
                props.above();
                setOpen(false);
              }}
              actions={null}
            >
              above
            </Modal>
          </Show>
        </>
      );
    }

    it("sends Escape to the topmost alone, and then to the one below", () => {
      const below = vi.fn();
      const above = vi.fn();
      const r = render(() => <Stack below={below} above={above} />);
      pressEscape();
      expect(above).toHaveBeenCalledTimes(1);
      expect(below).not.toHaveBeenCalled();
      expect(r.queryByRole("dialog", { name: "Above" })).toBeNull();
      pressEscape();
      expect(above).toHaveBeenCalledTimes(1);
      expect(below).toHaveBeenCalledTimes(1);
    });

    it("keeps the one below working when one that is not the topmost goes first", () => {
      const [first, setFirst] = createSignal(true);
      const closed: string[] = [];
      render(() => (
        <>
          <Show when={first()}>
            <Modal title="First" onClose={() => closed.push("first")} actions={null}>
              x
            </Modal>
          </Show>
          <Modal title="Second" onClose={() => closed.push("second")} actions={null}>
            x
          </Modal>
        </>
      ));
      setFirst(false);
      pressEscape();
      expect(closed).toEqual(["second"]);
    });

    it("hears nothing once the last one is gone", () => {
      const onClose = vi.fn();
      const [open, setOpen] = createSignal(true);
      render(() => (
        <Show when={open()}>
          <Modal title="A" onClose={onClose} actions={null}>
            x
          </Modal>
        </Show>
      ));
      setOpen(false);
      pressEscape();
      expect(onClose).not.toHaveBeenCalled();
      // a modal that opens later hears it again
      setOpen(true);
      pressEscape();
      expect(onClose).toHaveBeenCalledTimes(1);
    });
  });

  describe("while busy", () => {
    it("closes with neither Escape nor the backdrop, and closes again once it is not busy", () => {
      const onClose = vi.fn();
      const [busy, setBusy] = createSignal(true);
      const r = render(() => (
        <Modal title="A" busy={busy()} onClose={onClose} actions={null}>
          x
        </Modal>
      ));
      pressEscape();
      fireEvent.click(r.container.querySelector(".modal-backdrop")!);
      expect(onClose).not.toHaveBeenCalled();
      setBusy(false);
      pressEscape();
      expect(onClose).toHaveBeenCalledTimes(1);
      fireEvent.click(r.container.querySelector(".modal-backdrop")!);
      expect(onClose).toHaveBeenCalledTimes(2);
    });

    it("does not close the one below when the topmost is busy: Escape goes to the topmost alone", () => {
      const below = vi.fn();
      const above = vi.fn();
      render(() => (
        <>
          <Modal title="Below" onClose={below} actions={null}>
            below
          </Modal>
          <Modal title="Above" busy onClose={above} actions={null}>
            above
          </Modal>
        </>
      ));
      pressEscape();
      expect(above).not.toHaveBeenCalled();
      expect(below).not.toHaveBeenCalled();
    });
  });
});

describe("Field", () => {
  it("labels the control it is given, which the label points to", () => {
    const r = render(() => <Field label="Storage">{(id) => <select id={id} />}</Field>);
    expect(r.getByRole("combobox", { name: "Storage" })).toBeTruthy();
  });

  it("gives every field an id of its own", () => {
    const r = render(() => (
      <>
        <Field label="A">{(id) => <input id={id} />}</Field>
        <Field label="B">{(id) => <input id={id} />}</Field>
      </>
    ));
    expect(r.getByLabelText("A").id).not.toBe(r.getByLabelText("B").id);
  });
});

describe("Check", () => {
  it("is a checkbox named by its label, that reports what it is set to", () => {
    const onChange = vi.fn();
    const r = render(() => <Check label="I know" checked={false} onChange={onChange} />);
    const box = r.getByRole("checkbox", { name: "I know" }) as HTMLInputElement;
    fireEvent.click(box);
    expect(onChange).toHaveBeenLastCalledWith(true);
    fireEvent.click(box);
    expect(onChange).toHaveBeenLastCalledWith(false);
  });

  it("shows what it is given, and can be off", () => {
    const [checked, setChecked] = createSignal(true);
    const r = render(() => <Check label="I know" checked={checked()} disabled onChange={() => {}} />);
    const box = r.getByRole("checkbox") as HTMLInputElement;
    expect(box.checked).toBe(true);
    expect(box.disabled).toBe(true);
    setChecked(false);
    expect(box.checked).toBe(false);
  });
});

describe("Radio", () => {
  it("is one of a choice, named by its label, that tells the page when it is picked", () => {
    const [picked, setPicked] = createSignal("nat");
    const r = render(() => (
      <>
        <Radio name="net" label="NAT" checked={picked() === "nat"} onChange={() => setPicked("nat")} />
        <Radio
          name="net"
          label="Bridge"
          checked={picked() === "bridge"}
          onChange={() => setPicked("bridge")}
        />
      </>
    ));
    const nat = r.getByRole("radio", { name: "NAT" }) as HTMLInputElement;
    const bridge = r.getByRole("radio", { name: "Bridge" }) as HTMLInputElement;
    expect([nat.checked, bridge.checked]).toEqual([true, false]);
    fireEvent.click(bridge);
    expect(picked()).toBe("bridge");
    expect([nat.checked, bridge.checked]).toEqual([false, true]);
  });

  it("can be disabled", () => {
    const r = render(() => <Radio name="n" label="Bridge" checked={false} disabled onChange={() => {}} />);
    expect((r.getByRole("radio") as HTMLInputElement).disabled).toBe(true);
  });
});

describe("JobStatus", () => {
  it("shows the step that runs, with a spinner", () => {
    const r = render(() => <JobStatus step="Step 1/2: prepare" />);
    expect(r.getByText("Step 1/2: prepare")).toBeTruthy();
    expect(r.container.querySelector(".loading")).toBeTruthy();
    expect(r.queryByRole("alert")).toBeNull();
  });

  it("shows a failure as an alert, with the output of the failed command in a pre", () => {
    const r = render(() => (
      <JobStatus failure={{ text: "Step 1/1 failed", output: "error: cannot open smb.conf" }} />
    ));
    const alert = r.getByRole("alert");
    expect(alert.textContent).toContain("Step 1/1 failed");
    expect(alert.querySelector("pre")?.textContent).toBe("error: cannot open smb.conf");
  });

  it("shows a failure without output with no pre", () => {
    const r = render(() => <JobStatus failure={{ text: "Another change is running" }} />);
    expect(r.getByRole("alert").textContent).toBe("Another change is running");
    expect(r.container.querySelector("pre")).toBeNull();
  });

  it("says that the state of a job is not known as a status, not as an alert", () => {
    const r = render(() => <JobStatus failure={{ text: "Status unknown – reload", neutral: true }} />);
    expect(r.getByRole("status").textContent).toBe("Status unknown – reload");
    expect(r.queryByRole("alert")).toBeNull();
  });

  it("shows nothing when there is nothing to show", () => {
    const r = render(() => <JobStatus />);
    expect(r.container.textContent).toBe("");
  });

  describe("how far the step is", () => {
    it("is a bar with the percent, rounded, when the step says it (a long copy does)", () => {
      const r = render(() => <JobStatus step="Step 3/6: copy" progress={{ label: "copy", percent: 41.4 }} />);
      const bar = r.getByRole("progressbar") as HTMLProgressElement;
      expect(bar.value).toBe(41.4);
      expect(bar.max).toBe(100);
      expect(bar.getAttribute("aria-label")).toBe("Step 3/6: copy");
      expect(r.getByText("41 %")).toBeTruthy();
      expect(r.getByText("Step 3/6: copy")).toBeTruthy();
    });

    it("starts at 0, which is a value, not none", () => {
      const r = render(() => <JobStatus step="copy" progress={{ label: "copy", percent: 0 }} />);
      expect((r.getByRole("progressbar") as HTMLProgressElement).value).toBe(0);
      expect(r.getByText("0 %")).toBeTruthy();
    });

    it("is no bar when the step reports nothing, nor without a step", () => {
      expect(render(() => <JobStatus step="copy" />).queryByRole("progressbar")).toBeNull();
      cleanup();
      const r = render(() => <JobStatus progress={{ label: "copy", percent: 40 }} />);
      expect(r.queryByRole("progressbar")).toBeNull();
    });
  });
});

describe("Callout", () => {
  it("is its text in a box that is no alert: it stands there from the start", () => {
    const r = render(() => <Callout tone="warn">Mind this</Callout>);
    expect(r.getByText("Mind this")).toBeTruthy();
    expect(r.queryByRole("alert")).toBeNull();
    expect(r.queryByRole("status")).toBeNull();
  });

  it("has a colour for each of its tones", () => {
    const classes = (["ok", "warn", "bad", "info"] as const).map((tone) => {
      const r = render(() => <Callout tone={tone}>{tone}</Callout>);
      const box = r.getByText(tone);
      cleanup();
      return box.className;
    });
    expect(new Set(classes).size).toBe(4);
    expect(classes[0]).toContain("bg-success/10");
    expect(classes[1]).toContain("bg-warning/10");
    expect(classes[2]).toContain("bg-error/10");
  });
});
