import {
  AdminRequired,
  ErrorAlert,
  LoadingState,
  PageHeader,
  useApi,
  useI18n,
  useSession,
  useToast,
} from "@edelweiss/sdk";
import { type Accessor, createMemo, createResource, createSignal, For, onCleanup, Show } from "solid-js";
import type { SettingsView, ShareView, UserView, ViewResponse } from "./api";
import { ConnectionsTab } from "./ConnectionsTab";
import { errorText, staleRefusal, tSafe } from "./errors";
import type { Stale } from "./FolderTree";
import { formatList, serverName } from "./format";
import { type Guard, PageGuard, useGuard } from "./guard";
import { JobBanner } from "./JobBanner";
import { Behind, JobStatus, modalLocked } from "./Modal";
import { type Open, PageDialogs } from "./PageDialogs";
import { ServiceAlert } from "./ServiceAlert";
import { SettingsTab } from "./SettingsTab";
import { SetupDialog } from "./SetupDialog";
import type { ShareAction } from "./ShareRow";
import { SharesTab } from "./SharesTab";
import { UsersTab } from "./UsersTab";
import { PageJob, useJobRun } from "./useJob";

/** How often the page reads the view again while it is visible, in milliseconds. */
const RELOAD_MS = 5000;

type Tab = "shares" | "users" | "connections" | "settings";

const TABS: Tab[] = ["shares", "users", "connections", "settings"];

/**
 * The page of the extension: it reads the view (GET /view) every 5 seconds while the page is visible, and on Reload and
 * after every change, and keeps the last view that came when a read fails (below the error). It gives what is below it the
 * job of the view (PageJob, which a job falls back to when its own polls fail) and the guard (PageGuard, which asks for
 * admin mode first and reads the view again).
 */
export function SharesPage() {
  const api = useApi();
  const [data, { refetch }] = createResource(() => api.get<ViewResponse>("/view"));
  // the last view that came: a reload that fails leaves it on the page, below the error
  const view = createMemo<ViewResponse | undefined>((last) => (data.error ? last : (data.latest ?? last)));
  const timer = setInterval(() => {
    if (document.visibilityState === "visible" && !data.loading) void refetch();
  }, RELOAD_MS);
  onCleanup(() => clearInterval(timer));

  // After admin mode the guard reads the view again. refetch does not reject when the read fails (the resource keeps the
  // error), so the failure is thrown here: nothing opens then, and the error stays on the page.
  const guard = useGuard(async () => {
    await refetch();
    if (data.error) throw data.error;
  });
  return (
    <PageJob.Provider value={() => view()?.job}>
      <PageGuard.Provider value={guard}>
        <Page view={view} error={() => data.error} refetch={refetch} guard={guard} />
      </PageGuard.Provider>
    </PageJob.Provider>
  );
}

/**
 * The page itself: the head (whether Samba runs, its name in the network, the protocol; Reload and "Freigabe anlegen"),
 * the banner of the job of the view, and on a host that is not set up the setup in place of the tabs. Once it is set up:
 * the services that are stopped (ServiceAlert), the tabs (Freigaben, Nutzer, Verbindungen, Einstellungen) and the dialogs.
 *
 * Everything that changes something goes through the guard (admin mode first, then the view is read again) and then acts on
 * the share as that view has it, and only while no job runs in it. The setup and the start of the services run here, not
 * in their panels: the setup writes the config in the middle, which takes its dialog away, and services that run take the
 * alert away, while the job goes on and its end is still to be told. A user who can never get admin mode gets no button that
 * changes something; one without admin mode is told that the buttons ask for it. While a dialog is open the page behind it
 * is inert (Behind), and one that is busy is never replaced by another.
 *
 * The end of each job of the rights of a folder is handed to the folder trees (stale), which read their folders again, and so
 * is a disk that the server said cannot keep folder rights (noAcl, by share). Reload also reads the connections again.
 */
