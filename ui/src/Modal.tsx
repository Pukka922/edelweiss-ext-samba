import { createEffect, createSignal, createUniqueId, type JSX, onCleanup, onMount, Show } from "solid-js";
import type { JobProgressState } from "./api";
import type { Failure } from "./useJob";

/** An open modal: its close, whether it is busy, and its element. */
interface Open {
  close: () => void;
  busy: () => boolean;
  el?: HTMLElement;
}

/**
 * The open modals, the topmost (the one opened last) at the end: Escape goes to it alone, so a dialog above another one
 * closes without the one below, and a busy one closes nothing. A signal: what stands behind the modals follows it.
 */
const [stack, setStack] = createSignal<Open[]>([]);

const onEscape = (e: KeyboardEvent) => {
  if (e.key === "Escape") stack().at(-1)?.close();
};

/** Whether a modal is open: the page behind it is inert then (Behind). */
export const modalOpen = () => stack().length > 0;

/** Whether an open modal is busy (its change runs, the bridge dialog waits for its job): nothing may take its place. */
export const modalLocked = () => stack().some((m) => m.busy());

/** Keeps the inert attribute of el as on says (set as an attribute: what the browser and the tests both read). */
const inertWhile = (el: () => HTMLElement, on: () => boolean) =>
  createEffect(() => el().toggleAttribute("inert", on()));

/**
 * The content of the page that the modals stand in front of: inert while one is open, so that neither the keyboard nor
 * a click reaches a control behind it (a dialog would be replaced from behind its back). It is no box of its own.
 */
export function Behind(props: { children: JSX.Element }) {
  let el!: HTMLDivElement;
  inertWhile(() => el, modalOpen);
  return (
    <div ref={el} class="contents">
      {props.children}
    </div>
  );
}

/**
 * A modal dialog like the SDK's ConfirmDialog, with content of its own. Escape (when it is the topmost) and the
 * backdrop close it unless busy. A modal that another one is opened above (a confirmation over the content of a
 * dialog) is inert until that one closes.
 */
export function Modal(props: {
  title: string;
  busy?: boolean;
  wide?: boolean;
  onClose: () => void;
  children: JSX.Element;
  actions: JSX.Element;
}) {
  let el!: HTMLDivElement;
  const close = () => {
    if (!props.busy) props.onClose();
  };
  const me: Open = { close, busy: () => props.busy === true };
  onMount(() => {
    me.el = el;
    const open = [...stack(), me];
    setStack(open);
    if (open.length === 1) document.addEventListener("keydown", onEscape);
  });
  onCleanup(() => {
    const open = stack().filter((m) => m !== me);
    setStack(open);
    if (open.length === 0) document.removeEventListener("keydown", onEscape);
  });
  // another modal is above this one (and is not inside it)
  inertWhile(
    () => el,
    () => {
      const top = stack().at(-1);
      return top !== undefined && top !== me && !(top.el && el.contains(top.el));
    },
  );
  return (
    <div ref={el} class="modal modal-open" role="dialog" aria-modal="true" aria-label={props.title}>
      <div class={props.wide ? "modal-box max-w-3xl" : "modal-box"}>
        <h3 class="text-lg font-bold">{props.title}</h3>
        <div class="flex flex-col gap-3 py-4">{props.children}</div>
        <div class="modal-action">{props.actions}</div>
      </div>
      <div class="modal-backdrop" aria-hidden="true" onClick={close} />
    </div>
  );
}

/** A labelled control: children get the id that the label points to. */
export function Field(props: { label: string; children: (id: string) => JSX.Element }) {
  const id = createUniqueId();
  return (
    <div class="flex flex-col gap-1">
      <label class="text-sm" for={id}>
        {props.label}
      </label>
      {props.children(id)}
    </div>
  );
}

/** A checkbox with its text. */
export function Check(props: {
  label: JSX.Element;
  checked: boolean;
  disabled?: boolean;
  onChange: (on: boolean) => void;
}) {
  return (
    <label class="label cursor-pointer justify-start gap-2 whitespace-normal">
      <input
        type="checkbox"
        class="checkbox checkbox-sm"
        checked={props.checked}
        disabled={props.disabled}
        onChange={(e) => props.onChange(e.currentTarget.checked)}
      />
      <span>{props.label}</span>
    </label>
  );
}

/** A row of a list of facts (a dl with two columns): its label and, beside it, its value. */
export function Row(props: { label: string; children: JSX.Element }) {
  return (
    <>
      <dt class="opacity-70">{props.label}</dt>
      <dd>{props.children}</dd>
    </>
  );
}

const CALLOUTS = { ok: "bg-success/10", warn: "bg-warning/10", bad: "bg-error/10", info: "bg-base-200" };

/**
 * A box of text in a dialog that says what a change does (info), that it is fine (ok), that something is to be minded (warn) or that
 * something is lost (bad). It is no alert: it stands there from the start and is no answer of the server.
 */
export function Callout(props: { tone: keyof typeof CALLOUTS; children: JSX.Element }) {
  return <div class={`rounded-box p-3 text-sm ${CALLOUTS[props.tone]}`}>{props.children}</div>;
}

/** A radio button with its text; the buttons of one choice have the same name. */
export function Radio(props: {
  name: string;
  label: JSX.Element;
  checked: boolean;
  disabled?: boolean;
  onChange: () => void;
}) {
  return (
    <label class="label cursor-pointer justify-start gap-2 whitespace-normal">
      <input
        type="radio"
        class="radio radio-sm"
        name={props.name}
        checked={props.checked}
        disabled={props.disabled}
        onChange={() => props.onChange()}
      />
      <span>{props.label}</span>
    </label>
  );
}

/**
 * The job of a dialog: the step that runs, with a bar and the percent when the step says how far it is (progress: a long
 * command may report it; a step that does not has no bar), or the text of its failure with the output of the failed command
 * in a pre; a job whose state is not known (a neutral failure) only says so, as a status.
 */
export function JobStatus(props: { step?: string; failure?: Failure; progress?: JobProgressState }) {
  const percent = () => props.progress?.percent;
  return (
    <>
      <Show when={props.step}>
        {(step) => (
          <>
            <p class="flex items-center gap-2 text-sm">
              <span class="loading loading-spinner loading-sm" aria-hidden="true" />
              {step()}
            </p>
            <Show when={percent() !== undefined}>
              <p class="flex items-center gap-2 text-sm">
                <progress
                  class="progress progress-primary w-full"
                  value={percent()}
                  max="100"
                  aria-label={step()}
                />
                <span class="whitespace-nowrap">{Math.round(percent() as number)} %</span>
              </p>
            </Show>
          </>
        )}
      </Show>
      <Show when={props.failure?.neutral ? props.failure : undefined}>
        {(f) => (
          <div class="alert alert-info text-sm" role="status">
            {f().text}
          </div>
        )}
      </Show>
      <Show when={props.failure?.neutral ? undefined : props.failure}>
        {(f) => (
          <div class="alert alert-error flex-col items-start text-sm" role="alert">
            <span>{f().text}</span>
            <Show when={f().output}>
              {(output) => (
                <pre class="max-h-48 w-full overflow-auto whitespace-pre-wrap rounded-box bg-base-200 p-2 text-xs text-base-content">
                  {output()}
                </pre>
              )}
            </Show>
          </div>
        )}
      </Show>
    </>
  );
}
