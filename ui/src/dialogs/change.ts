import { type Accessor, createEffect, createSignal } from "solid-js";
import type { JobRequest, JobState } from "../api";
import { staleRefusal } from "../errors";
import { useJobRun } from "../useJob";

/**
 * The job of a dialog that changes a share (the editor, the delete): useJobRun, and the point where the change is
 * over, when nothing is left that the dialog can send. That is a refusal that says the page read the host before it changed
 * (staleRefusal: the same request would be refused again), a job that ended without success (a part of it may be done: a
 * second request could do it twice), and a job whose state is not known. A change that is over tells the page to read
 * the view again (onReload) and leaves the dialog Close alone (over). retry says of a job that ended without success that
 * the dialog may send the request again, when it is known to have undone what it did. running is the state of the job while it
 * runs, for the progress of a long one.
 *
 * A dialog about a share of the view gives leave: gone says the share is not in the view any more, and then the dialog
 * closes, unless its own job runs, whose first step may take the share from the view, or the change is over (the dialog stays
 * to say why). reset takes the change back, for a dialog
 * whose request is no harm when it is sent twice. onRefused is asked first about a refusal: it returns true when the dialog
 * shows it itself (at the field it is about), which leaves the change open and no failure text.
 */
export function useChange(props: {
  onDone: (message: string) => void;
  onReload: () => void;
  retry?: (st: JobState) => boolean;
  leave?: { gone: Accessor<boolean>; onClose: () => void };
  onRefused?: (err: unknown) => boolean;
}) {
  const {
    over,
    finish,
    reset: unfinish,
  } = useEnd({
    onReload: props.onReload,
    leave: props.leave,
    busy: () => job.busy(),
  });
  const job = useJobRun(props.onDone, (err) => {
    if (props.onRefused?.(err)) return true;
    if (staleRefusal(err)) finish();
    return false;
  });
  /** Sends the request; done is the text of the end of a job that is done. Resolves with the final state. */
  const run = async (req: JobRequest, done: (st: JobState) => string) => {
    const st = await job.run(req, done);
    if (st === undefined ? job.failure()?.neutral : st.status !== "done" && !props.retry?.(st)) finish();
    return st;
  };
  /**
   * For a request that does the same when it is sent again (a schedule is written whole): the change is not over any more, and the
   * failure is forgotten, so that a changed form can be sent. The page has been told to read the view again, and the form shows it.
   */
  const reset = () => {
    unfinish();
    job.dismiss();
  };
  return { busy: job.busy, failure: job.failure, step: job.step, running: job.running, over, run, reset };
}

export type Change = ReturnType<typeof useChange>;

/**
 * The end of a change, for a dialog whose request is its own (useChange, useAccount): over says that nothing is left that the
 * dialog can send, finish makes it so and tells the page to read the view again (onReload), reset takes it back. A dialog about
 * something of the view gives leave: gone says that it is not in the view any more, and then the dialog closes, unless its
 * own request runs (busy) or its change is over (it stays to say why).
 */
export function useEnd(props: {
  onReload: () => void;
  leave?: { gone: Accessor<boolean>; onClose: () => void };
  busy: Accessor<boolean>;
}) {
  const [over, setOver] = createSignal(false);
  const finish = () => {
    setOver(true);
    props.onReload();
  };
  const leave = props.leave;
  if (leave) {
    createEffect(() => {
      if (leave.gone() && !props.busy() && !over()) leave.onClose();
    });
  }
  return { over, finish, reset: () => setOver(false) };
}
