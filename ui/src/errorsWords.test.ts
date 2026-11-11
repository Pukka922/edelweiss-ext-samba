import { ApiError } from "@edelweiss/sdk";
import { describe, expect, it } from "vitest";
import { errorText, jobErrorText } from "./errors";
import { job } from "./test/fixtures";
import { translator } from "./test/render";

const en = translator("en");
const de = translator("de");
const both = [en, de] as const;

const err = (status: number, code: string, details: Record<string, unknown> = {}) =>
  new ApiError(status, code, "RAW message of the server", details);

describe("not_cancellable", () => {
  it("says that the job cannot be cancelled now", () => {
    expect(errorText(err(409, "not_cancellable"), de)).toBe(
      "Das lässt sich gerade nicht abbrechen – der Vorgang ist in einem Schritt, den man nicht abbrechen kann, oder schon zu Ende.",
    );
    expect(errorText(err(409, "not_cancellable"), en)).toBe(
      "That cannot be cancelled right now – the job is in a step that cannot be cancelled, or it is over.",
    );
  });
});

describe("in_use", () => {
  it("names the shares that use it, unchanged", () => {
    expect(errorText(err(409, "in_use", { shares: ["web", "db$&"] }), de)).toBe(
      "Das wird noch benutzt von: web, db$&.",
    );
    expect(errorText(err(409, "in_use", { shares: ["web", "db"] }), en)).toBe(
      "That is still in use by: web, db.",
    );
  });

  it("takes the text of the code when it names no share", () => {
    for (const details of [{}, { shares: [] }, { shares: "web" }]) {
      expect(errorText(err(409, "in_use", details), en), JSON.stringify(details)).toBe(
        "That is still in use.",
      );
      expect(errorText(err(409, "in_use", details), de), JSON.stringify(details)).toBe(
        "Das wird noch benutzt.",
      );
    }
  });
});

describe("jobErrorText for the codes that a long job ends with", () => {
  const ended = (over: Partial<ReturnType<typeof job>>) =>
    job({ status: "failed", steps: ["prepare", "lbu"], step: 1, ...over });

  it("words a job that was cancelled, with the step it stopped at", () => {
    const st = ended({ code: "cancelled", error: "cancelled: save the changes" });
    expect(jobErrorText(st, de)).toBe(
      "Der Vorgang wurde bei Schritt 2/2 (Änderungen sichern (lbu commit)) abgebrochen. Schon erledigte Schritte bleiben bestehen; prüfe den Stand.",
    );
    expect(jobErrorText(st, en)).toBe(
      "The job was cancelled at step 2/2 (save the changes (lbu commit)). Steps already done stay done; check the result.",
    );
  });

  it("does not show the message of the server for a job that was cancelled: the user asked for it", () => {
    const st = ended({ code: "cancelled", error: "RAW message of the server" });
    for (const t of both) expect(jobErrorText(st, t)).not.toContain("RAW message");
  });
});
