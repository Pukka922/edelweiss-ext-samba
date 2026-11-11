import { ApiError, type Params, type Translator } from "@edelweiss/sdk";
import type { JobState } from "./api";

const MARK = "\u0000";

/**
 * t(key, params) with values that stay what they are. The t() of the SDK puts a value in with String.replace(pattern,
 * string), which reads "$'", "$&", "$`" and "$$" in the value as replacement patterns: "mount: unknown device 'a$' in
 * fstab" would be garbled, with the text after the placeholder pasted in at the "$'". So t gets sentinels (strings
 * without a "$"), and the real values take their place afterwards, in one pass over the text (a value is never read
 * again as a placeholder or a sentinel). Use it for every text that carries a name or a message of the server: they
 * may hold a "$".
 */
export function tSafe(t: Translator["t"], key: string, params: Params = {}): string {
  const names = Object.keys(params);
  const sentinels = Object.fromEntries(names.map((name, i) => [name, `${MARK}${i}${MARK}`]));
  // split at the marks: the odd parts are the numbers of the sentinels, the even ones the text between them
  return t(key, sentinels)
    .split(MARK)
    .map((part, i) => {
      const name = i % 2 === 1 ? names[Number(part)] : undefined;
      return name === undefined ? part : String(params[name]);
    })
    .join("");
}

/**
 * Whether a refusal says that the page read the host before it changed: what the request names is not there any more
 * (not_found). A dialog that gets one is over: the same request would be refused again, so it offers Close and the page
 * reads the view again. Every other refusal names something the user can change or wait out (busy).
 */
export const staleRefusal = (err: unknown): boolean => err instanceof ApiError && err.code === "not_found";

/** The text of key with params, or undefined when there is none. */
function textOf(t: Translator["t"], key: string, params: Params = {}): string | undefined {
  const text = tSafe(t, key, params);
  return text === key ? undefined : text;
}

const isText = (s: unknown): s is string => typeof s === "string" && s !== "";

/** The texts of a list in the details of an error (the shares of an in_use). */
const textsOf = (v: unknown): string[] => (Array.isArray(v) ? v.filter(isText) : []);

const isID = (n: unknown): n is number => typeof n === "number" && Number.isInteger(n);

/**
 * The text of an invalid_input from its field and reason: the reason with a text of its own (errors.reason.<reason>: why a
 * folder was refused, not_absolute, no_data_mount, link, not_dir, per_user, per_user_disk_changed, system_dir), else the text of the
 * field (errors.field.<field>, with the message of the server). Undefined for a field without a text.
 */
function inputText(err: ApiError, t: Translator["t"]): string | undefined {
  const { field, reason } = err.details;
  if (!isText(field)) return undefined;
  const why = isText(reason) ? textOf(t, `errors.reason.${reason}`) : undefined;
  return why ?? textOf(t, `errors.field.${field}`, { message: err.message });
}

/**
 * The text of a refusal that names what it is about in its details (the shares that use a folder, the share whose disk is
 * not there, the IDs of an owner without an account, why a disk cannot keep folder rights, what a conversion changes, the
 * group of the system that a service account cannot join), undefined when it names nothing the text can use.
 */
function detailText(err: ApiError, t: Translator["t"]): string | undefined {
  const d = err.details;
  switch (err.code) {
    case "invalid_input":
      return inputText(err, t);
    case "in_use": {
      const shares = textsOf(d.shares);
      return shares.length > 0 ? textOf(t, "errors.inUseBy", { shares: shares.join(", ") }) : undefined;
    }
    case "unavailable":
      return isText(d.share) ? textOf(t, "errors.unavailableShare", { share: d.share }) : undefined;
    case "no_owner":
      return isID(d.uid) && isID(d.gid) ? textOf(t, "errors.noOwner", { uid: d.uid, gid: d.gid }) : undefined;
    case "no_acl":
      return isText(d.reason)
        ? textOf(t, "errors.noAcl", { reason: textOf(t, `errors.aclReason.${d.reason}`) ?? d.reason })
        : undefined;
    case "needs_convert":
      return isText(d.share) && isID(d.files)
        ? textOf(t, "errors.needsConvert", { share: d.share, files: d.files })
        : undefined;
    case "system_account":
      return isText(d.group) ? textOf(t, "errors.systemGroup", { group: d.group }) : undefined;
  }
  return undefined;
}

/**
 * A text for an error of the backend: for a refusal that names what it is about (see detailText, and the field and the
 * reason of an invalid input) the text of that; else the translation of its code (errors.<code>, with the message of the
 * server). A code without a text gives the message of the server, any other error its own message.
 */
export function errorText(err: unknown, t: Translator["t"]): string {
  if (!(err instanceof ApiError)) return err instanceof Error ? err.message : String(err);
  return detailText(err, t) ?? textOf(t, `errors.${err.code}`, { message: err.message }) ?? err.message;
}

/** The text of the label of a step (steps.<label>), the label itself when it has none. */
export function stepLabel(t: Translator["t"], label: string | undefined): string {
  return label === undefined ? "" : (textOf(t, `steps.${label}`) ?? label);
}

/** The step a job is at: "Step 1/2: save the changes (lbu commit)". */
export function stepText(st: JobState, t: Translator["t"]): string {
  return tSafe(t, "job.step", { n: st.step + 1, m: st.steps.length, label: stepLabel(t, st.steps[st.step]) });
}

/**
 * The text of a job that failed or was interrupted: jobs.<code> with the step and the message of the server (failed,
 * interrupted, cancelled); jobs.failed for a code without a text. The output of the failed command is not in it: the dialog shows it apart.
 */
export function jobErrorText(st: JobState, t: Translator["t"]): string {
  // a job whose runner is gone has the status interrupted (and may have no code); one that a SIGTERM ended has the code
  const code = st.status === "interrupted" ? "interrupted" : (st.code ?? "failed");
  const params = {
    n: st.step + 1,
    m: st.steps.length,
    label: stepLabel(t, st.steps[st.step]),
    error: st.error ?? st.code ?? "",
  };
  return textOf(t, `jobs.${code}`, params) ?? tSafe(t, "jobs.failed", params);
}
