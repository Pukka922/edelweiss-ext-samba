import { readdirSync, readFileSync } from "node:fs";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { ApiError } from "@edelweiss/sdk";
import { describe, expect, it } from "vitest";
import de from "../public/locales/de.json";
import en from "../public/locales/en.json";
import { errorText } from "./errors";
import { translator } from "./test/render";

/**
 * The page has a text for every code, every field and every reason that the Go code can answer with, and for every reason
 * of the view (why a folder cannot be served, why smb.conf leaves a share out). The lists are read from the Go sources
 * with regular expressions: a refusal that is added there and not worded here is caught. The sanity checks below break
 * when a pattern stops finding what it should.
 */
const ROOT = resolve(dirname(fileURLToPath(import.meta.url)), "../..");

/** What a pattern finds in the Go sources (no tests) of a package or a file of the repository, each once, sorted. */
function found(re: RegExp, ...where: string[]): string[] {
  const text = where
    .flatMap((path) => {
      const full = join(ROOT, path);
      return path.endsWith(".go")
        ? [readFileSync(full, "utf8")]
        : readdirSync(full)
            .filter((f) => f.endsWith(".go") && !f.endsWith("_test.go"))
            .map((f) => readFileSync(join(full, f), "utf8"));
    })
    .join("\n");
  return [...new Set([...text.matchAll(re)].map((m) => m[1] as string))].sort();
}

/**
 * The codes of the API: internal/actions Code…, internal/api code…, internal/accounts Code… (name_taken is answered as
 * exists), and those of the SDK and the core.
 */
const CODES = [
  ...new Set([
    ...found(/^\s*Code\w+\s*=\s*"(\w+)"/gm, "internal/actions/actions.go"),
    ...found(/^\s*code\w+\s*=\s*"(\w+)"/gm, "internal/api/api.go"),
    ...found(/^\s*Code\w+\s*=\s*"(\w+)"/gm, "internal/accounts/errors.go").filter((c) => c !== "name_taken"),
    "admin_required",
    "unauthenticated",
    "csrf_invalid",
  ]),
].sort();

/**
 * The fields of an invalid_input (and of an exists): invalid("x") and .Field = "x" of internal/actions; invalid("x"),
 * invalidFor("x") and apiError(code, message, "x") of internal/api; the fields of the refusals of internal/accounts.
 */
