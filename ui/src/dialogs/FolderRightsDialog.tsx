import { ApiError, useApi, useI18n } from "@edelweiss/sdk";
import { type Accessor, createSignal, Show } from "solid-js";
import type { FoldersResponse, JobRequest, Right, ShareView, ViewResponse } from "../api";
import { Buttons } from "../Buttons";
import { errorText, tSafe } from "../errors";
import { Callout, JobStatus, Modal } from "../Modal";
import { CloseButton } from "./ChangeButtons";
import { useChange } from "./change";
import { RightsTable, shownRights } from "./RightsTable";

/** Whether two sets of rights give the same users the same rights. */
const sameRights = (a: Record<string, Right>, b: Record<string, Right>) =>
  Object.keys(a).length === Object.keys(b).length && Object.entries(a).every(([u, r]) => b[u] === r);

const isCount = (n: unknown): n is number => typeof n === "number" && Number.isInteger(n) && n >= 0;

/** What the conversion changes, as needs_convert says it: the files and folders, and whether there are more (capped). */
interface Conversion {
  files: number;
  capped: boolean;
}

/**
 * "Rechte für „<share>/<folder>“": the rights of a folder below a share (GET /folders, read once: a new view of the page reads
 * nothing again and keeps the table), whose own rights they are ("eigene Rechte", "erbt von …", "wie die Freigabe"), and the
 * table of the Samba users of the view, each at most what the folder may have (max: folder rights only narrow; the cap is the
 * share's for a folder in the share's own folder, the folder above's further down). What is sent is what the table shows.
 * "Anwenden" starts the job folder-rights, once the table differs from the folder's rights.
 *
 * - A share that does not run per user yet answers the first "Anwenden" with needs_convert: the dialog then says what the
 *   conversion does to the existing files and how many it changes ("mehr als" where the server says it stopped counting), and
 *   "Umstellen und anwenden" sends the same with convert.
 * - While the job runs, the dialog shows its step and how far it is, and "Vorgang abbrechen" asks it to stop
 *   (POST /jobs/<id>/cancel) while it is in a step that can be cancelled. A cancelled job says that what was done stays; the
 *   tree of the share shows what is left (onEnded, for every end of a job, before the page reads the view again).
 * - A folder whose files do not all have its rights (disk.mixed, after a cancel) says so. As long as the table is unchanged,
 *   "Erneut anwenden" sends the rights again for the folder they come from: its own, the one it inherits them from, or the
 *   share's own folder (folder "" without rights: the share's rights from the top down).
 * - The share's own folder (folder "") shows the share's rights, which change with the share: only a mixed state is applied
 *   again there.
 * - "Rechte der Freigabe übernehmen" takes the own rights of the folder away (inherit).
 * - A disk that cannot keep folder rights (no_acl) ends the dialog with the reason, which the page keeps for the tree (onNoAcl).
 *
 * The dialog closes when the share is gone from the view, unless its job runs.
 */
