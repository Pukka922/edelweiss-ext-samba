import { ApiError } from "@edelweiss/sdk";
import { cleanup, fireEvent } from "@solidjs/testing-library";
import { createSignal, Show } from "solid-js";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { JobRequest, JobState } from "./api";
import { job } from "./test/fixtures";
import { apiError, json, renderExt, settle, stubApi } from "./test/render";
import { PageJob, POLL_MS, POLL_RETRIES, useJobRun } from "./useJob";

afterEach(() => {
  cleanup();
  vi.useRealTimers();
  vi.unstubAllGlobals();
});

const REQUEST: JobRequest = { kind: "setup" };

/** The job J9: a setup of samba with two steps, at its first one. */
const running = job({ id: "J9", kind: "setup", target: "samba", steps: ["prepare", "lbu"], step: 0 });

/** A button that runs REQUEST, and what the hook tells: busy, the step, the failure and its output. */
function Harness(props: {
  onDone: (message: string) => void;
  request?: JobRequest;
  onRefused?: (err: unknown) => boolean;
}) {
  const { busy, failure, step, run } = useJobRun(props.onDone, props.onRefused);
  return (
    <>
      <button
        type="button"
        onClick={() => void run(props.request ?? REQUEST, (st) => `${st.kind} of ${st.target} is done`)}
      >
        Run
      </button>
      <p>{busy() ? "busy" : "idle"}</p>
      <Show when={step()}>{(text) => <p data-testid="step">{text()}</p>}</Show>
      <Show when={failure()}>
        {(f) => (
          <div role={f().neutral ? "status" : "alert"}>
            <span>{f().text}</span>
            <Show when={f().output}>{(output) => <pre>{output()}</pre>}</Show>
          </div>
        )}
      </Show>
    </>
  );
}

/** The routes of J9: running at first, then what polls gives, one answer per GET (the last one stays). */
function routes(polls: (() => Response)[]) {
  let n = 0;
  return stubApi({
    "POST /jobs": () => json(running, 202),
    "GET /jobs/J9": () => polls[Math.min(n++, polls.length - 1)]!(),
  });
}

const at = (over: Partial<JobState>) => () => json({ ...running, ...over });
const failing = () => apiError(503, "failed", "the server is busy");
const noAnswer = (): Response => {
  throw new TypeError("Failed to fetch");
};

function render(onDone = vi.fn(), pageJob?: () => JobState | null | undefined) {
  const r = renderExt(() =>
    pageJob ? (
      <PageJob.Provider value={pageJob}>
        <Harness onDone={onDone} />
      </PageJob.Provider>
    ) : (
      <Harness onDone={onDone} />
    ),
  );
  fireEvent.click(r.getByRole("button", { name: "Run" }));
  return { ...r, onDone };
}

