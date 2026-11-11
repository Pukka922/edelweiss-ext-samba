// @vitest-environment node
import { readdirSync, readFileSync } from "node:fs";
import { join, relative } from "node:path";
import { fileURLToPath } from "node:url";
import { describe, expect, it } from "vitest";

/**
 * A guard for tSafe (errors.ts): the t() of the SDK puts a value in with String.replace(pattern, string), which reads
 * "$'", "$&", "$`" and "$$" in the value as replacement patterns, and values with names or server messages may hold
 * a "$". Every text with a value goes through tSafe, so the sources outside errors.ts must not call t(key, params)
 * themselves. A test with a bare "a$" does not find such a call (a "$" at the end of a value is left alone), so this
 * one reads the sources.
 */

const SRC = fileURLToPath(new URL(".", import.meta.url));

/** The sources of ui/src that are no tests: *.ts and *.tsx, not test/ and not *.test.*; errors.ts is where t() is called. */
function sources(dir: string): string[] {
  return readdirSync(dir, { withFileTypes: true }).flatMap((e) => {
    const path = join(dir, e.name);
    if (e.isDirectory()) return e.name === "test" ? [] : sources(path);
    return /\.tsx?$/.test(e.name) && !/\.test\./.test(e.name) && e.name !== "errors.ts" ? [path] : [];
  });
}

/** The end of the string or template literal that starts at i (the end of the source when it is not closed). */
function endOfString(source: string, i: number): number {
  const quote = source.charAt(i);
  for (let j = i + 1; j < source.length; j++) {
    if (source.charAt(j) === "\\") j++;
    else if (source.charAt(j) === quote) return j;
  }
  return source.length;
}

/** Whether the call whose arguments start at i has a second argument: a comma at the level of the call itself. */
function hasSecondArgument(source: string, start: number): boolean {
  let depth = 1;
  for (let i = start; i < source.length; i++) {
    const ch = source.charAt(i);
    if (ch === '"' || ch === "'" || ch === "`") i = endOfString(source, i);
    else if ("([{".includes(ch)) depth++;
    else if (")]}".includes(ch)) {
      if (--depth === 0) return false;
    } else if (ch === "," && depth === 1) return true;
  }
  return false;
}

/** The lines of the calls t(first, second ...) in source. A comment that shows such a call counts as well. */
function rawTranslations(source: string): number[] {
  const lines: number[] = [];
  for (const m of source.matchAll(/(?<![\w$])t\(/g)) {
    if (hasSecondArgument(source, m.index + m[0].length))
      lines.push(source.slice(0, m.index).split("\n").length);
  }
  return lines;
}

describe("the guard for t() with parameters", () => {
  it("finds a call with a second argument, wherever it is", () => {
    for (const source of [
      'const a = t("done.locked", { name });',
      't("a",\n  { name })',
      `return t(\`a.\${b}\`, params);`,
      "t(key(1, 2), p)",
      "x(() => t(k, { a: [1, 2] }))",
      'i18n.t("a", {})',
    ])
      expect(rawTranslations(source), source).toEqual([1]);
    expect(rawTranslations('a();\n\nb(t("x", { n }));')).toEqual([3]);
  });

  it("leaves everything else alone", () => {
    for (const source of [
      't("form.sameName")',
      't("a, b")',
      `t(\`a, \${b}\`)`,
      "t(a(1, 2))",
      't("it\'s, ok")',
      'tSafe(t, "done.locked", { name })',
      'const { t } = useI18n(); split(",")',
      "act(a, b); (t, u); t.x(1, 2); format(a, b)",
    ])
      expect(rawTranslations(source), source).toEqual([]);
  });

  it("holds for the sources of the extension: values go through tSafe", () => {
    const files = sources(SRC);
    expect(files.length).toBeGreaterThan(5); // the guard reads something
    const offenders = files.flatMap((file) =>
      rawTranslations(readFileSync(file, "utf8")).map((line) => `${relative(SRC, file)}:${line}`),
    );
    expect(offenders, "use tSafe(t, key, params) from errors.ts instead of t(key, params)").toEqual([]);
  });
});