export function FolderRightsDialog(props: {
  /** The share as the view had it at the click. */
  share: ShareView;
  /** The folder below the share; "" is the share's own. */
  folder: string;
  view: Accessor<ViewResponse>;
  /** A job runs in the view. */
  busy: Accessor<boolean>;
  onClose: () => void;
  onDone: (message: string) => void;
  onReload: () => void;
  /** A job of the dialog ended, whatever its end; told before onDone and onReload. */
  onEnded: () => void;
  /** The server said why the disk of the share cannot keep folder rights. */
  onNoAcl: (reason: string) => void;
}) {
  const api = useApi();
  const { t, locale } = useI18n();
  const at = props.share;
  const folder = props.folder;
  const root = folder === "";
  /** The folder is below another folder: what it may have is what the folder above gives. */
  const nested = folder.includes("/");
  const pathOf = (f: string) => (f === "" ? at.name : `${at.name}/${f}`);
  const path = pathOf(folder);
  const [info, setInfo] = createSignal<FoldersResponse>();
  const [readError, setReadError] = createSignal<unknown>();
  const [rights, setRights] = createSignal<Record<string, Right>>({});
  /** What the conversion changes, once the server asked for it. */
  const [convert, setConvert] = createSignal<Conversion>();
  const [noAcl, setNoAcl] = createSignal(false);
  const [cancelling, setCancelling] = createSignal(false);
  const [cancelError, setCancelError] = createSignal<string>();
  api
    .get<FoldersResponse>(`/folders?share=${encodeURIComponent(at.name)}&dir=${encodeURIComponent(folder)}`)
    .then(
      (res) => {
        setInfo(res);
        setRights({ ...res.rights });
      },
      (err: unknown) => setReadError(err),
    );

  const change = useChange({
    onDone: (message) => {
      props.onEnded();
      props.onDone(message);
    },
    onReload: () => {
      props.onEnded();
      props.onReload();
    },
    leave: {
      gone: () => !props.view().config.shares.some((s) => s.name === at.name),
      onClose: props.onClose,
    },
    onRefused: (err) => {
      if (!(err instanceof ApiError)) return false;
      if (err.code === "needs_convert" && isCount(err.details.files)) {
        setConvert({ files: err.details.files, capped: err.details.capped === true });
        return true;
      }
      if (err.code === "no_acl") {
        setNoAcl(true);
        if (typeof err.details.reason === "string") props.onNoAcl(err.details.reason);
      }
      return false;
    },
  });

  const users = () => props.view().users.filter((u) => !u.noEntry);
  const known = () => props.view().usersKnown;
  const shown = () => shownRights(users(), rights(), info()?.max ?? {});
  /** The rights of the folder as the config has them, as the table shows them. */
  const stored = () => {
    const i = info();
    return i ? shownRights(users(), i.rights, i.max) : {};
  };
  const unchanged = () => sameRights(shown(), stored());
  const mixed = () => info()?.disk?.mixed === true;
  /** The folder whose rights the folder has: itself, the nearest one above with rights of its own, or "" (the share's). */
  const source = () => {
    const i = info();
    return i === undefined || root ? "" : i.own ? folder : i.from;
  };
  /** A mixed state that is applied again as it is: the rights of the folder they come from, the table unchanged. */
  const reapplying = () => mixed() && unchanged();
  const over = () => change.over() || noAcl() || readError() !== undefined;
  const locked = () => change.busy() || over() || root;
  const canApply = () => !props.busy() && known() && info() !== undefined && (reapplying() || !unchanged());
  const label = () =>
    t(convert() !== undefined ? "folder.convertApply" : reapplying() ? "folder.reapply" : "folder.apply");

  const run = (req: JobRequest, done: string) => {
    setCancelling(false);
    setCancelError(undefined);
    void change.run(req, () => done);
  };
  const apply = () => {
    if (reapplying()) {
      const from = source();
      // the share's own folder takes no rights: the share's are applied again
      const again: JobRequest =
        from === ""
          ? { kind: "folder-rights", name: at.name, folder: "" }
          : { kind: "folder-rights", name: at.name, folder: from, rights: stored() };
      run(again, tSafe(t, "done.folder-rights", { path: pathOf(from) }));
      return;
    }
    const converting = convert() !== undefined;
    run(
      {
        kind: "folder-rights",
        name: at.name,
        folder,
        rights: shown(),
        ...(converting ? { convert: true } : {}),
      },
      converting
        ? tSafe(t, "done.folder-convert", { share: at.name, path })
        : tSafe(t, "done.folder-rights", { path }),
    );
  };
  const inherit = () =>
    run(
      { kind: "folder-rights", name: at.name, folder, inherit: true },
      tSafe(t, "done.folder-inherit", { path }),
    );
  const cancel = async () => {
    const st = change.running();
    if (!st) return;
    setCancelling(true);
    setCancelError(undefined);
    try {
      await api.post(`/jobs/${encodeURIComponent(st.id)}/cancel`);
    } catch (err) {
      setCancelling(false);
      setCancelError(errorText(err, t));
    }
  };

  const marker = (i: FoldersResponse) => {
    if (i.own) return t("folders.own");
    return i.from === "" ? t("folders.same") : tSafe(t, "folders.from", { from: i.from });
  };
  const count = (c: Conversion) =>
    tSafe(t, c.capped ? "folder.convertMore" : "folder.convertFiles", {
      files: new Intl.NumberFormat(locale()).format(c.files),
    });
  const note = () => t(root ? "folder.root" : nested ? "folder.noteAbove" : "folder.note");

  return (
    <Modal
      title={tSafe(t, "folder.title", { path })}
      wide
      busy={change.busy()}
      onClose={props.onClose}
      actions={
        <Show when={!over()} fallback={<CloseButton onClose={props.onClose} />}>
          <Show when={change.busy()}>
            <button
              type="button"
              class="btn btn-ghost mr-auto"
              disabled={!change.running()?.cancellable || cancelling()}
              onClick={() => void cancel()}
            >
              {t("folder.cancel")}
            </button>
          </Show>
          <Show when={!change.busy() && info()?.own}>
            <button
              type="button"
              class="btn btn-ghost mr-auto"
              disabled={props.busy() || !known()}
              onClick={inherit}
            >
              {t("folder.inherit")}
            </button>
          </Show>
          <Buttons
            busy={change.busy()}
            disabled={!canApply()}
            label={label()}
            onClose={props.onClose}
            onConfirm={apply}
          />
        </Show>
      }
    >
      <Show
        when={info()}
        fallback={
          <Show
            when={readError()}
            fallback={
              <p class="text-sm opacity-70" role="status">
                {t("navigator.reading")}
              </p>
            }
          >
            {(err) => <p class="text-sm text-error">{errorText(err(), t)}</p>}
          </Show>
        }
      >
        {(i) => (
          <>
            <Show when={!root}>
              <p class="text-sm opacity-70">{marker(i())}</p>
            </Show>
            <Show when={known()} fallback={<p class="text-sm text-warning">{t("folder.usersUnknown")}</p>}>
              <Show
                when={users().some((u) => (i().max[u.name] ?? "") !== "")}
                fallback={
                  <p class="text-sm opacity-70">{t(nested ? "folder.nobodyAbove" : "folder.nobody")}</p>
                }
              >
                <RightsTable
                  users={users()}
                  value={rights()}
                  max={i().max}
                  maxAbove={nested}
                  disabled={locked()}
                  usersKnown
                  onChange={setRights}
                />
              </Show>
            </Show>
            <p class="text-xs opacity-70">{note()}</p>
            <Show when={mixed()}>
              <Callout tone="warn">{t("folder.mixed")}</Callout>
            </Show>
            <Show when={i().disk?.partial}>
              <p class="text-xs opacity-70">{t("folder.partial")}</p>
            </Show>
            <Show when={convert()} fallback={<Callout tone="info">{t("folder.files")}</Callout>}>
              {(c) => (
                <Callout tone="warn">
                  <p class="font-semibold">{t("folder.convertTitle")}</p>
                  <p>{t("folder.convertText")}</p>
                  <p>{count(c())}</p>
                </Callout>
              )}
            </Show>
          </>
        )}
      </Show>
      <Show when={props.busy() && !change.busy()}>
        <p class="text-sm text-warning" role="status">
          {t("dialog.busy")}
        </p>
      </Show>
      <JobStatus step={change.step()} failure={change.failure()} progress={change.running()?.progress} />
      <Show when={cancelling() && change.busy()}>
        <p class="text-sm" role="status">
          {t("folder.cancelling")}
        </p>
      </Show>
      <Show when={cancelError()}>{(text) => <p class="text-sm text-error">{text()}</p>}</Show>
    </Modal>
  );
}
