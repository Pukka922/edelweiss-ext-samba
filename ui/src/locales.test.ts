import { expect, it } from "vitest";
import de from "../public/locales/de.json";
import en from "../public/locales/en.json";

const keys = (o: object, prefix = ""): string[] =>
  Object.entries(o).flatMap(([k, v]) =>
    typeof v === "object" && v !== null ? keys(v, `${prefix}${k}.`) : [`${prefix}${k}`],
  );

it("has the same keys in English and German", () => {
  expect(keys(de).sort()).toEqual(keys(en).sort());
});

/** The texts of a section of a locale, by key. */
const texts = (o: object): [string, string][] =>
  Object.entries(o).flatMap(([k, v]) =>
    typeof v === "object" && v !== null
      ? texts(v).map(([kk, vv]): [string, string] => [`${k}.${kk}`, vv])
      : [[k, String(v)]],
  );

it("promises no undo for a job: no plan of the extension undoes what it did", () => {
  const promises = /zurückgenommen|rückgängig|undone|rolled back/i;
  for (const locale of [en, de]) {
    const jobTexts = [...texts({ job: locale.job }), ...texts({ jobs: locale.jobs })];
    expect(jobTexts.length).toBeGreaterThan(5); // the texts are read
    expect(jobTexts.filter(([, text]) => promises.test(text))).toEqual([]);
  }
  expect(de.jobs.cancelled).toBe(
    "Der Vorgang wurde bei Schritt {{ n }}/{{ m }} ({{ label }}) abgebrochen. Schon erledigte Schritte bleiben bestehen; prüfe den Stand.",
  );
  expect(en.jobs.cancelled).toBe(
    "The job was cancelled at step {{ n }}/{{ m }} ({{ label }}). Steps already done stay done; check the result.",
  );
});
