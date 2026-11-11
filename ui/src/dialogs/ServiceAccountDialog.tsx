import { ApiError, useApi, useI18n } from "@edelweiss/sdk";
import { createSignal, Show } from "solid-js";
import type { AccountAnswer, OwnerView } from "../api";
import { Buttons } from "../Buttons";
import { errorText, jobErrorText, stepText, tSafe } from "../errors";
import { JobStatus, Modal } from "../Modal";
import { type Failure, useJob } from "../useJob";
import { CloseButton } from "./ChangeButtons";

/**
 * "Dienstkonto anlegen" for a folder whose owner has no account on the host (a disk from another computer): it names the
 * folder and the IDs, and makes the account smb-<uid> with exactly these IDs (POST /service-accounts {path}; no login, no
 * home, no Samba user; the files stay as they are). onChanged is told as soon as the account is there, so that the owner
 * is read again. When shares have that folder, the answer names the job that renders them again: the dialog follows it
 * (useJob) and is done (onDone, with the text of it) at its end; a job that ends without success leaves its failure and
 * Close alone, since the account is there. An owner that has an account already (exists: the line was read before it was
 * made) is said so, the owner is read again, and Close is left alone. Any other refusal stands in the dialog, and the
 * request can be sent again.
 */
export function ServiceAccountDialog(props: {
  path: string;
  owner: OwnerView;
  onClose: () => void;
  onDone: (message: string) => void;
  onChanged: () => void;
}) {
  const api = useApi();
  const { t } = useI18n();
  const job = useJob();
  const [busy, setBusy] = createSignal(false);
  const [failure, setFailure] = createSignal<Failure>();
  const [over, setOver] = createSignal(false);
  const step = () => {
    const st = job.running();
    return st ? stepText(st, t) : undefined;
  };
  const send = async () => {
    setBusy(true);
    setFailure(undefined);
    try {
      const answer = await api.post<AccountAnswer>("/service-accounts", { path: props.path });
      props.onChanged();
      const done = tSafe(t, "done.serviceAccount", { user: answer.user ?? "" });
      const st = answer.job && (await job.follow(answer.job));
      if (!answer.job || st?.status === "done") {
        props.onDone(done);
        return;
      }
      setOver(true);
      setFailure(
        st ? { text: jobErrorText(st, t), output: st.output } : { text: t("job.unknown"), neutral: true },
      );
    } catch (err) {
      if (err instanceof ApiError && err.code === "exists") {
        setOver(true);
        setFailure({ text: t("serviceAccount.exists") });
        props.onChanged();
      } else setFailure({ text: errorText(err, t) });
    } finally {
      setBusy(false);
    }
  };
  return (
    <Modal
      title={t("serviceAccount.title")}
      busy={busy()}
      onClose={props.onClose}
      actions={
        <Show when={!over()} fallback={<CloseButton onClose={props.onClose} />}>
          <Buttons
            busy={busy()}
            label={t("serviceAccount.button")}
            onClose={props.onClose}
            onConfirm={() => void send()}
          />
        </Show>
      }
    >
      <p>
        {tSafe(t, "serviceAccount.text", { path: props.path, uid: props.owner.uid, gid: props.owner.gid })}
      </p>
      <p>{t("serviceAccount.idNote")}</p>
      <JobStatus step={step()} failure={failure()} />
    </Modal>
  );
}
