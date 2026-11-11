import { ApiError, type Translator } from "@edelweiss/sdk";
import { errorText, tSafe } from "../errors";

/** The fields of the share dialog that a refusal can be shown at. */
export type FormField = "name" | "path" | "recycleDays" | "rights";

const FORM_FIELDS: string[] = ["name", "path", "recycleDays", "rights"];

const isText = (s: unknown): s is string => typeof s === "string" && s !== "";

/**
 * The field of the share dialog that a refusal of share-add or share-edit is about, with its text: an invalid_input of a
 * field of the form (worded by its reason, else its field), a name that is taken, a folder that is in another share or
 * holds one, has an owner without an account, or is not there. Undefined for any other refusal, which
 * stands below the form.
 */
export function fieldOf(err: unknown, t: Translator["t"]): [FormField, string] | undefined {
  if (!(err instanceof ApiError)) return undefined;
  const d = err.details;
  switch (err.code) {
    case "invalid_input":
      return isText(d.field) && FORM_FIELDS.includes(d.field)
        ? [d.field as FormField, errorText(err, t)]
        : undefined;
    case "exists":
      return d.field === "name" ? ["name", t("share.nameTaken")] : undefined;
    case "in_use": {
      const shares = Array.isArray(d.shares) ? d.shares.filter(isText) : [];
      return [
        "path",
        shares.length > 0 ? tSafe(t, "share.pathInUse", { shares: shares.join(", ") }) : errorText(err, t),
      ];
    }
    case "no_owner":
      return ["path", errorText(err, t)];
    case "not_found":
      return isText(d.path) ? ["path", t("share.pathMissing")] : undefined;
  }
  return undefined;
}
