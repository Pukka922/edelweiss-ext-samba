import { ApiError, useI18n, useSession } from "@edelweiss/sdk";
import { type Accessor, createEffect, createSignal, createUniqueId, For, on, Show } from "solid-js";
import type { OwnerView, Right, ShareRequest, ShareView, ViewResponse } from "../api";
import { errorText, tSafe } from "../errors";
import { serverName } from "../format";
import { Check, Field, JobStatus, Modal } from "../Modal";
import { ChangeButtons } from "./ChangeButtons";
import { useChange } from "./change";
import { cleanPath, NavigatorDialog, useBrowse } from "./Navigator";
import { OwnerLine, ownerOf } from "./OwnerLine";
import { RightsTable } from "./RightsTable";
import { ServiceAccountDialog } from "./ServiceAccountDialog";
import { type FormField, fieldOf } from "./shareRefusal";

/** How long the typing of a folder has to pause before its owner is read, in milliseconds. */
export const CHECK_MS = 300;

/** The warnings of a folder that the page has a text for (paths.Warn*). */
const KNOWN_WARNINGS = ["system", "root_owned", "holds_mount", "ram"];

/** The days a recycle bin keeps files (recycle.MinDays to recycle.MaxDays), and what a new one starts with. */
const MIN_DAYS = 1;
const MAX_DAYS = 3650;
const DEFAULT_DAYS = 30;

/** Whether days is a whole number of days that a recycle bin keeps files. */
const validDays = (days: string) =>
  /^\d+$/.test(days) && Number(days) >= MIN_DAYS && Number(days) <= MAX_DAYS;

/**
 * Adds a share, or (share given) changes one: the name in the network, the folder (typed, or chosen with "Durchsuchen …" in
 * the navigator, which can also make a folder), the description, who may do what (RightsTable over the Samba users),
 * "versteckt", and the recycle bin with its days (30 when it is turned on, 1 to 3650). Below the folder the dialog names its
 * owner, as GET /browse reads it once the typing pauses: Samba accesses the files as that owner. A folder of root is not
 * sent; a folder whose owner has no account (the server's rule: no name of its user or its group) offers "Dienstkonto
 * anlegen" (ServiceAccountDialog), and the line is read again once the account is there. A share with folder rights keeps
 * its folder. The folder of a share whose disk is not mounted says "Platte fehlt", as its row does, and is read again
 * when the disk is back; a folder that the server refuses with a reason (a link, /proc, /sys, /dev) is not sent either.
 *
 * Any folder can be a share. The warnings of the folder (GET /browse, or those of a 409 confirm_needed) stand as one red
 * alert, one line each, and the share is only sent once "Ich weiß, was ich tue" is ticked, with acknowledge: the codes of
 * the warnings that the alert showed; this goes for an edit too, and so for a share that smb.conf leaves out as
 * unconfirmed (the folder came to belong to root since): saving it with the box ticked stores the confirmation. A 409
 * confirm_needed always asks for the box, and when it names no warning that the page knows, the alert starts with
 * errors.confirm_needed, so that nothing is sent again silently. Another folder unticks the box. Save waits until the
 * folder is read.
 *
 * The change runs as a job (useChange). A refusal about a field (shareRefusal.fieldOf) is shown at that field, and the form
 * can be sent again; any other refusal stands below the form. Only users of the view get rights, so a
 * share is not changed while the Samba users cannot be read (usersKnown). The edit is about the share as it was at the
 * click, and closes when the share is gone from the view.
 */