const FIELDS = [
  ...new Set([
    ...found(/\binvalid\("(\w+)"/g, "internal/actions"),
    ...found(/\.Field = "(\w+)"/g, "internal/actions"),
    ...found(/\binvalid(?:For)?\("(\w+)"/g, "internal/api"),
    ...found(/\bapiError\([\w.]+, [^\n]*, "(\w+)", nil\)/g, "internal/api"),
    ...found(/\brefuse\(Code(?:Invalid|Taken|System|NotFound), "(\w+)"/g, "internal/accounts"),
    ...found(/\bcheckServiceID\(\w+, "(\w+)"\)/g, "internal/accounts"),
  ]),
].sort();

/** The reasons of an invalid_input: the reason… constants of internal/actions/shares.go and internal/api/navigator.go. */
const INPUT_REASONS = found(
  /^\s*reason\w+\s*=\s*"(\w+)"/gm,
  "internal/actions/shares.go",
  "internal/api/navigator.go",
);

/** The reasons of no_acl: the Reason… constants of internal/acl/supported.go. */
const ACL_REASONS = found(/^\s*Reason\w+\s*=\s*"(\w+)"/gm, "internal/acl/supported.go");

/**
 * The codes that a job ends with: the code… and Code… constants of internal/jobs/step.go, and those that the Codes of a
 * step give an exit status of --samba-tool (the Code… constants of internal/recycle/restore.go).
 */
const JOB_CODES = found(/^\s*(?:const\s+)?[cC]ode\w+\s*=\s*"(\w+)"/gm, "internal/jobs/step.go");
const STEP_CODES = found(/^const Code\w+ = "(\w+)"/gm, "internal/recycle/restore.go");

/** Why a folder cannot be served (paths.Available): the constants of internal/paths/check.go. */
const SHARE_REASONS = found(
  /^\s*(?:Unmounted|Missing|Link|NoOwner|KernelTree)\s*=\s*"(\w+)"/gm,
  "internal/paths/check.go",
);

/** Why smb.conf leaves a share out: the Skip… constants of internal/samba/render.go. */
const SKIP_REASONS = found(/^\s*Skip\w+\s*=\s*"(\w+)"/gm, "internal/samba/render.go");

/** The text at a path of dots, undefined when there is none. */
function at(dict: object, path: string): string | undefined {
  let node: unknown = dict;
  for (const part of path.split(".")) {
    if (typeof node !== "object" || node === null) return undefined;
    node = (node as Record<string, unknown>)[part];
  }
  return typeof node === "string" ? node : undefined;
}

const LOCALES = [
  ["en", en],
  ["de", de],
] as const;

describe("what the Go code can answer with", () => {
  it("is found in the sources: the patterns still find what they are for", () => {
    expect(CODES).toEqual(
      expect.arrayContaining([
        "invalid_input",
        "invalid_json",
        "not_setup",
        "busy",
        "exists",
        "in_use",
        "unavailable",
        "no_owner",
        "root_owned",
        "confirm_needed",
        "folder_rights_unsafe",
        "wrong_password",
        "no_acl",
        "needs_convert",
        "system_account",
        "failed",
        "not_found",
        "not_cancellable",
        "too_large",
        "length_required",
      ]),
    );
    expect(FIELDS).toEqual(
      expect.arrayContaining([
        "name",
        "path",
        "recycleDays",
        "rights",
        "dir",
        "folder",
        "password",
        "uid",
        "gid",
      ]),
    );
    expect(INPUT_REASONS).toEqual(
      [
        "kernel",
        "link",
        "no_data_mount",
        "not_absolute",
        "not_dir",
        "per_user",
        "per_user_disk_changed",
        "in_share",
        "not_mounted",
      ].sort(),
    );
    expect(ACL_REASONS).toEqual(["no_posix_acl", "no_privilege", "no_xattr"]);
    expect(JOB_CODES).toEqual(["cancelled", "failed", "interrupted", "not_confirmed"]);
    expect(STEP_CODES).toEqual(["no_noreplace"]);
    expect(SHARE_REASONS).toEqual(["kernel", "link", "missing", "no_owner", "unmounted"]);
    expect(SKIP_REASONS).toEqual([
      "bad_name",
      "bad_path",
      "case_clash",
      "no_owner",
      "no_users",
      "unconfirmed",
    ]);
  });

  for (const [locale, dict] of LOCALES) {
    describe(locale, () => {
      it("has a text for every code of the API", () => {
        expect(
          CODES.filter((c) => !at(dict, `errors.${c}`)),
          "errors.<code>",
        ).toEqual([]);
      });

      it("has a text for every field of an invalid input", () => {
        expect(
          FIELDS.filter((f) => !at(dict, `errors.field.${f}`)),
          "errors.field.<field>",
        ).toEqual([]);
      });

      it("has a text for every reason of an invalid input, and of no_acl", () => {
        expect(
          INPUT_REASONS.filter((r) => !at(dict, `errors.reason.${r}`)),
          "errors.reason.<reason>",
        ).toEqual([]);
        expect(
          ACL_REASONS.filter((r) => !at(dict, `errors.aclReason.${r}`)),
          "errors.aclReason.<reason>",
        ).toEqual([]);
      });

      it("has a text for every code that a job ends with", () => {
        expect(
          [...JOB_CODES, ...STEP_CODES].filter((c) => !at(dict, `jobs.${c}`)),
          "jobs.<code>",
        ).toEqual([]);
      });

      it("has a text for every reason of the view", () => {
        expect(
          SHARE_REASONS.filter((r) => !at(dict, `shares.reason.${r}`)),
          "shares.reason.<reason>",
        ).toEqual([]);
        expect(
          SKIP_REASONS.filter((r) => !at(dict, `shares.skipped.${r}`)),
          "shares.skipped.<reason>",
        ).toEqual([]);
      });
    });
  }
});

describe("errorText for the codes and reasons of the API", () => {
  const en_ = translator("en");
  const de_ = translator("de");
  const RAW = "RAW message of the server";
  const err = (status: number, code: string, details: Record<string, unknown> = {}) =>
    new ApiError(status, code, RAW, details);

  it("words every code, with and without details, never with the message of the server alone", () => {
    for (const code of CODES) {
      for (const t of [en_, de_]) {
        const text = errorText(err(409, code), t);
        expect(text, code).not.toBe(RAW);
        expect(text, code).not.toMatch(/^errors\./);
        expect(text, code).not.toContain("{{");
      }
      expect(errorText(err(409, code), en_), code).not.toBe(errorText(err(409, code), de_));
    }
  });

  it("words every reason of an invalid input whole, whatever the field", () => {
    for (const reason of INPUT_REASONS) {
      for (const t of [en_, de_]) {
        const text = errorText(err(400, "invalid_input", { field: "path", reason }), t);
        expect(text, reason).not.toContain(RAW);
        expect(text, reason).not.toMatch(/^(The input is not valid|Die Eingabe geht so nicht)/);
      }
    }
  });

  it("words the kernel refusal", () => {
    const kernel = err(400, "invalid_input", { field: "path", reason: "kernel" });
    expect(errorText(kernel, de_)).toBe(
      "/proc, /sys und /dev sind Schnittstellen zum Kernel und können nicht freigegeben werden.",
    );
    expect(errorText(kernel, en_)).toBe(
      "/proc, /sys and /dev are interfaces to the kernel and cannot be shared.",
    );
  });

  it("words confirm_needed, folder_rights_unsafe and the folder that changed or cannot be read", () => {
    expect(errorText(err(409, "confirm_needed", { warnings: ["system"] }), de_)).toBe(
      "Bestätige zuerst die Warnungen zu diesem Ordner.",
    );
    expect(errorText(err(409, "folder_rights_unsafe", { warnings: ["root_owned"] }), de_)).toBe(
      "Für Systemordner, Ordner von root und Home-Verzeichnisse gibt es keine Ordner-Rechte: Das Umstellen würde jede Datei darin für die Gruppe schreibbar machen – bei einem Home sperrt das die Anmeldung per SSH-Schlüssel.",
    );
    const gone = err(400, "invalid_input", { field: "dir", reason: "no_data_mount" });
    expect(errorText(gone, de_)).toBe(
      "Die Platte des Ordners hat sich geändert oder ist nicht mehr da, oder der Ordner lässt sich nicht lesen.",
    );
    expect(errorText(gone, en_)).toBe(
      "The disk of the folder has changed or is gone, or the folder cannot be read.",
    );
    expect(errorText(err(409, "root_owned"), de_)).toBe(
      "Der Ordner gehört root, und root hat schon ein Konto: Dafür wird kein Dienstkonto angelegt.",
    );
  });

  it("words the folder at the mount point of a disk that is not mounted", () => {
    const gone = err(400, "invalid_input", { field: "path", reason: "not_mounted" });
    expect(errorText(gone, de_)).toBe(
      "Hier gehört laut /etc/fstab eine Platte hin, die gerade nicht eingehängt ist. Häng sie zuerst ein.",
    );
    expect(errorText(gone, en_)).toBe(
      "By /etc/fstab a disk belongs here that is not mounted right now. Mount it first.",
    );
  });

  it("words the reason of a share with folder rights that is on another disk now", () => {
    const moved = err(400, "invalid_input", { field: "path", reason: "per_user_disk_changed" });
    expect(errorText(moved, de_)).toBe(
      "Diese Freigabe nutzt Ordner-Rechte und liegt jetzt auf einem anderen Datenträger. Entferne sie und lege sie neu an.",
    );
    expect(errorText(moved, en_)).toBe(
      "This share uses folder rights and is on another disk now. Remove it and add it again.",
    );
  });

  it("gives every field a text of its own, not the one of an input without a field", () => {
    for (const field of FIELDS) {
      for (const t of [en_, de_]) {
        expect(errorText(err(400, "invalid_input", { field }), t), field).not.toBe(
          errorText(err(400, "invalid_input", { field: "nosuchfield" }), t),
        );
      }
    }
  });

  it("names what a refusal is about, unchanged", () => {
    expect(errorText(err(409, "unavailable", { share: "Backup$&" }), de_)).toBe(
      "Die Platte der Freigabe Backup$& ist nicht da.",
    );
    expect(errorText(err(409, "no_owner", { uid: 1001, gid: 1002 }), en_)).toBe(
      "The owner 1001:1002 of the folder has no account on this host – create a service account.",
    );
    expect(errorText(err(409, "needs_convert", { share: "Fotos", files: 1240 }), de_)).toBe(
      "Die Freigabe Fotos muss für Ordner-Rechte erst umgestellt werden (1240 Dateien und Ordner).",
    );
    expect(errorText(err(409, "no_acl", { reason: "no_xattr" }), de_)).toBe(
      "Die Platte dieser Freigabe kann keine Ordner-Rechte halten: Das Dateisystem hält keine erweiterten Attribute.",
    );
    expect(errorText(err(409, "in_use", { shares: ["Fotos", "a$'b"] }), de_)).toBe(
      "Das wird noch benutzt von: Fotos, a$'b.",
    );
  });

  it("takes the text of the code when the details name nothing it can use", () => {
    expect(errorText(err(409, "no_acl", { reason: "odd" }), de_)).toBe(
      "Die Platte dieser Freigabe kann keine Ordner-Rechte halten: odd",
    );
    expect(errorText(err(409, "no_owner", { uid: "x" }), en_)).toBe(
      "The owner of the folder has no account on this host – create a service account.",
    );
    expect(errorText(err(409, "unavailable"), en_)).toBe("The disk of this share is not there.");
  });

  it("words a wrong password and a system account", () => {
    expect(errorText(err(409, "wrong_password"), de_)).toBe("Das Linux-Passwort stimmt nicht.");
    expect(errorText(err(409, "system_account"), en_)).toBe(
      "That does not work with a system account or an account of the extension.",
    );
  });

  it("names the group of the system that a service account cannot join", () => {
    expect(errorText(err(409, "system_account", { field: "gid", group: "wheel$&" }), en_)).toBe(
      "The group wheel$& is a group of the system: a service account joins only the group users or a group with a GID of 1000 to 65532.",
    );
  });
});
