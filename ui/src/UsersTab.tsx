import { useI18n } from "@edelweiss/sdk";
import { IconKey, IconX } from "@tabler/icons-solidjs";
import { For, Show } from "solid-js";
import type { ShareView, UsersExtView, UserView } from "./api";
import { versionBelow } from "./format";
import { IconButton } from "./IconButton";
import { keyed } from "./keys";

/** The first version of edelweiss-ext-users that takes Samba along when a user is deleted or a password is changed. */
const USERS_EXT_MIN: [number, number, number] = [0, 2, 0];

/** The shares that name the user, as the preview shows them: "Fotos ✎, Filme 👁". */
const sharesOf = (shares: ShareView[], user: string, none: string) => {
  const named = shares.flatMap((s) => {
    const right = s.rights[user] ?? "";
    return right === "" ? [] : [`${s.name} ${right === "write" ? "✎" : "👁"}`];
  });
  return named.length > 0 ? named.join(", ") : none;
};

/**
 * The users tab: the table of the Samba users with their kind ("nur Samba", "Host-Benutzer"), their shares and the actions
 * (change the password, delete, or for a host user remove the Samba access), and the two buttons that add one. The rows are
 * by the name of the user, so that a new view every few seconds keeps the rows, their buttons and the focus. Where the
 * view cannot read the Samba users (usersKnown: not in admin mode) a hint takes their place. A users extension that is
 * installed and older than 0.2.0 (or whose version cannot be read) gets a warning: it does not know pure Samba users and
 * does not protect them. A host user has one password, so its key button is "Passwort angleichen" (it asks for the Linux
 * password), not "Passwort ändern". An account of the extension whose Samba entry is gone (noEntry) says "ohne
 * Samba-Eintrag" and can only be deleted. With admin mode on and the Samba users not read, a hint says so and the add
 * buttons are disabled. busy: a job runs, and nothing that changes something can be started; a readOnly page has no buttons.
 */
export function UsersTab(props: {
  users: UserView[];
  shares: ShareView[];
  usersKnown: boolean;
  /** Admin mode is on: then users that are not known could not be read, and are not just hidden. */
  admin: boolean;
  usersExt: UsersExtView;
  readOnly: boolean;
  busy: boolean;
  onAddSamba: () => void;
  onAddHost: () => void;
  onPassword: (user: UserView) => void;
  onDelete: (user: UserView) => void;
}) {
  const { t } = useI18n();
  const rows = keyed(
    () => props.users,
    (u) => u.name,
  );
  /** Admin mode is on and the Samba users still could not be read (pdbedit failed): nothing can be added. */
  const unreadable = () => props.admin && !props.usersKnown;
  const oldExt = () => props.usersExt.installed && versionBelow(props.usersExt.version, USERS_EXT_MIN);
  return (
    <div class="flex flex-col gap-3">
      <Show when={oldExt()}>
        <div class="alert alert-warning" role="alert">
          {t("users.extOld")}
        </div>
      </Show>
      <section class="card card-border bg-base-100">
        <div class="card-body gap-3 p-4">
          <Show when={!props.readOnly}>
            <div class="flex flex-wrap justify-end gap-2">
              <button
                type="button"
                class="btn btn-sm"
                disabled={props.busy || unreadable()}
                title={unreadable() ? t("users.unreadable") : undefined}
                onClick={() => props.onAddHost()}
              >
                {t("users.addHost")}
              </button>
              <button
                type="button"
                class="btn btn-sm btn-primary"
                disabled={props.busy || unreadable()}
                title={unreadable() ? t("users.unreadable") : undefined}
                onClick={() => props.onAddSamba()}
              >
                {t("users.addSamba")}
              </button>
            </div>
          </Show>
          <Show
            when={props.usersKnown}
            fallback={
              <p class="text-sm opacity-70">{t(unreadable() ? "users.unreadable" : "users.unknown")}</p>
            }
          >
            <Show
              when={rows.keys().length > 0}
              fallback={
                <p class="text-sm opacity-70">{t(props.readOnly ? "users.emptyReadOnly" : "users.empty")}</p>
              }
            >
              <div class="overflow-x-auto">
                <table class="table table-sm">
                  <thead>
                    <tr>
                      <th>{t("users.columns.name")}</th>
                      <th>{t("users.columns.kind")}</th>
                      <th>{t("users.columns.shares")}</th>
                      <th>
                        <span class="sr-only">{t("actions.label")}</span>
                      </th>
                    </tr>
                  </thead>
                  <tbody>
                    <For each={rows.keys()}>
                      {(name) => {
                        const u = rows.track(name);
                        const host = () => u().kind === "host";
                        return (
                          <tr>
                            <td>
                              <span class="font-semibold">{name}</span>
                            </td>
                            <td>
                              <span class="flex flex-wrap items-center gap-1">
                                <span
                                  class="badge badge-sm whitespace-nowrap"
                                  classList={{ "badge-secondary badge-soft": !host() }}
                                >
                                  {t(host() ? "rights.host" : "rights.samba")}
                                </span>
                                <Show when={u().disabled}>
                                  <span class="badge badge-sm badge-warning badge-soft">
                                    {t("rights.disabled")}
                                  </span>
                                </Show>
                                <Show when={u().noEntry}>
                                  <span class="badge badge-sm badge-warning badge-soft whitespace-nowrap">
                                    {t("users.noEntry")}
                                  </span>
                                </Show>
                                <span class="text-xs opacity-70">
                                  {t(host() ? "users.hintHost" : "users.hintSamba")}
                                </span>
                              </span>
                            </td>
                            <td>{sharesOf(props.shares, name, t("users.noShares"))}</td>
                            <td class="w-px whitespace-nowrap text-right">
                              <Show when={!props.readOnly}>
                                <div class="flex items-center justify-end">
                                  <Show when={!u().noEntry}>
                                    <IconButton
                                      icon={IconKey}
                                      label={`${t(host() ? "users.sync" : "users.password")} ${name}`}
                                      tooltip={t(host() ? "users.sync" : "users.password")}
                                      disabled={props.busy}
                                      onClick={() => props.onPassword(u())}
                                    />
                                  </Show>
                                  <IconButton
                                    icon={IconX}
                                    label={`${t(host() ? "users.drop" : "users.delete")} ${name}`}
                                    tooltip={t(host() ? "users.drop" : "users.delete")}
                                    danger
                                    disabled={props.busy}
                                    onClick={() => props.onDelete(u())}
                                  />
                                </div>
                              </Show>
                            </td>
                          </tr>
                        );
                      }}
                    </For>
                  </tbody>
                </table>
              </div>
            </Show>
          </Show>
        </div>
      </section>
    </div>
  );
}
