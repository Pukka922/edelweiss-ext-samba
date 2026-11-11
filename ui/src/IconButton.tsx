import type { Component } from "solid-js";
import { Dynamic } from "solid-js/web";

/** What IconButton passes to an icon: a Tabler icon takes these (and more). */
export type IconProps = { size?: number | string; strokeWidth?: number | string };

/**
 * A small button that shows only an icon, with its text as a tooltip. label is its accessible name (for example
 * "Stop web"), tooltip the visible text ("Stop"). The icon is hidden from assistive technology: the label names the
 * button.
 *
 * The tooltip opens to the left and stays on one line: the buttons sit in the last column of a table inside an
 * overflow-x-auto box, and daisyUI lays an invisible tooltip out all the time. One centered above the button would
 * reach over the right edge of the box and give every table a horizontal scrollbar; a tooltip that wraps to two lines
 * (daisyUI wraps at 20rem) hangs below the button and makes the box scroll vertically when the button is in its last
 * row.
 *
 * A disabled danger button is not red, nor a disabled ok button green: text-error and text-success would override the
 * dimmed color of a disabled button, and the red trash of an item in use would look clickable.
 */
export function IconButton(props: {
  icon: Component<IconProps>;
  label: string;
  tooltip: string;
  onClick: () => void;
  disabled?: boolean;
  danger?: boolean;
  /** Green: the button starts something. */
  ok?: boolean;
  /** The title of the button: a text that says why a button is disabled. */
  title?: string;
}) {
  return (
    <div class="tooltip tooltip-left before:max-w-none before:whitespace-nowrap" data-tip={props.tooltip}>
      <button
        type="button"
        class="btn btn-ghost btn-xs btn-square"
        classList={{
          "text-error": props.danger && !props.disabled,
          "text-success": props.ok && !props.disabled,
        }}
        aria-label={props.label}
        title={props.title}
        disabled={props.disabled}
        onClick={() => props.onClick()}
      >
        <Dynamic component={props.icon} size={16} strokeWidth={1.75} aria-hidden="true" />
      </button>
    </div>
  );
}

/**
 * IconButton as a link: a small button of an icon that is an anchor, for what is a plain request of the browser (the download of a
 * file: no XHR, the session of the page carries it). href is where it goes and download the name that the file is saved under. onClick
 * is told the click, and may stop it (preventDefault) to open the link itself later, once admin mode is on.
 */
export function IconLink(props: {
  icon: Component<IconProps>;
  label: string;
  tooltip: string;
  href: string;
  download?: string;
  onClick?: (e: MouseEvent) => void;
}) {
  return (
    <div class="tooltip tooltip-left before:max-w-none before:whitespace-nowrap" data-tip={props.tooltip}>
      <a
        class="btn btn-ghost btn-xs btn-square"
        aria-label={props.label}
        href={props.href}
        download={props.download}
        onClick={(e) => props.onClick?.(e)}
      >
        <Dynamic component={props.icon} size={16} strokeWidth={1.75} aria-hidden="true" />
      </a>
    </div>
  );
}
