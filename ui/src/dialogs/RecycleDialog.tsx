import { ApiError, useApi, useI18n } from "@edelweiss/sdk";
import { type Accessor, createEffect, createSignal, For, Show } from "solid-js";
import type { RecycleEntry, RecycleResponse, ShareView, ViewResponse } from "../api";
import { errorText, tSafe } from "../errors";
import { formatAgo, formatBytes } from "../format";
import { keyed } from "../keys";
import { JobStatus, Modal } from "../Modal";
import { binSize } from "../ShareRow";
import { useJobRun } from "../useJob";
import { JobConfirmDialog } from "./JobConfirmDialog";

/** The folder and the name of a path below the share: ["2024/Sommer", "IMG_2041.jpg"]; the folder is "" in the share's own. */
const split = (p: string): [string, string] => {
  const i = p.lastIndexOf("/");
  return i < 0 ? ["", p] : [p.slice(0, i), p.slice(i + 1)];
};

/**
 * The name a file of the bin had before it was deleted: a copy of recycle:versions ("Copy #1 of a.txt") goes back under the
 * name it is a copy of, as the server restores it (OriginalName of internal/recycle/restore.go).
 */
const originalName = (name: string) => /^Copy #[1-9][0-9]* of (.+)$/.exec(name)?.[1] ?? name;

/**
 * The name that a restored file gets when its name is taken where it goes, as the server names the first one (conflictName of
 * internal/recycle/restore.go): " (wiederhergestellt)" before the extension, which a name that starts with its dot has not.
 */
const conflictName = (name: string) => {
  const dot = name.lastIndexOf(".");
  const ext = dot > 0 ? name.slice(dot) : "";
  return `${name.slice(0, name.length - ext.length)} (wiederhergestellt)${ext}`;
};

/**
 * "Papierkorb von <share>": the files in the recycle bin of a share (GET /recycle, the newest first), each with the place it had,
 * its size and when it was deleted, and the size of the bin with the days it keeps files. "mehr als" and a note say that the
 * server's count did not see the whole bin (summary.partial); a list cut at its limit (truncated) says that it shows only the
 * newest, with the exact size. The list is read when the dialog opens and after each change, never by a new view of the page,
 * and its rows are by the path of the file, so they keep their buttons and the focus.
 *
 * "Wiederherstellen" brings a file back as the job recycle-restore; its end names where it went, and the name it gets when that
 * one is taken there: the server never overwrites. A file that is not in the bin any more is said so, and the bin is read again.
 * "Papierkorb leeren …" asks first (JobConfirmDialog, beside the dialog, which is inert behind it: it cannot be undone), then
 * runs recycle-empty. A change that is done tells
 * the page (onChanged: it says so and reads the view again, for the size of the bin in the row), and the dialog stays. Nothing
 * is changed while a job runs; the dialog closes when the share is gone from the view, unless its own job runs.
 */
