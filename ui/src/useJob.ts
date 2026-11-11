import { ApiError, useApi, useI18n } from "@edelweiss/sdk";
import { type Accessor, createContext, createSignal, onCleanup, useContext } from "solid-js";
import type { JobRequest, JobState } from "./api";
import { errorText, jobErrorText, stepText } from "./errors";

/** How often a running job is asked for its state, in milliseconds. */
export const POLL_MS = 1000;

/** How often in a row a poll of a running job that gets no answer or a 5xx is tried again, one per POLL_MS. */
export const POLL_RETRIES = 5;

/**
 * The job of the page's view (view.job: running or interrupted, undefined when there is none), which a dialog falls
 * back to when GET /jobs/<id> cannot tell it the state of its job. Without a provider there is none.
 */
export const PageJob = createContext<Accessor<JobState | null | undefined>>(() => undefined);

const sleep = (ms: number) => new Promise<void>((resolve) => setTimeout(resolve, ms));

/** Whether a failed call may get an answer when it is asked again: no answer at all, or a failure of the server. */
export const transient = (err: unknown) => !(err instanceof ApiError) || err.status >= 500;

/**
 * Runs jobs: start POSTs the request and asks GET /jobs/<id> every second until the job is no longer running; it
 * resolves with the final state (rejects with the ApiError of a refused POST). A poll that gets no answer or a 5xx is
 * asked again, up to POLL_RETRIES times in a row; after that, and at once for any other answer (404: no such job), the
 * job of the page's view (PageJob) tells: while it is this job running, start keeps waiting; when it shows the job ended
 * (interrupted), that is the final state; when the view has no such job, start resolves with undefined: the state is
 * not known. follow does the same for a job that another request started (the answer of POST /service-accounts names
 * it). running is the state of the job while it runs. A component that is gone stops asking.
 */
export function useJob(): {
  start(req: JobRequest): Promise<JobState | undefined>;
  follow(st: JobState): Promise<JobState | undefined>;
  running: Accessor<JobState | undefined>;
} {
  const api = useApi();
  const pageJob = useContext(PageJob);
  const [running, setRunning] = createSignal<JobState>();
  let alive = true;
  onCleanup(() => {
    alive = false;
  });
  const follow = async (started: JobState): Promise<JobState | undefined> => {
    let st = started;
    let failed = 0; // the polls in a row that failed
    try {
      while (st.status === "running" && alive) {
        setRunning(st);
        await sleep(POLL_MS);
        if (!alive) break;
        try {
          st = await api.get<JobState>(`/jobs/${encodeURIComponent(st.id)}`);
          failed = 0;
        } catch (err) {
          if (transient(err) && ++failed <= POLL_RETRIES) continue;
          const seen = pageJob();
          if (seen?.id !== st.id) return undefined;
          if (seen.status !== "running") return seen;
          // the view shows it running: the next poll asks again
        }
      }
    } finally {
      setRunning(undefined);
    }
    return st;
  };
  const start = async (req: JobRequest) => follow(await api.post<JobState>("/jobs", req));
  return { start, follow, running };
}

/**
 * A job that ended without success: its text, and the output of the failed command. neutral: no failure, the state of
 * the job is not known (the text says to reload).
 */
export interface Failure {
  text: string;
  output?: string;
  neutral?: boolean;
}

/**
 * The job of a dialog: run starts it; busy while it runs (the POST included), step the text of the running step
 * ("Step 1/2: …"), running its state while it runs (a long job reports how far it is in it). A job that is done calls onDone
 * with the text of done(state); one that failed or was refused leaves its failure, one whose state is not known a neutral
 * text (the dialog stays open); dismiss forgets a failure that stands, and the next run does too. run resolves with the final
 * state, undefined when the request was refused or the state is not known. onRefused is told the error of a request that was
 * refused (the POST: a job that failed is no refusal); it returns true when the dialog dealt with it itself (for example by
 * asking for a confirmation), which leaves no failure text.
 */
export function useJobRun(onDone: (message: string) => void, onRefused?: (err: unknown) => boolean) {
  const { t } = useI18n();
  const job = useJob();
  const [busy, setBusy] = createSignal(false);
  const [failure, setFailure] = createSignal<Failure>();
  const step = () => {
    const st = job.running();
    return st ? stepText(st, t) : undefined;
  };
  const run = async (req: JobRequest, done: (st: JobState) => string): Promise<JobState | undefined> => {
    setBusy(true);
    setFailure(undefined);
    try {
      const st = await job.start(req);
      if (st === undefined) setFailure({ text: t("job.unknown"), neutral: true });
      else if (st.status === "done") onDone(done(st));
      else setFailure({ text: jobErrorText(st, t), output: st.output });
      return st;
    } catch (err) {
      if (!onRefused?.(err)) setFailure({ text: errorText(err, t) });
      return undefined;
    } finally {
      setBusy(false);
    }
  };
  return { busy, failure, step, running: job.running, run, dismiss: () => setFailure(undefined) };
}
