import { useApi, useI18n } from "@edelweiss/sdk";
import { type Accessor, createMemo } from "solid-js";
import type { AccountAnswer, UserView, ViewResponse } from "../api";
import { tSafe } from "../errors";
import { formatList } from "../format";
import { Callout, JobStatus, Modal } from "../Modal";
import { useAccount } from "./account";
import { ChangeButtons } from "./ChangeButtons";

/**
 * "Löschen" for a pure Samba user, "Samba-Zugang entfernen" for a host user (DELETE /users/<name>): the Samba user goes,
 * and with it its account when it is one of the extension; the Linux account of a host user stays, which the dialog says.
 * It names the shares in which the user has rights (the rights of a folder only narrow those of its share, so the shares
 * name them all), from the view. The server answers with the job that takes the name from the rights, which the dialog
 * follows; when that job could not start (jobError), the dialog says that the delete is done and how the name leaves the
 * shares ("Verwaiste Namen entfernen" on the page). The dialog is about the user as the view had it at the click, and closes
 * when the user is gone from the view.
 */
export function DeleteUserDialog(props: {
  user: UserView;
  view: Accessor<ViewResponse>;
  /** A job runs in the view. */
  busy: Accessor<boolean>;
  onClose: () => void;
  onDone: (message: string) => void;
  onReload: () => void;
}) {
  const api = useApi();
  const { t, locale } = useI18n();
  const at = props.user;
  const host = at.kind === "host";
  const account = useAccount({
    onDone: props.onDone,
    onReload: props.onReload,
    leave: {
      gone: () => props.view().usersKnown && !props.view().users.some((u) => u.name === at.name),
      onClose: props.onClose,
    },
  });
  /** The names of the shares with a right of the user; a string, so that a view of the same content changes nothing. */
  const shares = createMemo(() =>
    formatList(
      props
        .view()
        .config.shares.filter((s) => (s.rights[at.name] ?? "") !== "")
        .map((s) => s.name),
      locale(),
    ),
  );
  const send = () =>
    void account.run(
      () => api.del<AccountAnswer>(`/users/${encodeURIComponent(at.name)}`),
      tSafe(t, host ? "done.user-drop" : "done.user-delete", { name: at.name }),
      (error) => tSafe(t, "account.jobError", { name: at.name, error }),
    );
  return (
    <Modal
      title={tSafe(t, host ? "account.dropTitle" : "account.deleteTitle", { name: at.name })}
      busy={account.busy()}
      onClose={props.onClose}
      actions={
        <ChangeButtons
          change={account}
          danger
          disabled={props.busy()}
          label={t(host ? "users.drop" : "users.delete")}
          onClose={props.onClose}
          onConfirm={send}
        />
      }
    >
      <Callout tone="info">
        {tSafe(t, host ? "account.dropText" : "account.deleteText", { name: at.name })}
      </Callout>
      <p class="text-sm">
        {shares() === ""
          ? t("account.deleteNoShares")
          : tSafe(t, "account.deleteShares", { shares: shares() })}
      </p>
      <JobStatus step={account.step()} failure={account.failure()} />
    </Modal>
  );
}
