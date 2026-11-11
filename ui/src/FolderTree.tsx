import { useApi, useI18n } from "@edelweiss/sdk";
import { IconChevronDown, IconChevronRight } from "@tabler/icons-solidjs";
import { type Accessor, createEffect, createSignal, For, on, onCleanup, Show } from "solid-js";
import type { FolderEntry, FoldersResponse, MountView, Right, ShareView } from "./api";
import { errorText, tSafe } from "./errors";
import { keyed } from "./keys";

/**
 * A folder-rights job of a share ended (done, failed, cancelled, or its end is not known) at a folder below it: the tree of
 * that share reads its folders again, and that folder too. Each end is a new object.
 */
export interface Stale {
  share: string;
  folder: string;
}

/** The path of the folder name in dir below the share ("" is the share's folder). */
const below = (dir: string, name: string) => (dir === "" ? name : `${dir}/${name}`);

/** What the tree knows of a folder: the answer of its newest read, the error of that read, and whether a read is under way. */
interface Node {
  answer: Accessor<FoldersResponse | undefined>;
  error: Accessor<unknown>;
  reading: Accessor<boolean>;
}

interface NodeState extends Node {
  setAnswer: (a: FoldersResponse) => void;
  setError: (e: unknown) => void;
  setReading: (on: boolean) => void;
  /** The number of the newest read; 0 for a folder that was never read. */
  latest: number;
}

/**
 * The folders of a share, read with GET /folders one folder at a time. node(dir) is what is known of dir; load reads it (again:
 * only the answer of the newest read counts, and the answer before it stays until that one comes, so the lines stay); known says
 * whether it was read; reload reads again every folder that was. A tree that is gone takes no answer.
 */
function useFolders(share: string) {
  const api = useApi();
  const nodes = new Map<string, NodeState>();
  let alive = true;
  onCleanup(() => {
    alive = false;
  });
  const node = (dir: string): NodeState => {
    const there = nodes.get(dir);
    if (there) return there;
    const [answer, setAnswer] = createSignal<FoldersResponse>();
    const [error, setError] = createSignal<unknown>();
    const [reading, setReading] = createSignal(false);
    const made: NodeState = { answer, setAnswer, error, setError, reading, setReading, latest: 0 };
    nodes.set(dir, made);
    return made;
  };
  const load = async (dir: string) => {
    const n = node(dir);
    const mine = ++n.latest;
    const current = () => alive && mine === n.latest;
    n.setReading(true);
    try {
      const res = await api.get<FoldersResponse>(
        `/folders?share=${encodeURIComponent(share)}&dir=${encodeURIComponent(dir)}`,
      );
      if (current()) {
        n.setAnswer(res);
        n.setError(undefined);
      }
    } catch (err) {
      if (current()) n.setError(err);
    } finally {
      if (current()) n.setReading(false);
    }
  };
  return {
    node: (dir: string): Node => node(dir),
    load: (dir: string) => void load(dir),
    known: (dir: string) => (nodes.get(dir)?.latest ?? 0) > 0,
    reload: () => {
      for (const [dir, n] of nodes) if (n.latest > 0) void load(dir);
    },
  };
}

/** What the lines of a tree share: its folders, the folders that are open, and what a click on a folder does. */
interface Tree {
  folders: ReturnType<typeof useFolders>;
  open: Accessor<ReadonlySet<string>>;
  toggle: (path: string) => void;
  /** Folder rights can be set: the disk keeps them, as far as the page knows, and the share is no system or root one. */
  editable: Accessor<boolean>;
  /**
   * A share with the warning system or root_owned: no rights can be set, but those that exist can be removed, and a mixed
   * folder can be applied again (the server refuses folder_rights_unsafe for anything else).
   */
  limited: Accessor<boolean>;
  busy: Accessor<boolean>;
  onFolder: (path: string) => void;
}

/** The mark of a right in a list: ✎ read and write, 👁 read only. */
const mark = (r: Right) => (r === "write" ? "✎" : "👁");

/** What of a share the folder tree shows: its rights, the folders with rights of their own, and whether it runs per user. */
const rightsOf = (s: ShareView) => JSON.stringify([s.rights, s.folders, s.perUser]);

