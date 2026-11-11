import type { fakeSession } from "@edelweiss/sdk/testing";
import { within } from "@solidjs/testing-library";
import type { JobState, ViewResponse } from "../api";
import { SharesPage } from "../SharesPage";
import { job, view } from "./fixtures";
import { type Handler, json, renderExt, stubApi } from "./render";

export type R = ReturnType<typeof renderExt>;

/** The name of the share in a row of the table. */
const nameOf = (tr: HTMLElement) => tr.querySelector("td .font-semibold")?.textContent;

/** The names of the shares in the table, in order. */
export const names = (r: R) => r.queryAllByRole("row").flatMap((tr) => nameOf(tr) ?? []);

/** The row of a share. */
export const row = (r: R, name: string) => r.getAllByRole("row").find((tr) => nameOf(tr) === name)!;

/** The labels of the buttons in el. */
export const labels = (el: HTMLElement) =>
  within(el)
    .queryAllByRole("button")
    .flatMap((b) => b.getAttribute("aria-label") ?? []);

/** The alert whose text holds text. */
export const alertWith = (r: R, text: string) =>
  r.queryAllByRole("alert").find((a) => a.textContent?.includes(text));

/** The page with the view of the preview (or v: a view, or what gives the view of each read), in German unless said. */
export function renderPage(
  opts: {
    session?: ReturnType<typeof fakeSession>;
    locale?: "en" | "de";
    v?: ViewResponse | (() => ViewResponse);
    routes?: Record<string, Handler>;
  } = {},
) {
  const v = opts.v;
  const api = stubApi({
    "GET /view": () => json(typeof v === "function" ? v() : (v ?? view())),
    ...opts.routes,
  });
  const r = renderExt(() => <SharesPage />, opts.session, opts.locale ?? "de");
  return { ...r, api };
}

/** The routes of the job J9 of kind on target with its steps: running at first, then what states gives (the last stays). */
export function jobHandlers(kind: string, target: string, steps: string[], states: Partial<JobState>[]) {
  const first = job({ id: "J9", kind, target, steps, step: 0 });
  let poll = 0;
  return {
    "POST /jobs": () => json(first, 202),
    "GET /jobs/J9": () => json({ ...first, ...states[Math.min(poll++, states.length - 1)] }),
  };
}
