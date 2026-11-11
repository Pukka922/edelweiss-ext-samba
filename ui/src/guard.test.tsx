import { fakeSession } from "@edelweiss/sdk/testing";
import { cleanup, fireEvent, waitFor } from "@solidjs/testing-library";
import { afterEach, describe, expect, it, vi } from "vitest";
import { useGuard } from "./guard";
import { adminOnRequest, renderExt } from "./test/render";

afterEach(cleanup);

/** A button that goes through the guard with open, which reads the view again with reload. */
function Harness(props: { open: () => void; reload: () => Promise<unknown> }) {
  const act = useGuard(() => props.reload());
  return (
    <button type="button" onClick={() => void act(props.open)}>
      Go
    </button>
  );
}

const reloaded = () => vi.fn(() => Promise.resolve());

describe("useGuard", () => {
  it("opens at once in admin mode, without asking for it and without reading the view again", async () => {
    const session = fakeSession({ admin: true, canAdmin: true });
    const open = vi.fn();
    const reload = reloaded();
    const r = renderExt(() => <Harness open={open} reload={reload} />, session);
    fireEvent.click(r.getByRole("button", { name: "Go" }));
    await waitFor(() => expect(open).toHaveBeenCalledOnce());
    expect(session.calls.requestAdmin).toBe(0);
    expect(reload).not.toHaveBeenCalled();
  });

  it("asks for admin mode first, and opens nothing and reads nothing while it stays off", async () => {
    const session = fakeSession({ admin: false, canAdmin: true });
    const open = vi.fn();
    const reload = reloaded();
    const r = renderExt(() => <Harness open={open} reload={reload} />, session);
    fireEvent.click(r.getByRole("button", { name: "Go" }));
    await waitFor(() => expect(session.calls.requestAdmin).toBe(1));
    await new Promise((resolve) => setTimeout(resolve, 10));
    expect(open).not.toHaveBeenCalled();
    expect(reload).not.toHaveBeenCalled();
  });

  it("reads the view again once admin mode is on, and opens only after that", async () => {
    const session = adminOnRequest();
    const order: string[] = [];
    let done: () => void = () => {};
    const reload = vi.fn(() => {
      order.push("reload");
      return new Promise<void>((resolve) => {
        done = resolve;
      });
    });
    const open = vi.fn(() => order.push("open"));
    const r = renderExt(() => <Harness open={open} reload={reload} />, session);
    fireEvent.click(r.getByRole("button", { name: "Go" }));
    expect(open).not.toHaveBeenCalled();
    await waitFor(() => expect(reload).toHaveBeenCalledOnce());
    expect(session.api.admin()).toBe(true);
    // the view is not there yet: nothing opens
    await new Promise((resolve) => setTimeout(resolve, 10));
    expect(open).not.toHaveBeenCalled();
    done();
    await waitFor(() => expect(open).toHaveBeenCalledOnce());
    expect(order).toEqual(["reload", "open"]);
  });

  it("opens nothing when the view cannot be read again", async () => {
    const session = adminOnRequest();
    const open = vi.fn();
    const reload = vi.fn(() => Promise.reject(new Error("cannot read the view")));
    const r = renderExt(() => <Harness open={open} reload={reload} />, session);
    fireEvent.click(r.getByRole("button", { name: "Go" }));
    await waitFor(() => expect(reload).toHaveBeenCalledOnce());
    await new Promise((resolve) => setTimeout(resolve, 10));
    expect(open).not.toHaveBeenCalled();
  });

  it("asks for admin mode every time it is off", async () => {
    const session = fakeSession({ admin: false, canAdmin: true });
    const open = vi.fn();
    const r = renderExt(() => <Harness open={open} reload={reloaded()} />, session);
    fireEvent.click(r.getByRole("button", { name: "Go" }));
    await waitFor(() => expect(session.calls.requestAdmin).toBe(1));
    fireEvent.click(r.getByRole("button", { name: "Go" }));
    await waitFor(() => expect(session.calls.requestAdmin).toBe(2));
    expect(open).not.toHaveBeenCalled();
  });
});