/**
 * The tree of the folders of a share, below its row: the share's folder first (GET /folders with dir ""), and a folder when it
 * is opened, each once (lazily). Each folder says whose rights it has: "wie die Freigabe", "eigene Rechte" (🔒, with the
 * rights, which are read with the folder it is in) or "erbt von …". A click on a folder asks the page for its rights
 * (onFolder), not while a job runs. A folder whose files do not all have its rights (after a cancel: disk.mixed) is
 * "gemischter Stand", with "erneut anwenden", which opens its rights; so is the share's own folder (onFolder with "").
 *
 * On a disk without extended attributes (the view's mount), or once the server has said that the disk cannot keep folder rights
 * (aclReason, from a refusal), the tree says why and is there to look at only. So it is for a share whose folder has the
 * warning system or root_owned (the view's warnings), which says so with folder_rights_unsafe; there the folders with rights
 * of their own still open (to remove them), and "erneut anwenden" stays. The folders are by name and the tree is read again
 * only when the share's rights change or a folder-rights job of the share ended (stale), so a new view every few seconds keeps
 * its lines, the open folders and the focus. The end of a job comes before the view that the page reads after it: that view
 * brings what the reload for the end has read already, so it reads nothing again, whatever it changes.
 */
export function FolderTree(props: {
  share: ShareView;
  /** The disk of the share, as the view has it. */
  mount: MountView | undefined;
  /** Why the disk cannot keep folder rights, as the server said it (a no_acl refusal). */
  aclReason: string | undefined;
  stale: Stale | undefined;
  busy: boolean;
  onFolder: (folder: string) => void;
}) {
  const { t } = useI18n();
  const name = props.share.name;
  const folders = useFolders(name);
  const [open, setOpen] = createSignal<ReadonlySet<string>>(new Set());
  folders.load("");

  /** The end of a folder-rights job of the share has been read: the next view of the share is the one read after it. */
  let ended = false;
  let rights = rightsOf(props.share);
  // a new view with the same rights reads nothing; other rights change the marks and the caps
  createEffect(
    on(
      () => props.share,
      (s) => {
        const now = rightsOf(s);
        const changed = now !== rights;
        rights = now;
        if (ended) ended = false;
        else if (changed) folders.reload();
      },
      { defer: true },
    ),
  );
  createEffect(
    on(
      () => props.stale,
      (s) => {
        if (s?.share !== name) return;
        folders.reload();
        if (s.folder !== "" && !folders.known(s.folder)) folders.load(s.folder);
        ended = true;
      },
      { defer: true },
    ),
  );

  const reason = () => props.aclReason ?? (props.mount?.xattr === false ? "no_xattr" : undefined);
  const reasonText = (r: string) => {
    const key = `errors.aclReason.${r}`;
    const why = t(key);
    return `${tSafe(t, "errors.noAcl", { reason: why === key ? r : why })} ${t("folders.viewOnly")}`;
  };
  /** The share's folder is a system folder or belongs to root: folder rights are refused (folder_rights_unsafe). */
  const unsafe = () => props.share.warnings.some((w) => w === "system" || w === "root_owned");
  const tree: Tree = {
    folders,
    open,
    toggle: (path) => {
      const next = new Set(open());
      if (next.has(path)) next.delete(path);
      else {
        next.add(path);
        if (!folders.known(path)) folders.load(path);
      }
      setOpen(next);
    },
    editable: () => reason() === undefined && !unsafe(),
    limited: () => reason() === undefined && unsafe(),
    busy: () => props.busy,
    onFolder: (path) => props.onFolder(path),
  };
  return (
    <div class="flex flex-col gap-2 py-1">
      <p class="text-xs opacity-70">
        {props.mount
          ? tSafe(t, "folders.disk", { path: props.share.path, type: props.mount.type })
          : props.share.path}
      </p>
      <Show when={reason()}>{(r) => <p class="text-sm text-warning">{reasonText(r())}</p>}</Show>
      <Show when={tree.limited()}>
        <p class="text-sm text-warning">{t("errors.folder_rights_unsafe")}</p>
      </Show>
      <Show when={folders.node("").answer()?.disk?.mixed}>
        <div class="flex flex-wrap items-center gap-2">
          <p class="text-sm text-warning">{t("folders.rootMixed")}</p>
          <Show when={tree.editable() || tree.limited()}>
            <button
              type="button"
              class="btn btn-ghost btn-xs"
              disabled={props.busy}
              onClick={() => props.onFolder("")}
            >
              {t("folders.reapply")}
            </button>
          </Show>
        </div>
      </Show>
      <FolderList tree={tree} dir="" />
      <Show when={tree.editable()}>
        <p class="text-xs opacity-70">{t("folders.hint")}</p>
      </Show>
    </div>
  );
}