function Page(props: {
  view: Accessor<ViewResponse | undefined>;
  error: Accessor<unknown>;
  refetch: () => unknown;
  guard: Guard;
}) {
  const session = useSession();
  const toast = useToast();
  const { t, locale } = useI18n();
  const { view, guard } = props;
  const refetch = () => void props.refetch();
  // session.admin() reads the whole session object, which the shell replaces on every focus refresh
  const admin = createMemo(() => session.admin());
  const canAdmin = createMemo(() => session.canAdmin());

  const [tab, setTab] = createSignal<Tab>("shares");
  const [open, setOpenSignal] = createSignal<Open>();
  const [stale, setStale] = createSignal<Stale>();
  const [noAcl, setNoAcl] = createSignal<Record<string, string>>({});
  const [reloads, setReloads] = createSignal(0);
  /** Opens a dialog of the page, or closes it (undefined); a dialog that is busy is never replaced. */
  const setOpen = (next: Open | undefined) => {
    if (next !== undefined && modalLocked()) return;
    setOpenSignal(next);
  };
  const changed = (message: string) => {
    toast.success(message);
    refetch();
  };
  const done = (message: string) => {
    setOpen(undefined);
    changed(message);
  };
  const setup = useJobRun(changed);
  const start = useJobRun(changed);
  // the save of the settings runs here, not in its tab: its step and its end show whichever tab is open, and leaving the
  // tab does not stop it. A refusal that says the page read the host before it changed makes the page read it again.
  const saving = useJobRun(changed, (err) => {
    if (staleRefusal(err)) refetch();
    return false;
  });
  // the removal of the orphaned names runs here too, one user-drop after the other; the page tells their end once
  const dropping = useJobRun(() => {});
  /** A job runs in the view. */
  const jobRunning = createMemo(() => view()?.job?.status === "running");
  /** Nothing that changes something can be started: a job runs, or the setup, the start, a save or a removal does. */
  const busy = () => jobRunning() || setup.busy() || start.busy() || saving.busy() || dropping.busy();

  /**
   * The names that the shares give a right and that have no Samba entry (deleted in edelweiss-ext-users): only in admin mode,
   * with the Samba users known; their rights grant nothing.
   */
  const orphans = createMemo(
    () => {
      const v = view();
      if (!v || !admin() || !v.usersKnown) return [];
      const entries = new Set(v.users.filter((u) => !u.noEntry).map((u) => u.name));
      const named = v.config.shares.flatMap((s) =>
        Object.entries(s.rights).flatMap(([name, r]) => (r !== "" && !entries.has(name) ? [name] : [])),
      );
      return [...new Set(named)].sort((a, b) => a.localeCompare(b));
    },
    [] as string[],
    { equals: (a, b) => a.join("\n") === b.join("\n") },
  );
  /** "Verwaiste Namen entfernen": a user-drop for each orphaned name, one after the other, after the guard. */
  const dropOrphans = () =>
    void guard(async () => {
      const names = orphans();
      if (busy() || names.length === 0) return;
      for (const user of names) {
        const st = await dropping.run({ kind: "user-drop", user }, () => "");
        if (st?.status !== "done") return;
      }
      changed(tSafe(t, "done.orphans", { names: formatList(names, locale()) }));
    });

  /**
   * The setup asks for admin mode first. A Samba that is there before is named only in admin mode: when the view read
   * after admin mode reveals one, the setup waits for a second click, once the dialog has named it.
   */
  const startSetup = () => {
    const before = admin();
    void guard(() => {
      const v = view();
      if (!v || v.setup.setUp || busy() || (!before && v.setup.foreign)) return;
      void setup.run({ kind: "setup" }, () => t("done.setup"));
    });
  };
  const startServices = () =>
    void guard(() => {
      if (!busy()) void start.run({ kind: "start" }, () => t("done.start"));
    });
  const add = () =>
    void guard(() => {
      if (!busy() && view()?.setup.setUp) setOpen({ kind: "add" });
    });
  const action = (kind: ShareAction, s: ShareView) =>
    void guard(() => {
      const current = view()?.config.shares.find((c) => c.name === s.name);
      if (current && !busy()) setOpen({ kind, share: current });
    });
  const openFolder = (s: ShareView, folder: string) =>
    void guard(() => {
      const current = view()?.config.shares.find((c) => c.name === s.name);
      if (current && !busy()) setOpen({ kind: "folder", share: current, folder });
    });

  const addUser = (kind: "user-add" | "host-add") =>
    void guard(() => {
      const v = view();
      if (busy() || !v?.setup.setUp) return;
      // admin mode is on and the Samba users still cannot be read: say so, a button that does nothing says nothing
      if (!v.usersKnown) toast.error(t("users.unreadable"));
      else setOpen({ kind });
    });
  const saveSettings = (settings: SettingsView) =>
    void guard(() => {
      if (!busy()) void saving.run({ kind: "settings", settings }, () => t("done.settings"));
    });
  const userAction = (kind: "password" | "user-delete", u: UserView) =>
    void guard(() => {
      const current = view()?.users.find((c) => c.name === u.name);
      if (current && !busy()) setOpen({ kind, user: current });
    });

  const subtitle = () => {
    const v = view();
    if (!v) return undefined;
    if (!v.setup.setUp) return t("head.notSetUp");
    const s = v.config.settings;
    const name = serverName(s.name, session.info()?.hostname);
    return [
      t(v.services.samba ? "head.running" : "head.stopped"),
      ...(name === "" ? [] : [tSafe(t, "head.name", { name })]),
      t(s.smb2 ? "head.smb2" : "head.smb3"),
    ].join(" · ");
  };

  return (
    <div class="flex flex-col gap-4">
      <Behind>
        <PageHeader
          title={t("title")}
          subtitle={subtitle()}
          actions={
            <div class="flex items-center gap-2">
              <button
                type="button"
                class="btn btn-sm"
                onClick={() => {
                  refetch();
                  setReloads((n) => n + 1);
                }}
              >
                {t("actions.reload")}
              </button>
              <Show when={view()?.setup.setUp && canAdmin()}>
                <button type="button" class="btn btn-sm btn-primary" disabled={busy()} onClick={add}>
                  {t("actions.add")}
                </button>
              </Show>
            </div>
          }
        />
        <Show when={props.error()}>
          {(err) => <ErrorAlert error={errorText(err(), t)} onRetry={() => refetch()} />}
        </Show>
        <Show
          when={view()}
          fallback={
            <Show when={!props.error()}>
              <LoadingState label={t("loading")} />
            </Show>
          }
        >
          {(v) => (
            <>
              <Show when={v().job}>{(j) => <JobBanner job={j()} />}</Show>
              <Show
                when={v().setup.setUp}
                fallback={
                  <SetupDialog
                    foreign={v().setup.foreign}
                    readOnly={!canAdmin()}
                    admin={admin()}
                    busy={busy()}
                    step={setup.step()}
                    failure={setup.failure()}
                    onStart={startSetup}
                    onDismiss={setup.dismiss}
                  />
                }
              >
                {/* the setup set the config up in the middle of its job: its step and its end stand here */}
                <Show when={saving.step() !== undefined || saving.failure() !== undefined}>
                  <JobStatus step={saving.step()} failure={saving.failure()} />
                  <Show when={saving.failure()}>
                    <button
                      type="button"
                      class="btn btn-xs btn-ghost self-start"
                      onClick={() => saving.dismiss()}
                    >
                      {t("common.dismiss")}
                    </button>
                  </Show>
                </Show>
                <Show when={dropping.step() !== undefined || dropping.failure() !== undefined}>
                  <JobStatus step={dropping.step()} failure={dropping.failure()} />
                  <Show when={dropping.failure()}>
                    <button
                      type="button"
                      class="btn btn-xs btn-ghost self-start"
                      onClick={() => dropping.dismiss()}
                    >
                      {t("common.dismiss")}
                    </button>
                  </Show>
                </Show>
                <Show when={setup.step() !== undefined || setup.failure() !== undefined}>
                  <JobStatus step={setup.step()} failure={setup.failure()} />
                  <Show when={setup.failure()}>
                    <button
                      type="button"
                      class="btn btn-xs btn-ghost self-start"
                      onClick={() => setup.dismiss()}
                    >
                      {t("common.dismiss")}
                    </button>
                  </Show>
                </Show>
                <ServiceAlert
                  items={v().setup.items}
                  readOnly={!canAdmin()}
                  busy={busy()}
                  step={start.step()}
                  failure={start.failure()}
                  onStart={startServices}
                  onDismiss={start.dismiss}
                />
                <div role="tablist" class="tabs tabs-box w-fit" aria-label={t("tabs.label")}>
                  <For each={TABS}>
                    {(id) => (
                      <button
                        type="button"
                        role="tab"
                        class="tab"
                        classList={{ "tab-active": tab() === id }}
                        aria-selected={tab() === id}
                        onClick={() => setTab(id)}
                      >
                        {t(`tabs.${id}`)}
                      </button>
                    )}
                  </For>
                </div>
                <div role="tabpanel" aria-label={t(`tabs.${tab()}`)} class="flex flex-col gap-4">
                  <Show when={tab() === "shares"}>
                    <SharesTab
                      shares={v().config.shares}
                      mounts={v().mounts}
                      orphans={orphans()}
                      onDropOrphans={dropOrphans}
                      readOnly={!canAdmin()}
                      busy={busy()}
                      stale={stale()}
                      noAcl={noAcl()}
                      onAction={action}
                      onFolder={openFolder}
                    />
                  </Show>
                  <Show when={tab() === "users"}>
                    <UsersTab
                      users={v().users}
                      shares={v().config.shares}
                      usersKnown={v().usersKnown}
                      admin={admin()}
                      usersExt={v().usersExt}
                      readOnly={!canAdmin()}
                      busy={busy()}
                      onAddSamba={() => addUser("user-add")}
                      onAddHost={() => addUser("host-add")}
                      onPassword={(u) => userAction("password", u)}
                      onDelete={(u) => userAction("user-delete", u)}
                    />
                  </Show>
                  <Show when={tab() === "connections"}>
                    <ConnectionsTab admin={admin()} reload={reloads()} />
                  </Show>
                  <Show when={tab() === "settings"}>
                    <SettingsTab
                      settings={v().config.settings}
                      ifaces={v().ifaces}
                      readOnly={!canAdmin()}
                      busy={busy()}
                      onSave={saveSettings}
                    />
                  </Show>
                </div>
              </Show>
              <Show when={!admin()}>
                <AdminRequired message={canAdmin() ? t("alerts.adminHint") : t("alerts.adminOnly")} />
              </Show>
            </>
          )}
        </Show>
      </Behind>
      <Show when={open()} keyed>
        {(d) => (
          <PageDialogs
            open={d}
            view={() => view() as ViewResponse}
            busy={jobRunning}
            onClose={() => setOpen(undefined)}
            onDone={done}
            onChanged={changed}
            onReload={refetch}
            onFolderEnded={(share, folder) => setStale({ share, folder })}
            onNoAcl={(share, reason) => setNoAcl((m) => ({ ...m, [share]: reason }))}
          />
        )}
      </Show>
    </div>
  );
}
