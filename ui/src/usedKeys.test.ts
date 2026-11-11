// @vitest-environment node
import { readdirSync, readFileSync } from "node:fs";
import { join, relative } from "node:path";
import { fileURLToPath } from "node:url";
import { describe, expect, it } from "vitest";
import de from "../public/locales/de.json";
import en from "../public/locales/en.json";

/**
 * A guard against a text that is asked for and not there: every key that the sources name (a string that starts with a
 * part of the locale files and goes on with dots, "actions.reload") has a text in both languages, and a key that is built
 * ("shares.reason.${reason}") has at least the part before the built piece. A key that is misspelled would show as the
 * key itself in the page.
 */

const SRC = fileURLToPath(new URL(".", import.meta.url));

/** The sources of ui/src that are no tests: *.ts and *.tsx, not test/ and not *.test.*. */
function sources(dir: string): string[] {
  return readdirSync(dir, { withFileTypes: true }).flatMap((e) => {
    const path = join(dir, e.name);
    if (e.isDirectory()) return e.name === "test" ? [] : sources(path);
    return /\.tsx?$/.test(e.name) && !/\.test\./.test(e.name) ? [path] : [];
  });
}

/** The paths of all texts of a locale: "errors.field.name". */
function paths(dict: object, prefix = ""): string[] {
  return Object.entries(dict).flatMap(([k, v]) =>
    typeof v === "object" && v !== null ? paths(v, `${prefix}${k}.`) : [`${prefix}${k}`],
  );
}

const roots = new Set(Object.keys(en));
const enPaths = new Set(paths(en));
const dePaths = new Set(paths(de));

/** The keys that a source names: literals in quotes and templates that start with a root of the locale files. */
function keysIn(source: string): { key: string; built: boolean }[] {
  const found: { key: string; built: boolean }[] = [];
  for (const m of source.matchAll(/(["'`])([A-Za-z][A-Za-z0-9_-]*(?:\.[A-Za-z0-9_${}()\-.]+)+)\1/g)) {
    const text = m[2] as string;
    const root = text.split(".")[0] as string;
    if (!roots.has(root) || root === "title") continue;
    found.push({ key: text, built: text.includes("${") });
  }
  return found;
}

describe("the texts that the sources ask for", () => {
  const files = sources(SRC);

  it("finds the sources and some keys", () => {
    expect(files.length).toBeGreaterThan(15);
    const all = files.flatMap((f) => keysIn(readFileSync(f, "utf8")));
    expect(all.length).toBeGreaterThan(60);
  });

  it("has every key that a source names, in English and in German", () => {
    const missing = files.flatMap((file) =>
      keysIn(readFileSync(file, "utf8"))
        .filter((k) => !k.built)
        .flatMap(({ key }) => {
          const where = [enPaths.has(key) ? "" : "en", dePaths.has(key) ? "" : "de"].filter(Boolean);
          return where.length > 0 ? [`${relative(SRC, file)}: ${key} (${where.join(", ")})`] : [];
        }),
    );
    expect(missing).toEqual([]);
  });

  it("has the part before the built piece of every key that a source builds", () => {
    const missing = files.flatMap((file) =>
      keysIn(readFileSync(file, "utf8"))
        .filter((k) => k.built)
        .flatMap(({ key }) => {
          const prefix = key.slice(0, key.indexOf("${"));
          const ok =
            [...enPaths].some((p) => p.startsWith(prefix)) && [...dePaths].some((p) => p.startsWith(prefix));
          return ok ? [] : [`${relative(SRC, file)}: ${key}`];
        }),
    );
    expect(missing).toEqual([]);
  });

  it("catches a key that is not there", () => {
    expect(keysIn('t("actions.nosuch")').map((k) => k.key)).toEqual(["actions.nosuch"]);
    expect(enPaths.has("actions.nosuch")).toBe(false);
    expect(keysIn(`\`steps.\${label}\``)).toEqual([{ key: `steps.\${label}`, built: true }]);
  });
});
