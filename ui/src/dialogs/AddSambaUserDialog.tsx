import { useApi, useI18n } from "@edelweiss/sdk";
import { type Accessor, createSignal } from "solid-js";
import type { AccountAnswer } from "../api";
import { tSafe } from "../errors";
import { Callout, Field, JobStatus, Modal } from "../Modal";
import { useAccount } from "./account";
import { ChangeButtons } from "./ChangeButtons";
import { PasswordFields, passwordsMatch } from "./PasswordFields";

/**
 * "Samba-Nutzer anlegen": a user of Samba alone (POST /users/samba {name, password}), with no login and no home. The
 * password is typed twice; both fields are cleared the moment the request goes out, so no password stays in the page
 * whatever the answer is (on a refusal it is typed again). The server may first wait for the job that drops an old user of
 * that name from the config; a failure of that job is shown with its own text.
 */
export function AddSambaUserDialog(props: {
  /** A job runs in the view. */
  busy: Accessor<boolean>;
  onClose: () => void;
  onDone: (message: string) => void;
  onReload: () => void;
}) {
  const api = useApi();
  const { t } = useI18n();
  const [name, setName] = createSignal("");
  const [password, setPassword] = createSignal("");
  const [repeat, setRepeat] = createSignal("");
  const account = useAccount({ onDone: props.onDone, onReload: props.onReload });
  const canSend = () => !props.busy() && name().trim() !== "" && passwordsMatch(password(), repeat());
  const send = () => {
    const body = { name: name().trim(), password: password() };
    setPassword("");
    setRepeat("");
    void account.run(
      () => api.post<AccountAnswer>("/users/samba", body),
      tSafe(t, "done.user-add", { name: body.name }),
    );
  };
  return (
    <Modal
      title={t("users.addSamba")}
      busy={account.busy()}
      onClose={props.onClose}
      actions={
        <ChangeButtons
          change={account}
          disabled={!canSend()}
          label={t("account.create")}
          onClose={props.onClose}
          onConfirm={send}
        />
      }
    >
      <Field label={t("account.name")}>
        {(id) => (
          <input
            id={id}
            type="text"
            class="input input-sm w-full"
            autocomplete="off"
            disabled={account.busy()}
            value={name()}
            onInput={(e) => setName(e.currentTarget.value)}
          />
        )}
      </Field>
      <PasswordFields
        password={password()}
        repeat={repeat()}
        disabled={account.busy()}
        onPassword={setPassword}
        onRepeat={setRepeat}
      />
      <Callout tone="info">{t("account.sambaNote")}</Callout>
      <JobStatus step={account.step()} failure={account.failure()} />
    </Modal>
  );
}
