import { useApi, useI18n } from "@edelweiss/sdk";
import { createEffect, createSignal, For, on, onCleanup, Show } from "solid-js";
import type { ConnectionView } from "./api";
import { errorText, tSafe } from "./errors";
import { keyed } from "./keys";

/** How often the tab reads the connections again while it is open and the page is visible, in milliseconds. */
const RELOAD_MS = 5000;

/** The name of an SMB dialect as smbstatus gives it: "SMB 3.1.1" for SMB3_11, "SMB 3.0" for SMB3_00; another one as it is. */
const protocolName = (dialect: string) => {
  const m = /^SMB(\d)_(\d)(\d)$/.exec(dialect);
  return m ? `SMB ${m[1]}.${m[2]}${m[3] === "0" ? "" : `.${m[3]}`}` : dialect;
};

/** The device of a connection: its name and its address, the address alone where the name is the address or there is none. */
const deviceOf = (c: ConnectionView) =>
  c.machine === "" || c.machine === c.address ? c.address : `${c.machine} · ${c.address}`;

/** What tells a connection from the others in a new list: who, from where, to which share, since when. */
const keyOf = (c: ConnectionView) => JSON.stringify([c.user, c.address, c.share, c.since]);

/**
 * The connections tab: who is connected (GET /connections, which only admin mode reads: without it a hint takes the place of
 * the table), from which device, with which protocol, to which share, since when, and the files that are open. It reads them
 * when it opens, on Reload of the page (reload counts the clicks), and every 5 seconds while it is open and the page is visible:
 * a tab that is closed is gone and reads nothing. A read that fails says why and keeps the list before it. The rows are by the
 * connection, so a new list with the same connections keeps them.
 */
export function ConnectionsTab(props: { admin: boolean; reload: number }) {
  const api = useApi();
  const { t, locale } = useI18n();
  const [list, setList] = createSignal<ConnectionView[]>();
  const [error, setError] = createSignal<unknown>();
  let reading = false;
  let alive = true;
  const read = async () => {
    if (reading || !props.admin) return;
    reading = true;
    try {
      const res = await api.get<ConnectionView[]>("/connections");
      if (alive) {
        setList(res);
        setError(undefined);
      }
    } catch (err) {
      if (alive) setError(err);
    } finally {
      reading = false;
    }
  };
  createEffect(
    on(
      () => props.admin,
      (admin) => {
        if (admin) void read();
      },
    ),
  );
  createEffect(
    on(
      () => props.reload,
      () => void read(),
      { defer: true },
    ),
  );
  const timer = setInterval(() => {
    if (document.visibilityState === "visible") void read();
  }, RELOAD_MS);
  onCleanup(() => {
    alive = false;
    clearInterval(timer);
  });
  const rows = keyed(() => list() ?? [], keyOf);

  /** Since when: the time on the same day, "gestern" and the time the day before, else the date with the time. */
  const since = (at: string) => {
    const d = new Date(at);
    const now = new Date();
    const time = new Intl.DateTimeFormat(locale(), { timeStyle: "short" }).format(d);
    if (d.toDateString() === now.toDateString()) return time;
    const yesterday = new Date(now);
    yesterday.setDate(now.getDate() - 1);
    if (d.toDateString() === yesterday.toDateString()) return tSafe(t, "connections.yesterday", { time });
    return new Intl.DateTimeFormat(locale(), { dateStyle: "short", timeStyle: "short" }).format(d);
  };

  return (
    <section class="card card-border bg-base-100">
      <div class="card-body gap-3 p-4">
        <Show when={props.admin} fallback={<p class="text-sm opacity-70">{t("connections.adminOnly")}</p>}>
          <Show when={error()}>
            {(err) => (
              <div class="alert alert-error text-sm" role="alert">
                {errorText(err(), t)}
              </div>
            )}
          </Show>
          <Show when={list() === undefined && error() === undefined}>
            <p class="text-sm opacity-70" role="status">
              {t("loading")}
            </p>
          </Show>
          <Show when={list()?.length === 0}>
            <p class="text-sm opacity-70">{t("connections.none")}</p>
          </Show>
          <Show when={rows.keys().length > 0}>
            <div class="overflow-x-auto">
              <table class="table table-sm">
                <thead>
                  <tr>
                    <th>{t("connections.columns.user")}</th>
                    <th>{t("connections.columns.machine")}</th>
                    <th>{t("connections.columns.protocol")}</th>
                    <th>{t("connections.columns.share")}</th>
                    <th>{t("connections.columns.since")}</th>
                    <th>{t("connections.columns.files")}</th>
                  </tr>
                </thead>
                <tbody>
                  <For each={rows.keys()}>
                    {(key) => {
                      const c = rows.track(key);
                      return (
                        <tr>
                          <td>{c().user}</td>
                          <td>{deviceOf(c())}</td>
                          <td class="whitespace-nowrap">{protocolName(c().protocol)}</td>
                          <td>{c().share}</td>
                          <td class="whitespace-nowrap">{since(c().since)}</td>
                          <td>
                            <For each={c().files} fallback="–">
                              {(file) => <div class="break-all">{file}</div>}
                            </For>
                          </td>
                        </tr>
                      );
                    }}
                  </For>
                </tbody>
              </table>
            </div>
          </Show>
          <p class="text-xs opacity-70">{t("connections.note")}</p>
        </Show>
      </div>
    </section>
  );
}
