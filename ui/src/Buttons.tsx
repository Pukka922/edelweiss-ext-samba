import { useI18n } from "@edelweiss/sdk";

/** The buttons of a dialog: cancel, and the change (red for a danger). */
export function Buttons(props: {
  busy: boolean;
  disabled?: boolean;
  label: string;
  danger?: boolean;
  onClose: () => void;
  onConfirm: () => void;
}) {
  const { t } = useI18n();
  return (
    <>
      <button type="button" class="btn btn-ghost" disabled={props.busy} onClick={() => props.onClose()}>
        {t("common.cancel")}
      </button>
      <button
        type="button"
        class={props.danger ? "btn btn-error" : "btn btn-primary"}
        disabled={props.busy || props.disabled}
        onClick={() => props.onConfirm()}
      >
        {props.label}
      </button>
    </>
  );
}