export function ShareDialog(props: {
  share?: ShareView;
  view: Accessor<ViewResponse>;
  /** A job runs in the view. */
  busy: Accessor<boolean>;
  onClose: () => void;
  onDone: (message: string) => void;
  /** A change is done that leaves the dialog open (a service account): the page tells it and reads the view again. */
  onChanged: (message: string) => void;
  onReload: () => void;
}) {
  const { t } = useI18n();
  const session = useSession();
  const edit = props.share;
  const perUser = edit?.perUser === true;
  const [name, setName] = createSignal(edit?.name ?? "");
  const [path, setPath] = createSignal(edit?.path ?? "");
  const [comment, setComment] = createSignal(edit?.comment ?? "");
  const [rights, setRights] = createSignal<Record<string, Right>>({ ...(edit?.rights ?? {}) });
  const [hidden, setHidden] = createSignal(edit?.hidden ?? false);
  const [recycle, setRecycle] = createSignal(edit?.recycle ?? false);
  // a share without a recycle bin has no days: they are set when it is turned on
  const [days, setDays] = createSignal(edit && edit.recycleDays > 0 ? String(edit.recycleDays) : "");
  const [refused, setRefused] = createSignal<Partial<Record<FormField, string>>>({});
  const [browsing, setBrowsing] = createSignal<string>();
  const [know, setKnow] = createSignal(false);
  // the warnings that a 409 confirm_needed named, for a folder whose read said none (or none yet)
  const [named, setNamed] = createSignal<string[]>();
  // a 409 confirm_needed was the answer for this folder: the box is needed, whatever it named
  const [demanded, setDemanded] = createSignal(false);
  const [account, setAccount] = createSignal<{ path: string; owner: OwnerView }>();
  const folder = useBrowse(path, CHECK_MS);
  const id = createUniqueId();

  const forget = (field: FormField) => setRefused(({ [field]: _, ...rest }) => rest);
  const users = () => props.view().users.filter((u) => !u.noEntry);
  const known = () => props.view().usersKnown;

  const change = useChange({
    onDone: props.onDone,
    onReload: props.onReload,
    onRefused: (err) => {
      if (err instanceof ApiError && err.code === "confirm_needed") {
        const w = err.details.warnings;
        setNamed(Array.isArray(w) ? w.filter((x): x is string => typeof x === "string") : []);
        setDemanded(true);
        return true;
      }
      const at = fieldOf(err, t);
      if (at) setRefused({ [at[0]]: at[1] });
      return at !== undefined;
    },
    leave: edit && {
      gone: () => !props.view().config.shares.some((s) => s.name === edit.name),
      onClose: props.onClose,
    },
  });
  const locked = () => change.busy() || change.over();
  // another folder: its warnings are not the ones that were ticked
  createEffect(
    on(
      path,
      () => {
        setKnow(false);
        setNamed(undefined);
        setDemanded(false);
      },
      { defer: true },
    ),
  );

  /** The edited share keeps its folder, and the view says that the disk of it is not mounted. */
  const unplugged = () => {
    if (!edit || path() !== edit.path) return false;
    const now = props.view().config.shares.find((s) => s.name === edit.name) ?? edit;
    return !now.available && now.reason === "unmounted";
  };
  // the disk is back: its folder is read again (its read was refused while the disk was away); the first run has no was
  createEffect(
    on(unplugged, (now, was) => {
      if (was && !now) folder.refresh();
    }),
  );
  /** What the owner of the folder means for the share, once it is read (none for a disk that is not mounted). */
  const owner = () =>
    !cleanPath(path()) || folder.loading() || unplugged() ? undefined : ownerOf(folder.answer()?.owner);
  /** The server refused to read the folder for a reason (a link on the way, the kernel's folders, no folder). */
  const refusedFolder = () => {
    const read = folder.error();
    return read instanceof ApiError && typeof read.details.reason === "string";
  };
  /** The text below the folder: a refusal of it, the disk that is not mounted, else why it cannot be read. */
  const pathError = () => {
    const err = refused().path;
    if (err) return err;
    if (unplugged()) return t("shares.reason.unmounted");
    const read = folder.error();
    if (read === undefined) return undefined;
    return read instanceof ApiError && read.code === "not_found"
      ? t("share.pathMissing")
      : errorText(read, t);
  };
  /**
   * The warnings of the folder that the dialog is at: its read, else what a 409 said, else (the folder of a share that smb.conf
   * leaves out as unconfirmed) what the view says it never confirmed; none for a disk that is not there.
   */
  const warnings = () => {
    if (!cleanPath(path()) || unplugged()) return [];
    const read = folder.answer()?.warnings ?? [];
    if (read.length > 0) return read;
    const said = named() ?? [];
    if (said.length > 0) return said;
    if (edit && path() === edit.path) {
      const now = props.view().config.shares.find((s) => s.name === edit.name) ?? edit;
      if (now.skipped === "unconfirmed") return now.unconfirmed;
    }
    return [];
  };
  /** The checkbox is needed: the folder has warnings, or the server asked for a confirmation. */
  const needsBox = () => warnings().length > 0 || (demanded() && cleanPath(path()) && !unplugged());
  /** The alert starts with the text of confirm_needed: the server asked, and no warning of it is one the page knows. */
  const unnamed = () => demanded() && !warnings().some((w) => KNOWN_WARNINGS.includes(w));
  const daysError = () =>
    refused().recycleDays ?? (validDays(days()) ? undefined : t("share.recycleDaysHint"));
  const canSend = () =>
    !props.busy() &&
    known() &&
    name().trim() !== "" &&
    cleanPath(path()) &&
    !folder.loading() &&
    (!recycle() || validDays(days())) &&
    !unplugged() &&
    !refusedFolder() &&
    owner()?.state !== "missing" &&
    (!needsBox() || know());

  const send = () => {
    setRefused({});
    const share: ShareRequest = {
      name: name().trim(),
      path: path(),
      comment: comment(),
      // only users of the view: a name that is no Samba user any more is refused, and has no right to keep
      rights: Object.fromEntries(
        users().flatMap((u): [string, Right][] => {
          const r = rights()[u.name];
          return r ? [[u.name, r]] : [];
        }),
      ),
      hidden: hidden(),
      recycle: recycle(),
      recycleDays: recycle() ? Number(days()) : 0,
    };
    const acknowledge = warnings().length > 0 ? { acknowledge: warnings() } : {};
    const request = edit
      ? { kind: "share-edit", name: edit.name, share, ...acknowledge }
      : { kind: "share-add", share, ...acknowledge };
    void change.run(request, () =>
      tSafe(t, edit ? "done.share-edit" : "done.share-add", { name: share.name }),
    );
  };

  /** The id of the text below a field that describes it (its error), when there is one. */
  const describedBy = (field: string, text: string | undefined) =>
    text !== undefined ? `${id}-${field}` : undefined;
  const below = (field: string, text: string | undefined) => (
    <Show when={text}>
      <p id={`${id}-${field}`} class="text-xs text-error">
        {text}
      </p>
    </Show>
  );
  const address = () =>
    `\\\\${serverName(props.view().config.settings.name, session.info()?.hostname)}\\${name().trim() || "…"}`;
  const startBrowse = () => setBrowsing(cleanPath(path()) ? path() : "/");
  /** The text of a warning, the code itself for one the page does not know. */
  const warningText = (code: string) => {
    const key = `share.warning.${code}`;
    const text = t(key);
    return text === key ? code : text;
  };

  // the navigator and the service account stand beside the dialog, not in it: it is inert behind them (Modal)
  return (
    <>
      <Modal
        title={edit ? tSafe(t, "share.editTitle", { name: edit.name }) : t("share.addTitle")}
        wide
        busy={change.busy()}
        onClose={props.onClose}
        actions={
          <ChangeButtons
            change={change}
            disabled={!canSend()}
            label={t(edit ? "share.save" : "share.add")}
            onClose={props.onClose}
            onConfirm={send}
          />
        }
      >
        <Field label={t("share.name")}>
          {(fieldId) => (
            <>
              <input
                id={fieldId}
                type="text"
                class="input input-sm w-full"
                autocomplete="off"
                value={name()}
                disabled={locked()}
                onInput={(e) => {
                  setName(e.currentTarget.value);
                  forget("name");
                }}
                aria-invalid={refused().name !== undefined}
                aria-describedby={describedBy("name", refused().name)}
              />
              {below("name", refused().name)}
            </>
          )}
        </Field>
        <Field label={t("share.path")}>
          {(fieldId) => (
            <>
              <div class="flex gap-2">
                <input
                  id={fieldId}
                  type="text"
                  class="input input-sm w-full"
                  autocomplete="off"
                  spellcheck={false}
                  value={path()}
                  readOnly={perUser}
                  disabled={locked()}
                  onInput={(e) => {
                    setPath(e.currentTarget.value);
                    forget("path");
                  }}
                  aria-invalid={pathError() !== undefined}
                  aria-describedby={describedBy("path", pathError())}
                />
                <Show when={!perUser}>
                  <button type="button" class="btn btn-sm" disabled={locked()} onClick={startBrowse}>
                    {t("share.browse")}
                  </button>
                </Show>
              </div>
              {below("path", pathError())}
            </>
          )}
        </Field>
        <Show when={perUser}>
          <p class="text-xs opacity-70">{t("share.pathPerUser")}</p>
        </Show>
        <OwnerLine
          reading={cleanPath(path()) && folder.loading()}
          owner={owner()}
          perUser={perUser}
          disabled={locked()}
          onAccount={(o) => setAccount({ path: path(), owner: o })}
        />
        <Show when={needsBox()}>
          <div class="alert alert-error text-sm" role="alert">
            <ul class="flex flex-col gap-1">
              <Show when={unnamed()}>
                <li>{t("errors.confirm_needed")}</li>
              </Show>
              <For each={warnings()}>{(code) => <li>{warningText(code)}</li>}</For>
            </ul>
          </div>
          <Check label={t("share.know")} checked={know()} disabled={locked()} onChange={setKnow} />
        </Show>
        <Field label={t("share.comment")}>
          {(fieldId) => (
            <input
              id={fieldId}
              type="text"
              class="input input-sm w-full"
              autocomplete="off"
              value={comment()}
              disabled={locked()}
              onInput={(e) => setComment(e.currentTarget.value)}
            />
          )}
        </Field>
        <div class="flex flex-col gap-1">
          <span class="text-sm">{t("share.rights")}</span>
          <Show when={known()} fallback={<p class="text-sm text-warning">{t("share.usersUnknown")}</p>}>
            <Show when={users().length > 0} fallback={<p class="text-sm opacity-70">{t("share.noUsers")}</p>}>
              <RightsTable
                users={users()}
                value={rights()}
                disabled={locked()}
                usersKnown
                onChange={(v) => {
                  setRights(v);
                  forget("rights");
                }}
              />
            </Show>
            <Show when={!users().some((u) => rights()[u.name])}>
              <p class="text-xs text-warning">{t("share.nobody")}</p>
            </Show>
            {below("rights", refused().rights)}
          </Show>
        </div>
        <Check
          label={tSafe(t, "share.hidden", { address: address() })}
          checked={hidden()}
          disabled={locked()}
          onChange={setHidden}
        />
        <div class="flex flex-wrap items-center gap-2">
          <Check
            label={t("share.recycle")}
            checked={recycle()}
            disabled={locked()}
            onChange={(on) => {
              setRecycle(on);
              forget("recycleDays");
              if (on && !validDays(days())) setDays(String(DEFAULT_DAYS));
            }}
          />
          <Show when={recycle()}>
            <input
              type="number"
              class="input input-sm w-24"
              min={MIN_DAYS}
              max={MAX_DAYS}
              aria-label={t("share.recycleDays")}
              value={days()}
              disabled={locked()}
              onInput={(e) => {
                setDays(e.currentTarget.value);
                forget("recycleDays");
              }}
              aria-invalid={daysError() !== undefined}
              aria-describedby={describedBy("recycleDays", daysError())}
            />
            <span class="text-sm">{t("share.recycleDaysAfter")}</span>
          </Show>
        </div>
        <Show when={recycle()}>{below("recycleDays", daysError())}</Show>
        <Show when={props.busy() && !change.busy()}>
          <p class="text-sm text-warning" role="status">
            {t("dialog.busy")}
          </p>
        </Show>
        <JobStatus step={change.step()} failure={change.failure()} progress={change.running()?.progress} />
      </Modal>
      <Show when={browsing() !== undefined}>
        <NavigatorDialog
          mounts={props.view().mounts}
          start={browsing() ?? ""}
          onClose={() => setBrowsing(undefined)}
          onChoose={(chosen) => {
            setPath(chosen);
            forget("path");
            setBrowsing(undefined);
          }}
        />
      </Show>
      <Show when={account()}>
        {(a) => (
          <ServiceAccountDialog
            path={a().path}
            owner={a().owner}
            onClose={() => setAccount(undefined)}
            onChanged={() => {
              folder.refresh();
              props.onReload();
            }}
            onDone={(message) => {
              setAccount(undefined);
              props.onChanged(message);
            }}
          />
        )}
      </Show>
    </>
  );
}