export function RecycleDialog(props: {
  /** The share as the view had it at the click. */
  share: ShareView;
  view: Accessor<ViewResponse>;
  /** A job runs in the view. */
  busy: Accessor<boolean>;
  onClose: () => void;
  onChanged: (message: string) => void;
}) {
  const api = useApi();
  const { t, locale } = useI18n();
  const at = props.share;
  const [bin, setBin] = createSignal<RecycleResponse>();
  const [readError, setReadError] = createSignal<unknown>();
  const [notice, setNotice] = createSignal<string>();
  const [emptying, setEmptying] = createSignal(false);
  let latest = 0;
  /** Reads the bin; only the answer of the newest read counts, and the list before it stays until it comes. */
  const read = async () => {
    const mine = ++latest;
    try {
      const res = await api.get<RecycleResponse>(`/recycle?share=${encodeURIComponent(at.name)}`);
      if (mine === latest) {
        setBin(res);
        setReadError(undefined);
      }
    } catch (err) {
      if (mine === latest) setReadError(err);
    }
  };
  void read();
  const rows = keyed(
    () => bin()?.items ?? [],
    (it) => it.path,
  );

  const restore = useJobRun(props.onChanged, (err) => {
    if (!(err instanceof ApiError && err.code === "not_found" && typeof err.details.item === "string"))
      return false;
    setNotice(t("recycle.gone"));
    void read();
    return true;
  });
  const locked = () => restore.busy() || props.busy();
  const doneText = (p: string) => {
    const [dir, item] = split(p);
    const name = originalName(item);
    return tSafe(t, "done.recycle-restore", {
      name,
      dir: dir === "" ? at.name : `${at.name}/${dir}`,
      conflict: conflictName(name),
    });
  };
  const start = async (item: RecycleEntry) => {
    setNotice(undefined);
    const st = await restore.run({ kind: "recycle-restore", name: at.name, item: item.path }, () =>
      doneText(item.path),
    );
    if (st !== undefined || restore.failure()?.neutral) void read();
  };

  createEffect(() => {
    const gone = !props.view().config.shares.some((s) => s.name === at.name);
    if (gone && !restore.busy() && !emptying()) props.onClose();
  });

  /** The share as the view has it now, for the days of its bin. */
  const current = () => props.view().config.shares.find((s) => s.name === at.name) ?? at;
  const summary = () => {
    const sum = bin()?.summary;
    if (!sum) return undefined;
    const days = current().recycleDays;
    const keep = days === 1 ? t("recycle.dayOne") : tSafe(t, "recycle.days", { days });
    return days > 0 ? `${binSize(sum, t, locale())} · ${keep}` : binSize(sum, t, locale());
  };
  const empty = () => (bin()?.items.length ?? 0) === 0;

  return (
    <>
      <Modal
        title={tSafe(t, "recycle.title", { name: at.name })}
        wide
        busy={restore.busy()}
        onClose={props.onClose}
        actions={
          <>
            <button
              type="button"
              class="btn btn-error btn-outline mr-auto"
              disabled={locked() || empty()}
              onClick={() => setEmptying(true)}
            >
              {t("recycle.empty")}
            </button>
            <button
              type="button"
              class="btn btn-primary"
              disabled={restore.busy()}
              onClick={() => props.onClose()}
            >
              {t("common.close")}
            </button>
          </>
        }
      >
        <Show when={summary()}>{(text) => <p class="text-sm opacity-70">{text()}</p>}</Show>
        <Show when={bin()?.summary.partial}>
          <p class="text-xs opacity-70">{t("recycle.partial")}</p>
        </Show>
        <Show when={bin()?.truncated}>
          <p class="text-xs opacity-70">
            {tSafe(t, "recycle.truncated", {
              n: new Intl.NumberFormat(locale()).format(bin()?.items.length ?? 0),
            })}
          </p>
        </Show>
        <Show when={readError()}>{(err) => <p class="text-sm text-error">{errorText(err(), t)}</p>}</Show>
        <Show when={bin() === undefined && readError() === undefined}>
          <p class="text-sm opacity-70" role="status">
            {t("loading")}
          </p>
        </Show>
        <Show when={bin() !== undefined && empty()}>
          <p class="text-sm opacity-70">{t("recycle.none")}</p>
        </Show>
        <Show when={!empty()}>
          <div class="max-h-96 overflow-auto">
            <table class="table table-sm">
              <thead>
                <tr>
                  <th>{t("recycle.columns.file")}</th>
                  <th>{t("recycle.columns.place")}</th>
                  <th>{t("recycle.columns.size")}</th>
                  <th>{t("recycle.columns.deleted")}</th>
                  <th>
                    <span class="sr-only">{t("actions.label")}</span>
                  </th>
                </tr>
              </thead>
              <tbody>
                <For each={rows.keys()}>
                  {(p) => {
                    const item = rows.track(p);
                    const [dir, name] = split(p);
                    return (
                      <tr>
                        <td>{name}</td>
                        <td>{dir === "" ? "/" : `${dir}/`}</td>
                        <td class="whitespace-nowrap">{formatBytes(item().size, locale())}</td>
                        <td
                          class="whitespace-nowrap"
                          title={new Date(item().deleted).toLocaleString(locale())}
                        >
                          {formatAgo(item().deleted, Date.now(), locale())}
                        </td>
                        <td class="text-right">
                          <button
                            type="button"
                            class="btn btn-xs"
                            aria-label={`${t("recycle.restore")} ${p}`}
                            disabled={locked()}
                            onClick={() => void start(item())}
                          >
                            {t("recycle.restore")}
                          </button>
                        </td>
                      </tr>
                    );
                  }}
                </For>
              </tbody>
            </table>
          </div>
          <p class="text-xs opacity-70">{t("recycle.conflict")}</p>
        </Show>
        <Show when={notice()}>
          {(text) => (
            <div class="alert alert-warning text-sm" role="alert">
              {text()}
            </div>
          )}
        </Show>
        <Show when={props.busy() && !restore.busy()}>
          <p class="text-sm text-warning" role="status">
            {t("dialog.busy")}
          </p>
        </Show>
        <JobStatus step={restore.step()} failure={restore.failure()} />
      </Modal>
      <Show when={emptying()}>
        <JobConfirmDialog
          title={tSafe(t, "recycle.emptyTitle", { name: at.name })}
          text={tSafe(t, "recycle.emptyText", { name: at.name })}
          label={t("recycle.emptyButton")}
          danger
          request={{ kind: "recycle-empty", name: at.name }}
          done={tSafe(t, "done.recycle-empty", { name: at.name })}
          onClose={() => {
            setEmptying(false);
            // a job that failed may have removed a part of the bin
            void read();
          }}
          onDone={(message) => {
            setEmptying(false);
            void read();
            props.onChanged(message);
          }}
        />
      </Show>
    </>
  );
}
