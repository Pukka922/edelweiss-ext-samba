import { ApiError, useApi, useI18n } from "@edelweiss/sdk";
import { IconFolder } from "@tabler/icons-solidjs";
import {
  type Accessor,
  createEffect,
  createMemo,
  createSignal,
  For,
  on,
  onCleanup,
  Show,
  useContext,
} from "solid-js";
import type { BrowseResponse, MkdirResponse, MountView } from "../api";
import { errorText, tSafe } from "../errors";
import { formatBytes } from "../format";
import { PageGuard } from "../guard";
import { keyed } from "../keys";
import { Modal } from "../Modal";

/** Whether p is an absolute, clean path, which the server takes as a folder (no ".", "..", "//" or trailing "/"). */
export const cleanPath = (p: string) =>
  p.startsWith("/") &&
  (p === "/" ||
    (!p.endsWith("/") &&
      p
        .split("/")
        .slice(1)
        .every((s) => s !== "" && s !== "." && s !== "..")));

/** The data disk that holds p: the one with the longest mount point that is p or above it. */
export const mountOf = (mounts: MountView[], p: string) =>
  mounts
    .filter((m) => p === m.point || p.startsWith(m.point === "/" ? "/" : `${m.point}/`))
    .sort((a, b) => b.point.length - a.point.length)[0];

/**
 * GET /browse?dir= for the folder that dir gives, read again when it changes, after delayMs (the typing of a path pauses
 * first). Only the answer for the newest folder counts; a folder that is no clean, absolute path is not asked for. The
 * folder is read again only when it is another one: a view that the page reads again, with the same folder in it, reads
 * nothing (a typed name and the focus below the list stay). answer is the answer for the folder of now, error the error
 * of its read; refresh reads it again at once.
 */
export function useBrowse(dir: Accessor<string>, delayMs = 0) {
  const api = useApi();
  const [answer, setAnswer] = createSignal<BrowseResponse>();
  const [error, setError] = createSignal<unknown>();
  const [loading, setLoading] = createSignal(false);
  let timer: ReturnType<typeof setTimeout> | undefined;
  let latest = 0;
  const load = (d: string, delay: number) => {
    clearTimeout(timer);
    const mine = ++latest;
    setAnswer(undefined);
    setError(undefined);
    setLoading(cleanPath(d));
    if (!cleanPath(d)) return;
    const read = async () => {
      try {
        const res = await api.get<BrowseResponse>(`/browse?dir=${encodeURIComponent(d)}`);
        if (mine === latest) setAnswer(res);
      } catch (err) {
        if (mine === latest) setError(err);
      } finally {
        if (mine === latest) setLoading(false);
      }
    };
    if (delay > 0) timer = setTimeout(read, delay);
    else void read();
  };
  // a memo of a string: it tells the effect only of another folder
  const current = createMemo(dir);
  createEffect(on(current, (d) => load(d, delayMs)));
  onCleanup(() => {
    clearTimeout(timer);
    latest++;
  });
  return { answer, error, loading, refresh: () => load(current(), 0) };
}

/** The parent folder of a path below a mount point. */
const parentOf = (p: string) => p.slice(0, p.lastIndexOf("/")) || "/";

/**
 * "Neuer Ordner" in dir: the name, and the button that makes the folder (POST /browse/mkdir, after the guard of the page
 * asks for admin mode). The folder that is made is passed to onMade; a refusal stays below the name. In the folder of a
 * share (inShare) the button is disabled, and says why: a share of the new folder could never be made.
 */
