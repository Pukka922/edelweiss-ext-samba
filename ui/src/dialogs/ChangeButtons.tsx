import { useI18n } from "@edelweiss/sdk";
import { Show } from "solid-js";
import { Buttons } from "../Buttons";
import type { Change } from "./change";

/** Close alone: what a dialog offers when its change is over and nothing is left to send. */
export function CloseButton(props: { onClose: () => void }) {
  const { t } = useI18n();
  return (
    <button type="button" class="btn btn-primary" onClick={() => props.onClose()}>
      {t("common.close")}
    </button>
  );
}

/**
 * The buttons of a dialog that sends one change: Cancel and the button of the change (red for a danger, disabled while
 * the job runs or when disabled says so), and Close alone once the change is over. detach is for a change that takes hours
 * (a copy of many files): while its job runs the dialog can be closed, with Close in place of Cancel, and the job goes on (the row of
 * the share shows how far it is, and cancels it).
 */
export function ChangeButtons(props: {
  change: Pick<Change, "over" | "busy">;
  label: string;
  danger?: boolean;
  disabled?: boolean;
  detach?: boolean;
  onClose: () => void;
  onConfirm: () => void;
}) {
  const { t } = useI18n();
  return (
    <Show when={!props.change.over()} fallback={<CloseButton onClose={props.onClose} />}>
      <Show
        when={props.detach && props.change.busy()}
        fallback={
          <Buttons
            busy={props.change.busy()}
            danger={props.danger}
            disabled={props.disabled}
            label={props.label}
            onClose={props.onClose}
            onConfirm={props.onConfirm}
          />
        }
      >
        <button type="button" class="btn btn-ghost" onClick={() => props.onClose()}>
          {t("common.close")}
        </button>
        <button type="button" class={props.danger ? "btn btn-error" : "btn btn-primary"} disabled>
          {props.label}
        </button>
      </Show>
    </Show>
  );
}
