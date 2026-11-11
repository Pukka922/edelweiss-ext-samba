import { createI18n } from "@edelweiss/sdk";
import { FakeEventSource, fakeSession, recordingToasts, TestShell } from "@edelweiss/sdk/testing";
import { fireEvent, render } from "@solidjs/testing-library";
import { batch, type JSX } from "solid-js";
import { vi } from "vitest";
import de from "../../public/locales/de.json";
import en from "../../public/locales/en.json";
import type { JobState } from "../api";
import { job } from "./fixtures";

export const BASE = "/ext/samba/api";

export const json = (body: unknown, status = 200) =>
  new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } });
export const noContent = () => new Response(null, { status: 204 });
export const apiError = (
  status: number,
  code: string,
  message = code,
  details: Record<string, unknown> = {},
) => json({ error: { code, message, ...details } }, status);

export type Handler = (body: unknown) => Response | Promise<Response>;
export interface Call {
  method: string;
  path: string;
  body: unknown;
}

/** The route for a request: the one with exactly that text, else one whose text has a star (any characters) that matches it. */
function routeFor(routes: Record<string, Handler>, request: string): Handler | undefined {
  const exact = routes[request];
  if (exact) return exact;
  const pattern = (key: string) =>
    new RegExp(
      `^${key
        .split("*")
        .map((part) => part.replace(/[.+?^${}()|[\]\\]/g, "\\$&"))
        .join(".*")}$`,
    );
  return Object.entries(routes).find(([key]) => key.includes("*") && pattern(key).test(request))?.[1];
}

/** Stubs fetch. Routes are "METHOD /path" below the extension's API, query included; a * in a route stands for any characters. */
export function stubApi(routes: Record<string, Handler>) {
  const calls: Call[] = [];
  vi.stubGlobal(
    "fetch",
    vi.fn(async (url: string, init: RequestInit = {}) => {
      const method = init.method ?? "GET";
      const path = url.startsWith(BASE) ? url.slice(BASE.length) : url;
      const body = typeof init.body === "string" ? JSON.parse(init.body) : undefined;
      calls.push({ method, path, body });
      const handler = routeFor(routes, `${method} ${path}`);
      return handler ? handler(body) : new Response(null, { status: 404 });
    }),
  );
  return {
    calls,
    sent: (method: string, path: string) => calls.filter((c) => c.method === method && c.path === path),
  };
}

/**
 * The routes of the job J9 of kind on target with its steps: running at first, then what states gives, one per GET
 * (the last one stays). routes are more routes of the test (they win over these).
 */
export function jobRoutes(
  kind: string,
  target: string,
  steps: string[],
  states: Partial<JobState>[],
  routes: Record<string, Handler> = {},
) {
  const first = job({ id: "J9", kind, target, steps, step: 0 });
  let poll = 0;
  return stubApi({
    "POST /jobs": () => json(first, 202),
    "GET /jobs/J9": () => json({ ...first, ...states[Math.min(poll++, states.length - 1)] }),
    ...routes,
  });
}

/**
 * Replaces EventSource by the SDK's fake; FakeEventSource.instances lists the streams opened. A test fires the open
 * of a stream (the first connection and every reconnect of a real one) with instances[i].open(), its events with
 * emit(name, json) and a failure with fail(closed).
 */
export function stubEventSource() {
  FakeEventSource.instances = [];
  vi.stubGlobal("EventSource", FakeEventSource);
  return FakeEventSource;
}

/** Renders ui inside the test shell of the extension, with the English and German texts; toasts records the toasts. */
export function renderExt(
  ui: () => JSX.Element,
  session = fakeSession({ admin: true, canAdmin: true }),
  locale: "en" | "de" = "en",
) {
  const i18n = createI18n(locale);
  i18n.register("samba", "en", en);
  i18n.register("samba", "de", de);
  const toasts = recordingToasts();
  const view = render(() => (
    <TestShell extension="samba" i18n={i18n} session={session.api} toasts={toasts}>
      {ui()}
    </TestShell>
  ));
  return { ...view, session, toasts };
}

/**
 * A session without admin mode whose requestAdmin brings it the way the shell does (web/shell/src/state/session.ts,
 * enableAdmin): a moment later, the new session and the answer in one batch – so the answer comes before the effects
 * of the new session (a resource keyed on admin mode has only just started to load).
 */
export function adminOnRequest() {
  const session = fakeSession({ admin: false, canAdmin: true });
  session.api.requestAdmin = () =>
    new Promise<boolean>((resolve) => {
      setTimeout(() =>
        batch(() => {
          session.setInfo((info) => ({ ...info!, admin: true }));
          resolve(true);
        }),
      );
    });
  return session;
}

/** The translator of the extension for a locale, as the SDK builds it. */
export function translator(locale: "en" | "de") {
  const i18n = createI18n(locale);
  i18n.register("samba", "en", en);
  i18n.register("samba", "de", de);
  return (key: string, params?: Record<string, string | number>) => i18n.translate("samba", key, params);
}

/** Lets what waits on a timer of zero run (the requests that are under way), under fake timers. */
export const settle = () => vi.advanceTimersByTimeAsync(0);

/** Types value into a text field. */
export const type = (el: HTMLElement, value: string) => fireEvent.input(el, { target: { value } });
