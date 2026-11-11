import { useApi, useI18n } from "@edelweiss/sdk";
import { type Accessor, createEffect, createSignal, For, Show, untrack } from "solid-js";
import type { AccountAnswer, ViewResponse } from "../api";
import { tSafe } from "../errors";
import { keyed } from "../keys";
import { Callout, Field, JobStatus, Modal } from "../Modal";
import { useAccount } from "./account";
import { ChangeButtons } from "./ChangeButtons";

/**
 * "Host-Benutzer hinzufügen": gives a user of the host Samba access (POST /users/host {name, password}). The dropdown lists
 * the host users of the view without Samba access; one whose name Samba does not take is there but disabled, with "Name
 * nicht zulässig". The Linux password is checked by the server, which gives Samba the same; it is typed once, and cleared
 * the moment the request goes out, so no password stays in the page whatever the answer is. The options are by the name of
 * the user, so a new view every few seconds keeps the list and the choice.
 */
export function AddHostUserDialog(props: {
  view: Accessor<ViewResponse>;
  /** A job runs in the view. */
  busy: Accessor<boolean>;
  onClose: () => void;
  onDone: (message: string) => void;
  onReload: () => void;
}) {
  const api = useApi();
  const { t } = useI18n();
  const [picked, setPicked] = createSignal<string | undefined>(
    untrack(() => props.view().hostUsers.find((u) => u.selectable)?.name),
  );
  const [lost, setLost] = createSignal<string>();
  const [password, setPassword] = createSignal("");
  const account = useAccount({ onDone: props.onDone, onReload: props.onReload });
  const options = keyed(
    () => props.view().hostUsers,
    (u) => u.name,
  );
  /** The user that is chosen: the pick, which is never replaced by another user. */
  const chosen = picked;
  let select: HTMLSelectElement | undefined;
  // the select shows what is chosen, nothing when nobody is (a browser shows the first option of a select otherwise)
  createEffect(() => {
    const now = chosen() ?? "";
    options.keys();
    if (select) select.value = now;
  });
  // the picked user leaves the view (added elsewhere, or no host user any more): no choice is left, and the password
  // typed for that user is not kept for another
  createEffect(() => {
    const name = picked();
    if (name !== undefined && !props.view().hostUsers.some((u) => u.name === name && u.selectable)) {
      setLost(name);
      setPicked(undefined);
      setPassword("");
    }
  });
  const canSend = () => !props.busy() && chosen() !== undefined && password() !== "";
  const send = () => {
    const name = chosen();
    if (name === undefined) return;
    const body = { name, password: password() };
    setPassword("");
    void account.run(
      () => api.post<AccountAnswer>("/users/host", body),
      tSafe(t, "done.user-host", { name }),
    );
  };
  return (
    <Modal
      title={t("users.addHost")}
      busy={account.busy()}
      onClose={props.onClose}
      actions={
        <ChangeButtons
          change={account}
          disabled={!canSend()}
          label={t("account.add")}
          onClose={props.onClose}
          onConfirm={send}
        />
      }
    >
      <Show
        when={options.keys().length > 0}
        fallback={<p class="text-sm opacity-70">{t("account.noHostUsers")}</p>}
      >
        <Field label={t("account.hostUser")}>
          {(id) => (
            <select
              ref={select}
              id={id}
              class="select select-sm w-full"
              disabled={account.busy()}
              onChange={(e) => {
                setPicked(e.currentTarget.value);
                setLost(undefined);
              }}
            >
              <Show when={chosen() === undefined}>
                <option value="" disabled />
              </Show>
              <For each={options.keys()}>
                {(name) => {
                  const u = options.track(name);
                  return (
                    <option value={name} disabled={!u().selectable}>
                      {u().selectable ? name : `${name} – ${t("account.badName")}`}
                    </option>
                  );
                }}
              </For>
            </select>
          )}
        </Field>
      </Show>
      <Show when={lost()}>
        {(name) => (
          <p class="text-sm text-warning" role="status">
            {tSafe(t, "account.hostGone", { name: name() })}
          </p>
        )}
      </Show>
      <Field label={t("account.linuxPassword")}>
        {(id) => (
          <input
            id={id}
            type="password"
            class="input input-sm w-full"
            autocomplete="new-password"
            disabled={account.busy()}
            value={password()}
            onInput={(e) => setPassword(e.currentTarget.value)}
          />
        )}
      </Field>
      <Callout tone="info">{t("account.hostNote")}</Callout>
      <JobStatus step={account.step()} failure={account.failure()} />
    </Modal>
  );
}
