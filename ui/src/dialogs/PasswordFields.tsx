import { useI18n } from "@edelweiss/sdk";
import { createUniqueId, Show } from "solid-js";
import { Field } from "../Modal";

/**
 * A password typed twice: "Passwort" and "Passwort wiederholen", both new-password (the browser neither fills nor offers
 * to save them), with a hint where they differ. The values are the dialog's: it clears them when it sends, so nothing
 * stays in a signal after the request.
 */
export function PasswordFields(props: {
  password: string;
  repeat: string;
  disabled?: boolean;
  onPassword: (value: string) => void;
  onRepeat: (value: string) => void;
}) {
  const { t } = useI18n();
  const hint = createUniqueId();
  const differs = () => props.repeat !== "" && props.password !== props.repeat;
  return (
    <>
      <Field label={t("account.password")}>
        {(id) => (
          <input
            id={id}
            type="password"
            class="input input-sm w-full"
            autocomplete="new-password"
            disabled={props.disabled}
            value={props.password}
            onInput={(e) => props.onPassword(e.currentTarget.value)}
          />
        )}
      </Field>
      <Field label={t("account.repeat")}>
        {(id) => (
          <>
            <input
              id={id}
              type="password"
              class="input input-sm w-full"
              autocomplete="new-password"
              disabled={props.disabled}
              aria-describedby={differs() ? hint : undefined}
              value={props.repeat}
              onInput={(e) => props.onRepeat(e.currentTarget.value)}
            />
            <Show when={differs()}>
              <p id={hint} class="text-xs text-error">
                {t("account.mismatch")}
              </p>
            </Show>
          </>
        )}
      </Field>
    </>
  );
}

/** Whether the two passwords are the same and not empty: what can be sent. */
export const passwordsMatch = (password: string, repeat: string) => password !== "" && password === repeat;
