import { ApiError, type Translator, useApi, useI18n } from "@edelweiss/sdk";
import { type Accessor, createSignal } from "solid-js";
import type { AccountAnswer, JobState } from "../api";
import { errorText, jobErrorText, staleRefusal, stepText } from "../errors";
import { type Failure, useJob } from "../useJob";
import { useEnd } from "./change";

/**
 * The text of a refusal. A `failed` that names a job in its details (the add that first drops the old name of the user from
 * the config: that job did not end done) shows the failure of that job, as the server has it; when that cannot be read, the
 * text of the refusal.
 */
async function refusalText(
  err: unknown,
  t: Translator["t"],
  read: (id: string) => Promise<JobState>,
): Promise<string> {
  if (err instanceof ApiError && err.code === "failed" && typeof err.details.job === "string") {
    try {
      return jobErrorText(await read(err.details.job), t);
    } catch {
      // the refusal says enough
    }
  }
  return errorText(err, t);
}

/**
 * The request of a dialog about an account (the add, the password, the delete): not a job of its own, since the server
 * answers when it is done, but one of its answers may name a job that follows (the user-drop of a delete), which run
 * follows. busy while the request and that job run, step the text of the step of the job. A request that is done tells the
 * page (onDone, with the text); one that is refused leaves its text, and the request can be sent again. A refusal that
 * says the user is not there (not_found), and a job that ended without success (the account change is made then, and
 * a second request would do it twice), end the change: over, Close alone, and the page reads the view again (onReload). A
 * dialog about a user of the view gives leave: gone says the user is not in the view any more, and then the dialog closes,
 * unless its own request runs or its change is over (it stays to say why).
 */
export function useAccount(props: {
  onDone: (message: string) => void;
  onReload: () => void;
  leave?: { gone: Accessor<boolean>; onClose: () => void };
}) {
  const api = useApi();
  const { t } = useI18n();
  const job = useJob();
  const [busy, setBusy] = createSignal(false);
  const [failure, setFailure] = createSignal<Failure>();
  const { over, finish } = useEnd({ onReload: props.onReload, leave: props.leave, busy });
  const step = () => {
    const st = job.running();
    return st ? stepText(st, t) : undefined;
  };
  /**
   * Sends the request; done is the text for the page when it is done. An answer with jobError (the change is done, the job
   * that follows it could not start) ends the change with the text that noted makes of it: over, and the page reads the
   * view again.
   */
  const run = async (
    request: () => Promise<AccountAnswer | undefined>,
    done: string,
    noted: (jobError: string) => string = (e) => e,
  ) => {
    setBusy(true);
    setFailure(undefined);
    try {
      const answer = await request();
      if (answer?.jobError) {
        setFailure({ text: noted(answer.jobError), neutral: true });
        finish();
        return;
      }
      const st = answer?.job && (await job.follow(answer.job));
      if (!answer?.job || st?.status === "done") {
        props.onDone(done);
        return;
      }
      setFailure(
        st ? { text: jobErrorText(st, t), output: st.output } : { text: t("job.unknown"), neutral: true },
      );
      finish();
    } catch (err) {
      setFailure({
        text: await refusalText(err, t, (id) => api.get<JobState>(`/jobs/${encodeURIComponent(id)}`)),
      });
      if (staleRefusal(err)) finish();
    } finally {
      setBusy(false);
    }
  };
  return { busy, failure, step, over, run };
}

export type Account = ReturnType<typeof useAccount>;
