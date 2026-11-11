import { ApiError, createI18n } from "@edelweiss/sdk";
import { describe, expect, it } from "vitest";
import { errorText, jobErrorText, staleRefusal, stepLabel, stepText, tSafe } from "./errors";
import { job } from "./test/fixtures";
import { translator as samba } from "./test/render";

/** The translator of these tests, with texts of their own: tSafe works on whatever the translator gives back. */
function translator(locale: "en" | "de") {
  const i18n = createI18n(locale);
  i18n.register("test", "en", {
    cancel: "Cancel",
    running: "{{ name }} is running.",
    placed: "{{ name }} is in {{ place }}.",
    needs: "{{ name }} needs {{ other }}: without {{ other }} it does not start.",
  });
  i18n.register("test", "de", { running: "{{ name }} läuft." });
  return (key: string, params?: Record<string, string | number>) => i18n.translate("test", key, params);
}

describe("tSafe", () => {
  const t = translator("en");

  // a name or a message of the server may hold a "$": the patterns of String.replace must stay what they are
  it.each(["pc01$", "$&", "$'", "$`", "$$", "$1", "$<n>", "a$&b$'c", "pc01$' not found"])(
    "puts %s in unchanged",
    (value) => {
      expect(tSafe(t, "running", { name: value })).toBe(`${value} is running.`);
      expect(tSafe(t, "placed", { name: value, place: value })).toBe(`${value} is in ${value}.`);
    },
  );

  it("fills every placeholder, each as often as the text has it", () => {
    expect(tSafe(t, "placed", { name: "docs", place: "pool1" })).toBe("docs is in pool1.");
    expect(tSafe(t, "needs", { name: "docs", other: "pool1" })).toBe(
      "docs needs pool1: without pool1 it does not start.",
    );
  });

  it("does not read a value as a placeholder, or as the mark of another one", () => {
    const text = tSafe(t, "placed", { name: "{{ place }}", place: "\u00000\u0000" });
    expect(text).toBe("{{ place }} is in \u00000\u0000.");
    expect(tSafe(t, "placed", { name: "\u00001\u0000", place: "x" })).toBe("\u00001\u0000 is in x.");
  });

  it("takes numbers and the empty string", () => {
    expect(tSafe(t, "placed", { name: 7, place: "" })).toBe("7 is in .");
  });

  it("is the text without parameters when there are none, and the key when there is no text", () => {
    expect(tSafe(t, "cancel")).toBe("Cancel");
    expect(tSafe(t, "no.such.key", { name: "x" })).toBe("no.such.key");
  });

  it("speaks German too", () => {
    expect(tSafe(translator("de"), "running", { name: "pc01$" })).toBe("pc01$ läuft.");
  });
});

describe("errorText", () => {
  const en = samba("en");
  const de = samba("de");

  it("translates the codes of the API", () => {
    const cases: [number, string, string, string][] = [
      [403, "admin_required", "This needs admin mode.", "Dafür ist der Admin-Modus nötig."],
      [404, "not_found", "That no longer exists – reload.", "Das gibt es nicht mehr – lade neu."],
      [
        409,
        "busy",
        "Another change is running – try again in a moment.",
        "Gerade läuft schon eine andere Änderung – versuch es gleich noch einmal.",
      ],
    ];
    for (const [status, code, english, german] of cases) {
      const err = new ApiError(status, code, "the message of the server");
      expect(errorText(err, en), code).toBe(english);
      expect(errorText(err, de), code).toBe(german);
    }
  });

  it("puts the message of the server into the codes that carry it, unchanged", () => {
    const message = "smbd: cannot read $' of $& in $$";
    const text = (code: string, t: typeof en) => errorText(new ApiError(500, code, message), t);
    expect(text("failed", en)).toBe(`That failed: ${message}`);
    expect(text("failed", de)).toBe(`Das ist fehlgeschlagen: ${message}`);
    expect(text("invalid_json", en)).toBe(`The request could not be read: ${message}`);
    expect(text("invalid_json", de)).toBe(`Die Anfrage war nicht lesbar: ${message}`);
    expect(text("invalid_input", en)).toBe(`The input is not valid: ${message}`);
    expect(text("invalid_input", de)).toBe(`Die Eingabe geht so nicht: ${message}`);
  });

  it("shows the message of the server for a code without a text, and the message of any other error", () => {
    expect(errorText(new ApiError(500, "weird", "odd thing"), en)).toBe("odd thing");
    expect(errorText(new Error("network down"), en)).toBe("network down");
    expect(errorText("plain", en)).toBe("plain");
  });

  it("takes the text of the code for an invalid input without a field, or with a field that has no text", () => {
    const odd = new ApiError(400, "invalid_input", "bad", { field: "nosuch" });
    expect(errorText(odd, en)).toBe("The input is not valid: bad");
    expect(errorText(new ApiError(400, "invalid_input", "bad"), de)).toBe("Die Eingabe geht so nicht: bad");
  });

  it("takes the text of the field of an invalid input, with the message of the server unchanged", () => {
    const field = new ApiError(400, "invalid_input", "$& is bad", { field: "name" });
    expect(errorText(field, en)).toBe("The name is not valid: $& is bad");
    expect(errorText(field, de)).toBe("Der Name geht so nicht: $& is bad");
  });
});

