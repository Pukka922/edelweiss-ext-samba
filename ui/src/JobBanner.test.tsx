import { cleanup } from "@solidjs/testing-library";
import { afterEach, describe, expect, it } from "vitest";
import { JobBanner } from "./JobBanner";
import { job } from "./test/fixtures";
import { renderExt } from "./test/render";

afterEach(cleanup);

describe("JobBanner", () => {
  it("shows a running job with its kind, its target and its step", () => {
    const r = renderExt(() => (
      <JobBanner job={job({ kind: "setup", target: "samba", steps: ["prepare", "lbu"], step: 0 })} />
    ));
    expect(r.getByText("Running: Set up Samba samba")).toBeTruthy();
    expect(r.getByText("Step 1/2: prepare the files")).toBeTruthy();
    expect(r.container.querySelector(".alert-info")).not.toBeNull();
    expect(r.container.querySelector(".loading")).not.toBeNull();
  });

  it("words the kind of the job", () => {
    const r = renderExt(() => <JobBanner job={job({ kind: "setup", target: "docs", steps: ["lbu"] })} />);
    expect(r.getByText("Running: Set up Samba docs")).toBeTruthy();
    expect(r.getByText("Step 1/1: save the changes (lbu commit)")).toBeTruthy();
  });

  it("shows an interrupted job as a warning with the step it stopped at", () => {
    const r = renderExt(() => (
      <JobBanner
        job={job({
          status: "interrupted",
          kind: "setup",
          target: "samba",
          steps: ["prepare", "lbu"],
          step: 1,
        })}
      />
    ));
    const alert = r.getByRole("alert");
    expect(alert.className).toContain("alert-warning");
    expect(alert.textContent).toBe(
      "Interrupted: Set up Samba samba at step 2/2 (save the changes (lbu commit)). Check the state before you go on.",
    );
  });

  it("shows a job that a SIGTERM ended (failed with the code interrupted) as an interrupted one", () => {
    const r = renderExt(() => (
      <JobBanner
        job={job({
          status: "failed",
          code: "interrupted",
          kind: "setup",
          target: "samba",
          steps: ["prepare", "lbu"],
          step: 1,
        })}
      />
    ));
    const alert = r.getByRole("alert");
    expect(alert.className).toContain("alert-warning");
    expect(alert.textContent).toBe(
      "Interrupted: Set up Samba samba at step 2/2 (save the changes (lbu commit)). Check the state before you go on.",
    );
  });

  it("speaks German, and keeps a $ in the target", () => {
    const r = renderExt(
      () => <JobBanner job={job({ kind: "setup", target: "a$&b", steps: ["lbu"], step: 0 })} />,
      undefined,
      "de",
    );
    expect(r.getByText("Läuft: Samba einrichten a$&b")).toBeTruthy();
    expect(r.getByText("Schritt 1/1: Änderungen sichern (lbu commit)")).toBeTruthy();
  });

  it("shows nothing for a job that is done or failed: it is no interruption", () => {
    for (const over of [
      { status: "done" as const },
      { status: "failed" as const, code: "failed", error: "smbd: exit status 1" },
    ]) {
      const r = renderExt(() => <JobBanner job={job(over)} />);
      expect(r.container.textContent, over.status).toBe("");
      expect(r.queryByRole("alert"), over.status).toBeNull();
      expect(r.container.querySelector(".alert"), over.status).toBeNull();
      cleanup();
    }
  });
});