function NewFolder(props: { dir: string; inShare?: string; onMade: (path: string) => void }) {
  const api = useApi();
  const { t } = useI18n();
  const guard = useContext(PageGuard);
  const [open, setOpen] = createSignal(false);
  const [name, setName] = createSignal("");
  const [busy, setBusy] = createSignal(false);
  const [error, setError] = createSignal<string>();
  const make = () =>
    void guard(async () => {
      setBusy(true);
      setError(undefined);
      try {
        const res = await api.post<MkdirResponse>("/browse/mkdir", { dir: props.dir, name: name().trim() });
        setOpen(false);
        setName("");
        props.onMade(res.path);
      } catch (err) {
        setError(
          err instanceof ApiError && err.code === "exists" ? t("navigator.exists") : errorText(err, t),
        );
      } finally {
        setBusy(false);
      }
    });
  return (
    <Show
      when={open()}
      fallback={
        <>
          <button
            type="button"
            class="btn btn-sm btn-ghost self-start"
            disabled={props.inShare !== undefined}
            onClick={() => setOpen(true)}
          >
            {t("navigator.newFolder")}
          </button>
          <Show when={props.inShare}>
            {(share) => <p class="text-xs opacity-70">{tSafe(t, "navigator.inShare", { share: share() })}</p>}
          </Show>
        </>
      }
    >
      <div class="flex flex-wrap items-center gap-2">
        <input
          type="text"
          class="input input-sm flex-1"
          aria-label={t("navigator.newName")}
          autocomplete="off"
          spellcheck={false}
          value={name()}
          disabled={busy()}
          onInput={(e) => setName(e.currentTarget.value)}
        />
        <button type="button" class="btn btn-sm btn-ghost" disabled={busy()} onClick={() => setOpen(false)}>
          {t("common.cancel")}
        </button>
        <button
          type="button"
          class="btn btn-sm btn-primary"
          disabled={busy() || name().trim() === ""}
          onClick={make}
        >
          {t("navigator.create")}
        </button>
      </div>
      <Show when={error()}>{(text) => <p class="text-xs text-error">{text()}</p>}</Show>
    </Show>
  );
}

/**
 * The navigator over the data disks: the disks to start from, the path of the folder it shows (value) with its parts to go
 * back to, and the folders in it, which it enters on a click (onChange is told the folder), with ".." to go up to the mount
 * point. It offers folders only, on the data disks only, and nothing behind a link: it lists what GET /browse lists, which
 * never names a link, and a folder that the server refuses (a link on the way) shows why, with nothing below it and only the
 * way back. With allowMkdir it offers "Neuer Ordner …" in the folder it shows. The disks are by their mount point, and the
 * folder is read again only when it is another one, so a new list of disks with the same disks (the page reads the view
 * every few seconds) keeps the buttons, the list, a typed name and the focus.
 */