describe("the steps of a job", () => {
  const en = samba("en");

  it("words the label of a step, and keeps one without a text", () => {
    expect(stepLabel(en, "lbu")).toBe("save the changes (lbu commit)");
    expect(stepLabel(samba("de"), "lbu")).toBe("Änderungen sichern (lbu commit)");
    expect(stepLabel(en, "odd")).toBe("odd");
    expect(stepLabel(en, undefined)).toBe("");
  });

  it("names the step a job is at", () => {
    expect(stepText(job({ steps: ["prepare", "lbu"], step: 1 }), en)).toBe(
      "Step 2/2: save the changes (lbu commit)",
    );
    expect(stepText(job({ steps: ["lbu"], step: 0 }), samba("de"))).toBe(
      "Schritt 1/1: Änderungen sichern (lbu commit)",
    );
  });
});

describe("jobErrorText", () => {
  it("names the step and the message of a failure, unchanged", () => {
    const st = job({
      status: "failed",
      code: "failed",
      step: 0,
      error: "smbcontrol reload-config: exit 1 ($&)",
    });
    expect(jobErrorText(st, samba("en"))).toBe(
      "Step 1/2 (prepare the files) failed: smbcontrol reload-config: exit 1 ($&)",
    );
    expect(jobErrorText(st, samba("de"))).toBe(
      "Schritt 1/2 (Dateien vorbereiten) ist fehlgeschlagen: smbcontrol reload-config: exit 1 ($&)",
    );
  });

  it("takes the text of a failure for a code without one", () => {
    const st = job({ status: "failed", code: "odd", step: 1, error: "lbu: exit status 1" });
    expect(jobErrorText(st, samba("en"))).toBe(
      "Step 2/2 (save the changes (lbu commit)) failed: lbu: exit status 1",
    );
  });

  it("says it of a job whose runner is gone (status interrupted), whatever its code", () => {
    const st = job({ status: "interrupted", step: 1 });
    expect(jobErrorText(st, samba("en"))).toBe(
      "The job was interrupted at step 2/2 (save the changes (lbu commit)). Check the state before you go on.",
    );
  });

  it("says where an interrupted job stopped", () => {
    const st = job({ status: "interrupted", code: "interrupted", step: 0 });
    expect(jobErrorText(st, samba("de"))).toBe(
      "Der Vorgang wurde bei Schritt 1/2 (Dateien vorbereiten) unterbrochen. Prüfe den Zustand, bevor du weitermachst.",
    );
  });
});

describe("staleRefusal", () => {
  it("is a refusal that says the page read the host before it changed: what the request names is gone", () => {
    expect(staleRefusal(new ApiError(404, "not_found", "x"))).toBe(true);
  });

  it("is no other refusal: the others name something the user can change, or wait out", () => {
    for (const [status, code] of [
      [409, "busy"],
      [409, "in_use"],
      [409, "exists"],
      [409, "not_setup"],
      [400, "invalid_input"],
      [403, "admin_required"],
      [500, "failed"],
    ] as const)
      expect(staleRefusal(new ApiError(status, code, "x")), code).toBe(false);
  });

  it("is no error that is no answer of the server", () => {
    expect(staleRefusal(new TypeError("Failed to fetch"))).toBe(false);
    expect(staleRefusal("not_found")).toBe(false);
    expect(staleRefusal(undefined)).toBe(false);
  });
});
