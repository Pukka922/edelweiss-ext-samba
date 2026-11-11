import { useI18n } from "@edelweiss/sdk";
import { type Accessor, Show } from "solid-js";
import type { ShareView, ViewResponse } from "../api";
import { tSafe } from "../errors";
import { Callout, JobStatus, Modal } from "../Modal";
import { ChangeButtons } from "./ChangeButtons";
import { useChange } from "./change";

/**
 * The delete of a share, as a job (share-delete): only the share goes, its folder and the files in it stay, which the
 * dialog says. A share that the server does not have any more ends the change (Close alone, and the page reads the view
 * again); the dialog closes when the share is gone from the view, unless its own job runs.
 */
export function DeleteShareDialog(props: {
  share: ShareView;
  view: Accessor<ViewResponse>;
  /** A job runs in the view. */
  busy: Accessor<boolean>;
  onClose: () => void;
  onDone: (message: string) => void;
  onReload: () => void;
}) {
  const { t } = useI18n();
  const at = props.share;
  const change = useChange({
    onDone: props.onDone,
    onReload: props.onReload,
    leave: {
      gone: () => !props.view().config.shares.some((s) => s.name === at.name),
      onClose: props.onClose,
    },
  });
  const send = () =>
    void change.run({ kind: "share-delete", name: at.name }, () =>
      tSafe(t, "done.share-delete", { name: at.name }),
    );
  return (
    <Modal
      title={tSafe(t, "delete.title", { name: at.name })}
      busy={change.busy()}
      onClose={props.onClose}
      actions={
        <ChangeButtons
          change={change}
          danger
          disabled={props.busy()}
          label={t("actions.delete")}
          onClose={props.onClose}
          onConfirm={send}
        />
      }
    >
      <Callout tone="info">{tSafe(t, "delete.text", { path: at.path })}</Callout>
      <Show when={props.busy() && !change.busy()}>
        <p class="text-sm text-warning" role="status">
          {t("dialog.busy")}
        </p>
      </Show>
      <JobStatus step={change.step()} failure={change.failure()} />
    </Modal>
  );
}
