import { useApi, useI18n } from "@edelweiss/sdk";
import { type Accessor, createSignal } from "solid-js";
import type { AccountAnswer, UserView, ViewResponse } from "../api";
import { tSafe } from "../errors";
import { Callout, Field, JobStatus, Modal } from "../Modal";
import { useAccount } from "./account";
import { ChangeButtons } from "./ChangeButtons";

/**
 * "Passwort angleichen" for a host user, who has one password, the Linux one: the dialog asks for it, once, and the server
 * checks it against /etc/shadow before Samba gets the same (POST /users/<name>/password {password}; wrong_password when it
 * is not the Linux password). It brings a user back in line whose Linux password changed outside the users extension,
 * without removing and adding them again, which would drop their rights. The field is cleared the moment the request goes
 * out. The dialog is about the user as the view had it at the click, and closes when the user is gone from the view.
 */
export function SyncPasswordDialog(props: {
  user: UserView;
  view: Accessor<ViewResponse>;
  /** A job runs in the view. */
  busy: Accessor<boolean>;
  onClose: () => void;
  onDone: (message: string) => void;
  onReload: () => void;
}) {
  const api = useApi();
  const { t } = useI18n();
  const name = props.user.name;
  const [password, setPassword] = createSignal("");
  const account = useAccount({
    onDone: props.onDone,
    onReload: props.onReload,
    leave: {
      gone: () => props.view().usersKnown && !props.view().users.some((u) => u.name === name),
      onClose: props.onClose,
    },
  });
  const canSend = () => !props.busy() && password() !== "";
  const send = () => {
    const body = { password: password() };
    setPassword("");
    void account.run(
      () => api.post<AccountAnswer>(`/users/${encodeURIComponent(name)}/password`, body),
      tSafe(t, "done.user-sync", { name }),
    );
  };
  return (
    <Modal
      title={tSafe(t, "account.syncTitle", { name })}
      busy={account.busy()}
      onClose={props.onClose}
      actions={
        <ChangeButtons
          change={account}
          disabled={!canSend()}
          label={t("account.sync")}
          onClose={props.onClose}
          onConfirm={send}
        />
      }
    >
      <Field label={t("account.linuxPassword")}>
        {(id) => (
          <input
            id={id}
            type="password"
            class="input input-sm w-full"
            autocomplete="current-password"
            disabled={account.busy()}
            value={password()}
            onInput={(e) => setPassword(e.currentTarget.value)}
          />
        )}
      </Field>
      <Callout tone="info">{t("account.syncText")}</Callout>
      <JobStatus step={account.step()} failure={account.failure()} />
    </Modal>
  );
}
