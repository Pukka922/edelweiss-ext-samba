import { useApi, useI18n } from "@edelweiss/sdk";
import { type Accessor, createSignal } from "solid-js";
import type { AccountAnswer, UserView, ViewResponse } from "../api";
import { tSafe } from "../errors";
import { JobStatus, Modal } from "../Modal";
import { useAccount } from "./account";
import { ChangeButtons } from "./ChangeButtons";
import { PasswordFields, passwordsMatch } from "./PasswordFields";

/**
 * "Passwort ändern": the Samba password of a user (POST /users/<name>/password {password}), typed twice, and cleared the
 * moment the request goes out. It is for a pure Samba user: a host user has one password, the Linux one, which
 * SyncPasswordDialog takes over. The dialog is about the user as the view had it at the click, and closes when the user is gone from the view.
 */
export function PasswordDialog(props: {
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
  const [repeat, setRepeat] = createSignal("");
  const account = useAccount({
    onDone: props.onDone,
    onReload: props.onReload,
    leave: {
      gone: () => props.view().usersKnown && !props.view().users.some((u) => u.name === name),
      onClose: props.onClose,
    },
  });
  const canSend = () => !props.busy() && passwordsMatch(password(), repeat());
  const send = () => {
    const body = { password: password() };
    setPassword("");
    setRepeat("");
    void account.run(
      () => api.post<AccountAnswer>(`/users/${encodeURIComponent(name)}/password`, body),
      tSafe(t, "done.user-password", { name }),
    );
  };
  return (
    <Modal
      title={tSafe(t, "account.changeTitle", { name })}
      busy={account.busy()}
      onClose={props.onClose}
      actions={
        <ChangeButtons
          change={account}
          disabled={!canSend()}
          label={t("account.change")}
          onClose={props.onClose}
          onConfirm={send}
        />
      }
    >
      <PasswordFields
        password={password()}
        repeat={repeat()}
        disabled={account.busy()}
        onPassword={setPassword}
        onRepeat={setRepeat}
      />
      <JobStatus step={account.step()} failure={account.failure()} />
    </Modal>
  );
}
