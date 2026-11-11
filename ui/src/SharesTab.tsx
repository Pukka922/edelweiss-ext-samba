import { useI18n } from "@edelweiss/sdk";
import { createSignal, For, Show, useContext } from "solid-js";
import type { MountView, ShareView } from "./api";
import { tSafe } from "./errors";
import { FolderTree, type Stale } from "./FolderTree";
import { formatList } from "./format";
import { PageGuard } from "./guard";
import { keyed } from "./keys";
import { type ShareAction, ShareRow } from "./ShareRow";

/**
 * The shares tab: a warning for each share whose disk is not mounted (smbd refuses it until the disk is back), the table
 * of the shares with a row for each by its name (so that a new view every few seconds keeps the rows, their buttons and
 * the focus), and what the marks of the rights mean. Without shares, a hint takes the place of the table. busy: a job
 * runs, and nothing that changes something can be started; a readOnly page has no buttons.
 *
 * A share whose folder is there opens its folder tree (FolderTree) in a row right below its own, after the guard of the page
 * (GET /folders needs admin mode); a click on a folder in it asks the page for the rights of that folder (onFolder). stale is
 * the end of the last folder-rights job, and noAcl says, by share, why the server refused folder rights on its disk.
 *
 * The names of the shares that have no Samba entry any more (orphans: deleted in edelweiss-ext-users; the page knows them
 * in admin mode only) are marked "gelöscht" in the rows, and a warning names them, with "Verwaiste Namen entfernen"
 * (onDropOrphans), which the page alone starts: nothing is removed without that click.
 */
export function SharesTab(props: {
  shares: ShareView[];
  mounts: MountView[];
  orphans: string[];
  readOnly: boolean;
  busy: boolean;
  stale: Stale | undefined;
  noAcl: Record<string, string>;
  onAction: (kind: ShareAction, share: ShareView) => void;
  onFolder: (share: ShareView, folder: string) => void;
  onDropOrphans: () => void;
}) {
  const { t, locale } = useI18n();
  const guard = useContext(PageGuard);
  const rows = keyed(
    () => props.shares,
    (s) => s.name,
  );
  const unmounted = () =>
    props.shares.filter((s) => !s.available && s.reason === "unmounted").map((s) => s.name);
  return (
    <div class="flex flex-col gap-3">
      <Show when={props.orphans.length > 0}>
        <div class="alert alert-warning flex flex-wrap" role="alert">
          <span>{tSafe(t, "shares.orphans", { names: formatList(props.orphans, locale()) })}</span>
          <Show when={!props.readOnly}>
            <button
              type="button"
              class="btn btn-sm"
              disabled={props.busy}
              onClick={() => props.onDropOrphans()}
            >
              {t("shares.dropOrphans")}
            </button>
          </Show>
        </div>
      </Show>
      <For each={unmounted()}>
        {(name) => (
          <div class="alert alert-warning" role="alert">
            {tSafe(t, "shares.unmounted", { name })}
          </div>
        )}
      </For>
      <section class="card card-border bg-base-100">
        <div class="card-body gap-3 p-4">
          <Show
            when={rows.keys().length > 0}
            fallback={
              <p class="text-sm opacity-70">{t(props.readOnly ? "shares.emptyReadOnly" : "shares.empty")}</p>
            }
          >
            <div class="overflow-x-auto">
              <table class="table table-sm">
                <thead>
                  <tr>
                    <th>{t("shares.columns.share")}</th>
                    <th>{t("shares.columns.path")}</th>
                    <th>{t("shares.columns.rights")}</th>
                    <th>{t("shares.columns.recycle")}</th>
                    <th>{t("shares.columns.status")}</th>
                    <th>
                      <span class="sr-only">{t("actions.label")}</span>
                    </th>
                  </tr>
                </thead>
                <tbody>
                  <For each={rows.keys()}>
                    {(name) => {
                      const share = rows.track(name);
                      const [open, setOpen] = createSignal(false);
                      const toggle = () => {
                        if (open()) setOpen(false);
                        else void guard(() => setOpen(true));
                      };
                      return (
                        <>
                          <ShareRow
                            share={share()}
                            deleted={props.orphans}
                            readOnly={props.readOnly}
                            busy={props.busy}
                            expanded={open()}
                            onToggle={toggle}
                            onAction={props.onAction}
                          />
                          <Show when={open() && share().available && !props.readOnly}>
                            <tr>
                              <td colSpan={6} class="bg-base-200/50">
                                <FolderTree
                                  share={share()}
                                  mount={props.mounts.find((m) => m.point === share().mount)}
                                  aclReason={props.noAcl[name]}
                                  stale={props.stale}
                                  busy={props.busy}
                                  onFolder={(folder) => props.onFolder(share(), folder)}
                                />
                              </td>
                            </tr>
                          </Show>
                        </>
                      );
                    }}
                  </For>
                </tbody>
              </table>
            </div>
            <p class="text-xs opacity-70">{t("shares.legend")}</p>
          </Show>
        </div>
      </section>
    </div>
  );
}