/**
 * The folders in dir, by name: a new answer with the same folders keeps their lines. The folders with rights of their own are
 * read along, for their rights and their state.
 */
function FolderList(props: { tree: Tree; dir: string }) {
  const { t } = useI18n();
  const n = props.tree.folders.node(props.dir);
  const rows = keyed(
    () => n.answer()?.entries ?? [],
    (e) => e.name,
  );
  createEffect(() => {
    for (const e of n.answer()?.entries ?? []) {
      const path = below(props.dir, e.name);
      if (e.own && !props.tree.folders.known(path)) props.tree.folders.load(path);
    }
  });
  return (
    <>
      <ul class="flex flex-col gap-1" classList={{ "ml-6": props.dir !== "" }}>
        <For each={rows.keys()}>
          {(entry) => <FolderLine tree={props.tree} dir={props.dir} entry={rows.track(entry)} />}
        </For>
      </ul>
      <Show when={n.reading() && !n.answer()}>
        <p class="text-xs opacity-70" role="status">
          {t("folders.reading")}
        </p>
      </Show>
      <Show when={n.error()}>{(err) => <p class="text-xs text-error">{errorText(err(), t)}</p>}</Show>
      <Show when={n.answer()?.entries.length === 0}>
        <p class="text-xs opacity-70">{t("folders.empty")}</p>
      </Show>
      <Show when={n.answer()?.truncated}>
        <p class="text-xs opacity-70">{t("folders.truncated")}</p>
      </Show>
    </>
  );
}

/** A folder of the tree: open or close it, its name (a click asks for its rights), its rights and its state, and what is in it. */
function FolderLine(props: { tree: Tree; dir: string; entry: Accessor<FolderEntry> }) {
  const { t } = useI18n();
  const name = props.entry().name;
  const path = below(props.dir, name);
  const parent = props.tree.folders.node(props.dir);
  const self = props.tree.folders.node(path);
  const own = () => props.entry().own;
  const marker = () => {
    if (own()) return t("folders.own");
    const from = parent.answer()?.from ?? "";
    return from === "" ? t("folders.same") : tSafe(t, "folders.from", { from });
  };
  const badges = () =>
    own()
      ? Object.entries(self.answer()?.rights ?? {})
          .filter(([, r]) => r !== "")
          .sort(([a], [b]) => a.localeCompare(b))
      : [];
  const mixed = () => self.answer()?.disk?.mixed === true;
  const expanded = () => props.tree.open().has(path);
  const icon = () => <span aria-hidden="true">{own() ? "🔒" : "📁"}</span>;
  return (
    <li>
      <div class="flex flex-wrap items-center gap-1 text-sm" data-folder={path}>
        <Show
          when={props.entry().hasChildren}
          fallback={<span class="inline-block w-6" aria-hidden="true" />}
        >
          <button
            type="button"
            class="btn btn-ghost btn-xs btn-square"
            aria-label={tSafe(t, "folders.open", { name })}
            aria-expanded={expanded()}
            onClick={() => props.tree.toggle(path)}
          >
            <Show when={expanded()} fallback={<IconChevronRight size={14} aria-hidden="true" />}>
              <IconChevronDown size={14} aria-hidden="true" />
            </Show>
          </button>
        </Show>
        <Show
          when={props.tree.editable() || (props.tree.limited() && (own() || mixed()))}
          fallback={
            <span>
              {icon()} {name}
            </span>
          }
        >
          <button
            type="button"
            class="link link-hover"
            disabled={props.tree.busy()}
            onClick={() => props.tree.onFolder(path)}
          >
            {icon()} {name}
          </button>
        </Show>
        <For each={badges()}>
          {([user, right]) => (
            <span
              class="badge badge-sm badge-soft whitespace-nowrap"
              classList={{ "badge-success": right === "write", "badge-info": right === "read" }}
            >
              {user} {mark(right)}
            </span>
          )}
        </For>
        <span class="text-xs opacity-70">· {marker()}</span>
        <Show when={mixed()}>
          <span class="badge badge-sm badge-warning badge-soft">{t("folders.mixed")}</span>
          <Show when={props.tree.editable() || props.tree.limited()}>
            <button
              type="button"
              class="btn btn-ghost btn-xs"
              disabled={props.tree.busy()}
              onClick={() => props.tree.onFolder(path)}
            >
              {t("folders.reapply")}
            </button>
          </Show>
        </Show>
      </div>
      <Show when={expanded()}>
        <FolderList tree={props.tree} dir={path} />
      </Show>
    </li>
  );
}