export function Navigator(props: {
  mounts: MountView[];
  value: string;
  onChange: (dir: string) => void;
  allowMkdir: boolean;
}) {
  const { t, locale } = useI18n();
  /** The mount point of the disk that holds the folder it shows. */
  const point = createMemo(() => mountOf(props.mounts, props.value)?.point);
  const disks = keyed(
    () => props.mounts,
    (m) => m.point,
  );
  const browse = useBrowse(() => (point() ? props.value : ""));
  /** The folders from the mount point down to the one it shows: the mount point, then each part below it. */
  const crumbs = () => {
    const m = point();
    if (m === undefined) return [];
    const below = props.value.slice(m.length).split("/").filter(Boolean);
    return [m, ...below.map((_, i) => `${m === "/" ? "" : m}/${below.slice(0, i + 1).join("/")}`)];
  };
  const nameOf = (p: string, i: number) => (i === 0 ? p : p.slice(p.lastIndexOf("/") + 1));
  const below = (name: string) => `${props.value === "/" ? "" : props.value}/${name}`;
  return (
    <div class="flex flex-col gap-2">
      <Show
        when={props.mounts.length > 0}
        fallback={<p class="text-sm opacity-70">{t("navigator.noDisks")}</p>}
      >
        <span class="text-sm opacity-70">{t("navigator.disks")}</span>
        <div class="flex flex-wrap gap-1">
          <For each={disks.keys()}>
            {(p) => {
              const m = disks.track(p);
              return (
                <button
                  type="button"
                  class="btn btn-xs"
                  classList={{ "btn-primary btn-soft": point() === p }}
                  aria-pressed={point() === p}
                  onClick={() => props.onChange(p)}
                >
                  {tSafe(t, "navigator.disk", {
                    point: p,
                    type: m().type,
                    free: formatBytes(m().free, locale()),
                  })}
                </button>
              );
            }}
          </For>
        </div>
        <Show when={point() !== undefined} fallback={<p class="text-sm">{t("navigator.chooseDisk")}</p>}>
          <nav aria-label={t("navigator.path")} class="flex flex-wrap items-center gap-1 text-sm">
            <For each={crumbs()}>
              {(p, i) => (
                <>
                  <Show when={i() > 0}>
                    <span class="opacity-50" aria-hidden="true">
                      ›
                    </span>
                  </Show>
                  <Show
                    when={i() < crumbs().length - 1}
                    fallback={<span class="font-semibold">{nameOf(p, i())}</span>}
                  >
                    <button type="button" class="link" onClick={() => props.onChange(p)}>
                      {nameOf(p, i())}
                    </button>
                  </Show>
                </>
              )}
            </For>
          </nav>
          <ul class="menu menu-sm w-full rounded-box bg-base-200" aria-label={t("navigator.folders")}>
            <Show when={props.value !== point()}>
              <li>
                <button
                  type="button"
                  aria-label={t("navigator.up")}
                  onClick={() => props.onChange(parentOf(props.value))}
                >
                  <IconFolder size={16} aria-hidden="true" />
                  ..
                </button>
              </li>
            </Show>
            <For each={browse.answer()?.entries ?? []}>
              {(e) => (
                <li>
                  <button type="button" onClick={() => props.onChange(below(e.name))}>
                    <IconFolder size={16} aria-hidden="true" />
                    {e.name}
                  </button>
                </li>
              )}
            </For>
          </ul>
          <Show when={browse.loading()}>
            <p class="text-xs opacity-70" role="status">
              {t("navigator.reading")}
            </p>
          </Show>
          <Show when={browse.error()}>
            {(err) => <p class="text-xs text-error">{errorText(err(), t)}</p>}
          </Show>
          <Show when={browse.answer()?.entries.length === 0}>
            <p class="text-xs opacity-70">{t("navigator.empty")}</p>
          </Show>
          <Show when={browse.answer()?.truncated}>
            <p class="text-xs opacity-70">{t("navigator.truncated")}</p>
          </Show>
          <Show when={props.allowMkdir && browse.answer()}>
            <NewFolder dir={props.value} inShare={browse.answer()?.inShare} onMade={props.onChange} />
          </Show>
        </Show>
        <p class="text-xs opacity-70">{t("navigator.note")}</p>
      </Show>
    </div>
  );
}

/**
 * "Ordner wählen": the navigator in a dialog of its own, at start (a folder on a data disk, or "" to choose the disk
 * first), with the button that chooses the folder it shows (onChoose), once that is one on a data disk.
 */
export function NavigatorDialog(props: {
  mounts: MountView[];
  start: string;
  onClose: () => void;
  onChoose: (dir: string) => void;
}) {
  const { t } = useI18n();
  const [dir, setDir] = createSignal(props.start);
  const choosable = () => cleanPath(dir()) && mountOf(props.mounts, dir()) !== undefined;
  return (
    <Modal
      title={t("navigator.title")}
      wide
      onClose={props.onClose}
      actions={
        <>
          <button type="button" class="btn btn-ghost" onClick={() => props.onClose()}>
            {t("common.cancel")}
          </button>
          <button
            type="button"
            class="btn btn-primary"
            disabled={!choosable()}
            onClick={() => props.onChoose(dir())}
          >
            {tSafe(t, "navigator.choose", { dir: dir() })}
          </button>
        </>
      }
    >
      <Navigator mounts={props.mounts} value={dir()} onChange={setDir} allowMkdir />
    </Modal>
  );
}
