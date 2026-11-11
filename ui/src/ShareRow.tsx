import { type Translator, useI18n } from "@edelweiss/sdk";
import { IconChevronDown, IconChevronRight, IconPencil, IconTrash, IconX } from "@tabler/icons-solidjs";
import { For, Show } from "solid-js";
import type { ShareView, SummaryView } from "./api";
import { tSafe } from "./errors";
import { formatBytes } from "./format";
import { IconButton } from "./IconButton";

/** What a click on a row asks the page for: the edit of the share, its removal, or its recycle bin. */
export type ShareAction = "edit" | "delete" | "recycle";

/** The size of a recycle bin: "1,2 GB · 340 Dateien", "4,1 MB · 1 Datei", and "mehr als …" when it was not counted to its end. */
export function binSize(bin: SummaryView, t: Translator["t"], locale: string): string {
  const size = formatBytes(bin.bytes, locale);
  const files = new Intl.NumberFormat(locale).format(bin.files);
  if (bin.partial) return tSafe(t, "shares.recyclePartial", { size, files });
  return bin.files === 1
    ? tSafe(t, "shares.recycleSizeOne", { size })
    : tSafe(t, "shares.recycleSize", { size, files });
}

/** The text of key, or fallback when there is none. */
const orElse = (text: string, key: string, fallback: string) => (text === key ? fallback : text);

/**
 * A row of the shares: the name with its description and "versteckt", the folder, the rights as badges (✎ read and write,
 * 👁 read only, by user name; from the config, so they need no Samba users that the page can read), the recycle bin (its
 * size, "aus", or a dash where its disk is not there), and the status. The status is "verfügbar" only for a share that
 * smbd serves and whose folder is there: a share that smb.conf leaves out (skipped) is "nicht bereitgestellt" with the
 * reason, one whose folder cannot be served says why ("Platte fehlt", "Ordner fehlt", "Ordner ist ein Link", "Besitzer
 * fehlt"). The actions are the edit, the recycle bin (where it is on and the disk is there) and the delete, disabled while
 * busy; a readOnly page has none. Before the name, a share whose folder is there opens and closes its folder tree (onToggle;
 * expanded: it is open), also while busy: the tree is a look. A name of deleted (it has no Samba entry any more) is marked
 * "gelöscht" instead of its right: it has no access.
 */
export function ShareRow(props: {
  share: ShareView;
  /** The names without a Samba entry (the page knows the Samba users): their rights grant nothing. */
  deleted: string[];
  readOnly: boolean;
  busy: boolean;
  expanded: boolean;
  onToggle: () => void;
  onAction: (kind: ShareAction, share: ShareView) => void;
}) {
  const { t, locale } = useI18n();
  const s = () => props.share;
  const rights = () =>
    Object.entries(s().rights)
      .filter(([, r]) => r !== "")
      .sort(([a], [b]) => a.localeCompare(b));
  const reasonText = (reason: string) =>
    orElse(t(`shares.reason.${reason}`), `shares.reason.${reason}`, reason);
  const skipText = (reason: string) =>
    orElse(t(`shares.skipped.${reason}`), `shares.skipped.${reason}`, reason);
  const label = () => {
    if (s().skipped !== "") return t("shares.notServed");
    return s().available ? t("shares.available") : reasonText(s().reason);
  };
  /** What the status says below its label: the reason of a share that is left out, and why its folder cannot be served. */
  const details = () => {
    if (s().skipped === "") return "";
    const parts = [skipText(s().skipped)];
    if (!s().available) parts.push(reasonText(s().reason));
    return [...new Set(parts)].join(" · ");
  };
  const ok = () => s().available && s().skipped === "";
  const recycle = () => {
    const sh = s();
    if (!sh.recycle) return t("shares.recycleOff");
    return sh.recycleBin ? binSize(sh.recycleBin, t, locale()) : "–";
  };
  const action = (kind: ShareAction, icon: typeof IconPencil, danger?: boolean) => (
    <IconButton
      icon={icon}
      label={`${t(`actions.${kind}`)} ${s().name}`}
      tooltip={t(`actions.${kind}`)}
      danger={danger}
      disabled={props.busy}
      onClick={() => props.onAction(kind, s())}
    />
  );
  return (
    <tr>
      <td>
        <Show when={!props.readOnly && s().available}>
          <button
            type="button"
            class="btn btn-ghost btn-xs btn-square mr-1"
            aria-label={`${t("actions.folders")} ${s().name}`}
            aria-expanded={props.expanded}
            onClick={() => props.onToggle()}
          >
            <Show when={props.expanded} fallback={<IconChevronRight size={14} aria-hidden="true" />}>
              <IconChevronDown size={14} aria-hidden="true" />
            </Show>
          </button>
        </Show>
        <span class="font-semibold">{s().name}</span>
        <Show when={s().hidden}>
          <span class="badge badge-sm badge-ghost ml-2">{t("shares.hidden")}</span>
        </Show>
        <Show when={s().comment !== ""}>
          <div class="text-xs opacity-70">{s().comment}</div>
        </Show>
      </td>
      <td>
        <code>{s().path}</code>
      </td>
      <td>
        <div class="flex flex-wrap gap-1">
          <For each={rights()} fallback="–">
            {([user, right]) => (
              <Show
                when={!props.deleted.includes(user)}
                fallback={
                  <span class="badge badge-sm badge-ghost whitespace-nowrap opacity-70">
                    {user} · {t("rights.deleted")}
                  </span>
                }
              >
                <span
                  class="badge badge-sm badge-soft whitespace-nowrap"
                  classList={{ "badge-success": right === "write", "badge-info": right === "read" }}
                >
                  {user} {right === "write" ? "✎" : "👁"}
                </span>
              </Show>
            )}
          </For>
        </div>
      </td>
      <td class="whitespace-nowrap" classList={{ "opacity-70": !s().recycleBin }}>
        {recycle()}
      </td>
      <td>
        <span class="flex items-center gap-2">
          <span
            class={`inline-block size-2 shrink-0 rounded-full ${ok() ? "bg-success" : "bg-warning"}`}
            aria-hidden="true"
          />
          <span classList={{ "text-warning": !ok() }}>{label()}</span>
        </span>
        <Show when={details() !== ""}>
          <div class="text-xs opacity-70">{details()}</div>
        </Show>
      </td>
      <td class="w-px whitespace-nowrap text-right">
        <Show when={!props.readOnly}>
          <div class="flex items-center justify-end">
            {action("edit", IconPencil)}
            <Show when={s().recycle && s().available}>{action("recycle", IconTrash)}</Show>
            {action("delete", IconX, true)}
          </div>
        </Show>
      </td>
    </tr>
  );
}