describe("useJobRun", () => {
  it("posts the request, asks for the job every second and is done with the text of the final state", async () => {
    vi.useFakeTimers();
    const api = routes([at({ step: 1 }), at({ status: "done", step: 1 })]);
    const r = render();
    await settle();
    expect(api.sent("POST", "/jobs").map((c) => c.body)).toEqual([REQUEST]);
    // busy while the job runs, with its step
    expect(r.getByText("busy")).toBeTruthy();
    expect(r.getByTestId("step").textContent).toBe("Step 1/2: prepare the files");
    expect(POLL_MS).toBe(1000);
    await vi.advanceTimersByTimeAsync(POLL_MS - 1);
    expect(api.sent("GET", "/jobs/J9")).toHaveLength(0);
    await vi.advanceTimersByTimeAsync(1);
    expect(api.sent("GET", "/jobs/J9")).toHaveLength(1);
    expect(r.getByTestId("step").textContent).toBe("Step 2/2: save the changes (lbu commit)");
    expect(r.onDone).not.toHaveBeenCalled();
    await vi.advanceTimersByTimeAsync(POLL_MS);
    expect(api.sent("GET", "/jobs/J9")).toHaveLength(2);
    expect(r.onDone).toHaveBeenCalledExactlyOnceWith("setup of samba is done");
    expect(r.getByText("idle")).toBeTruthy();
    expect(r.queryByRole("alert")).toBeNull();
    // a job that is done is not asked for any more
    await vi.advanceTimersByTimeAsync(5 * POLL_MS);
    expect(api.sent("GET", "/jobs/J9")).toHaveLength(2);
  });

  it("shows the failure of a job with the output of the failed command, and stays idle for another try", async () => {
    vi.useFakeTimers();
    routes([
      at({
        status: "failed",
        code: "failed",
        error: "smbd: exit status 1",
        output: "error: cannot open smb.conf\nerror: internal error",
      }),
    ]);
    const r = render();
    await settle();
    await vi.advanceTimersByTimeAsync(POLL_MS);
    expect(r.getByRole("alert").textContent).toContain(
      "Step 1/2 (prepare the files) failed: smbd: exit status 1",
    );
    expect(r.getByRole("alert").querySelector("pre")?.textContent).toBe(
      "error: cannot open smb.conf\nerror: internal error",
    );
    expect(r.onDone).not.toHaveBeenCalled();
    expect(r.getByText("idle")).toBeTruthy();
  });

  it("shows the failure of a job that was interrupted", async () => {
    vi.useFakeTimers();
    routes([at({ status: "interrupted", code: "interrupted" })]);
    const r = render();
    await settle();
    await vi.advanceTimersByTimeAsync(POLL_MS);
    expect(r.getByRole("alert").textContent).toBe(
      "The job was interrupted at step 1/2 (prepare the files). Check the state before you go on.",
    );
  });

  it("is done at once when the POST already answers with a final state", async () => {
    vi.useFakeTimers();
    const api = stubApi({ "POST /jobs": () => json({ ...running, status: "done" }, 202) });
    const r = render();
    await settle();
    expect(r.onDone).toHaveBeenCalledExactlyOnceWith("setup of samba is done");
    expect(api.sent("GET", "/jobs/J9")).toHaveLength(0);
  });

  it("shows why a request is refused, in the text of its code", async () => {
    vi.useFakeTimers();
    stubApi({ "POST /jobs": () => apiError(409, "busy", "another change is running; try again") });
    const r = render();
    await settle();
    expect(r.getByRole("alert").textContent).toBe("Another change is running – try again in a moment.");
    expect(r.onDone).not.toHaveBeenCalled();
    expect(r.getByText("idle")).toBeTruthy();
  });

  describe("onRefused", () => {
    /** Renders the harness with onRefused and clicks Run. */
    const renderRefused = (onRefused: (err: unknown) => boolean) => {
      const onDone = vi.fn();
      const r = renderExt(() => <Harness onDone={onDone} onRefused={onRefused} />);
      fireEvent.click(r.getByRole("button", { name: "Run" }));
      return { ...r, onDone };
    };

    it("is told the error of a refused request, and the failure is shown all the same when it says no", async () => {
      vi.useFakeTimers();
      stubApi({ "POST /jobs": () => apiError(409, "busy", "another change is running; try again") });
      const onRefused = vi.fn(() => false);
      const r = renderRefused(onRefused);
      await settle();
      expect(onRefused).toHaveBeenCalledOnce();
      const err = (onRefused.mock.calls[0] as unknown[])[0] as ApiError;
      expect(err).toBeInstanceOf(ApiError);
      expect(err.code).toBe("busy");
      expect(r.getByRole("alert").textContent).toBe("Another change is running – try again in a moment.");
      expect(r.getByText("idle")).toBeTruthy();
    });

    it("shows no failure when it says that it dealt with the refusal itself", async () => {
      vi.useFakeTimers();
      stubApi({ "POST /jobs": () => apiError(409, "exists", "there is such a thing already") });
      const r = renderRefused(() => true);
      await settle();
      expect(r.queryByRole("alert")).toBeNull();
      expect(r.getByText("idle")).toBeTruthy();
      expect(r.onDone).not.toHaveBeenCalled();
    });

    it("clears the failure of the try before when the next refusal is dealt with", async () => {
      vi.useFakeTimers();
      let refused = 0;
      stubApi({
        "POST /jobs": () =>
          ++refused === 1
            ? apiError(409, "busy", "another change is running")
            : apiError(409, "exists", "there is such a thing already"),
      });
      const r = renderRefused((err) => (err as ApiError).code === "exists");
      await settle();
      expect(r.getByRole("alert")).toBeTruthy();
      fireEvent.click(r.getByRole("button", { name: "Run" }));
      await settle();
      expect(r.queryByRole("alert")).toBeNull();
    });

    it("is not asked about a job that failed, or about a request that was not refused", async () => {
      vi.useFakeTimers();
      routes([at({ status: "failed", code: "failed", error: "exit status 1" })]);
      const onRefused = vi.fn(() => true);
      const r = renderRefused(onRefused);
      await settle();
      await vi.advanceTimersByTimeAsync(POLL_MS);
      expect(r.getByRole("alert").textContent).toContain("exit status 1");
      expect(onRefused).not.toHaveBeenCalled();
    });
  });

  describe("when a poll gets no answer", () => {
    it("asks again when a poll gets no answer or a 5xx, up to 5 times, and goes on with the answer", async () => {
      vi.useFakeTimers();
      expect(POLL_RETRIES).toBe(5);
      const api = routes([noAnswer, failing, noAnswer, failing, failing, at({ status: "done" })]);
      const r = render();
      await settle();
      for (let i = 1; i <= POLL_RETRIES; i++) {
        await vi.advanceTimersByTimeAsync(POLL_MS);
        expect(api.sent("GET", "/jobs/J9")).toHaveLength(i);
        // still at its step: no failure
        expect(r.getByTestId("step").textContent).toBe("Step 1/2: prepare the files");
        expect(r.queryByRole("alert")).toBeNull();
      }
      await vi.advanceTimersByTimeAsync(POLL_MS);
      expect(r.onDone).toHaveBeenCalledExactlyOnceWith("setup of samba is done");
    });

    it("counts only the failures in a row", async () => {
      vi.useFakeTimers();
      // 4 failures, an answer, 4 more failures: the second run of them is no fifth failure of the first
      const answer = at({ step: 0 });
      const api = routes([
        failing,
        failing,
        failing,
        failing,
        answer,
        failing,
        failing,
        failing,
        failing,
        at({ status: "done" }),
      ]);
      const r = render();
      await settle();
      await vi.advanceTimersByTimeAsync(10 * POLL_MS);
      expect(api.sent("GET", "/jobs/J9")).toHaveLength(10);
      expect(r.onDone).toHaveBeenCalledOnce();
      expect(r.queryByRole("alert")).toBeNull();
    });

    it("keeps waiting after the retries while the view of the page shows the job running", async () => {
      vi.useFakeTimers();
      let polls = 0;
      const [pageJob, setPageJob] = createSignal<JobState | null>(running);
      routes([() => (++polls > 8 ? json({ ...running, status: "done" }) : failing())]);
      const r = render(vi.fn(), pageJob);
      await settle();
      await vi.advanceTimersByTimeAsync((POLL_RETRIES + 3) * POLL_MS);
      expect(polls).toBe(POLL_RETRIES + 3);
      expect(r.getByTestId("step").textContent).toBe("Step 1/2: prepare the files");
      expect(r.queryByRole("alert")).toBeNull();
      expect(r.onDone).not.toHaveBeenCalled();
      // the server answers again: the job is done
      setPageJob(null);
      await vi.advanceTimersByTimeAsync(POLL_MS);
      expect(r.onDone).toHaveBeenCalledExactlyOnceWith("setup of samba is done");
    });

    it("takes the end of the job from the view of the page when it shows the job interrupted", async () => {
      vi.useFakeTimers();
      routes([failing]);
      const seen = job({
        id: "J9",
        status: "interrupted",
        code: "interrupted",
        steps: ["prepare", "lbu"],
        step: 1,
      });
      const r = render(vi.fn(), () => seen);
      await settle();
      await vi.advanceTimersByTimeAsync((POLL_RETRIES + 1) * POLL_MS);
      expect(r.getByRole("alert").textContent).toBe(
        "The job was interrupted at step 2/2 (save the changes (lbu commit)). Check the state before you go on.",
      );
    });

    it("says that the state is not known, with no failure, when the view does not have the job either", async () => {
      vi.useFakeTimers();
      for (const [what, polls, ticks] of [
        ["after the retries", [failing], POLL_RETRIES + 1],
        ["at once for a 404", [() => apiError(404, "not_found", "there is no such job")], 1],
      ] as const) {
        const api = routes([...polls]);
        const r = render();
        await settle();
        await vi.advanceTimersByTimeAsync(ticks * POLL_MS);
        expect(r.getByRole("status").textContent, what).toBe("Status unknown – reload");
        expect(r.queryByRole("alert"), what).toBeNull();
        expect(r.onDone, what).not.toHaveBeenCalled();
        expect(r.getByText("idle"), what).toBeTruthy();
        // it asks no more
        const asked = api.sent("GET", "/jobs/J9").length;
        await vi.advanceTimersByTimeAsync(5 * POLL_MS);
        expect(api.sent("GET", "/jobs/J9"), what).toHaveLength(asked);
        cleanup();
      }
    });
  });

  it("stops asking for the job when the component is gone", async () => {
    vi.useFakeTimers();
    const api = routes([at({ step: 0 })]);
    const r = render();
    await settle();
    await vi.advanceTimersByTimeAsync(POLL_MS);
    expect(api.sent("GET", "/jobs/J9")).toHaveLength(1);
    r.unmount();
    await vi.advanceTimersByTimeAsync(5 * POLL_MS);
    expect(api.sent("GET", "/jobs/J9")).toHaveLength(1);
    expect(r.onDone).not.toHaveBeenCalled();
  });
});
