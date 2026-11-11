import { useI18n } from "@edelweiss/sdk";
import { createUniqueId, For, Show } from "solid-js";
import type { Right, UserView } from "../api";
import { tSafe } from "../errors";
import { keyed } from "../keys";

/** The rights in their order: each one gives what the one before it gives. */
const RIGHTS: Right[] = ["", "read", "write"];

const rank = (r: Right | undefined) => RIGHTS.indexOf(r ?? "");

/** The words of a right. */
const LABELS: Record<Right, string> = { "": "rights.none", read: "rights.read", write: "rights.write" };

/** The mark of a right in a list: ✎ read and write, 👁 read only. */
const mark = (r: Right) => (r === "write" ? "✎" : "👁");

/** The right that max caps user at: write without max, none for a user that max does not name. */
const capOf = (user: string, max: Record<string, Right> | undefined): Right =>
  max ? (max[user] ?? "") : "write";

/** A right at most cap. */
const clamp = (r: Right, cap: Right) => (rank(r) > rank(cap) ? cap : r);

/** The users that can get a right: those with a Samba entry (an account of the extension without one has none). */
const withEntry = (users: UserView[]) => users.filter((u) => !u.noEntry);

/**
 * The rights as RightsTable shows (and tells) them: the users with a Samba entry and a right in max (all of them without
 * max), each at most that right, without one whose right is none. A dialog sends this, so that what it sends is what it
 * shows: a name without a Samba entry is never sent.
 */
export function shownRights(
  users: UserView[],
  value: Record<string, Right>,
  max?: Record<string, Right>,
): Record<string, Right> {
  const out: Record<string, Right> = {};
  for (const u of withEntry(users)) {
    const right = clamp(value[u.name] ?? "", capOf(u.name, max));
    if (right !== "") out[u.name] = right;
  }
  return out;
}

/**
 * "Wer darf was": a row for each Samba user with "kein Zugriff | lesen | lesen + schreiben", the right of value checked.
 * onChange is told the rights as the table shows them then: the users it lists, without one whose right is none. Without max
 * a row names the kind of the user ("nur Samba", "Host-Benutzer"). With max (the folder rights, which only narrow) each row
 * is capped at the right of max: a right above it is not offered, a value above it shows (and is told) as the cap, a user
 * without a right in max is not listed (nor told), and the row names the right it is capped at and where it comes from: the
 * share ("Freigabe: ✎"), or with maxAbove the folder above ("Ordner darüber: ✎"). The rows are by the user's name, so that a
 * new view with the same users (the page reads it every few seconds) keeps them and the focus.
 *
 * With usersKnown (the page could read the Samba users), a name of value with a right and no Samba entry (deleted in
 * edelweiss-ext-users, or an account of the extension whose entry is gone) has a row of its own: "gelöscht", with no right to
 * give; it is not told, so a save drops it. On a narrow screen the rights of a row go below its name.
 */
export function RightsTable(props: {
  users: UserView[];
  value: Record<string, Right>;
  max?: Record<string, Right>;
  /** max is what the folder above gives, not the share. */
  maxAbove?: boolean;
  disabled?: boolean;
  /** The Samba users could be read: a name of value without an entry is one that is deleted. */
  usersKnown?: boolean;
  onChange: (value: Record<string, Right>) => void;
}) {
  const { t } = useI18n();
  const group = createUniqueId();
  const cap = (user: string) => capOf(user, props.max);
  const rows = keyed(
    () => withEntry(props.users).filter((u) => rank(cap(u.name)) > 0),
    (u) => u.name,
  );
  const deleted = () => {
    if (!props.usersKnown) return [];
    const entries = new Set(withEntry(props.users).map((u) => u.name));
    return Object.entries(props.value)
      .filter(([name, r]) => r !== "" && !entries.has(name))
      .map(([name]) => name)
      .sort((a, b) => a.localeCompare(b));
  };
  const shown = (user: string) => clamp(props.value[user] ?? "", cap(user));
  const set = (user: string, r: Right) =>
    props.onChange(shownRights(props.users, { ...props.value, [user]: r }, props.max));
  return (
    <div class="overflow-x-auto">
      <table class="table table-sm">
        <tbody>
          <For each={rows.keys()}>
            {(name) => {
              const u = rows.track(name);
              return (
                <tr class="flex flex-wrap items-center gap-x-2 gap-y-1 sm:table-row">
                  <td class="font-semibold">{name}</td>
                  <td>
                    <Show
                      when={props.max}
                      fallback={
                        <span class="flex flex-wrap gap-1">
                          <span
                            class="badge badge-sm whitespace-nowrap"
                            classList={{ "badge-secondary badge-soft": u().kind === "samba" }}
                          >
                            {t(u().kind === "samba" ? "rights.samba" : "rights.host")}
                          </span>
                          <Show when={u().disabled}>
                            <span class="badge badge-sm badge-warning badge-soft">
                              {t("rights.disabled")}
                            </span>
                          </Show>
                        </span>
                      }
                    >
                      <span class="text-sm opacity-70">
                        {tSafe(t, props.maxAbove ? "rights.maxAbove" : "rights.max", {
                          mark: mark(cap(name)),
                        })}
                      </span>
                    </Show>
                  </td>
                  <td class="basis-full sm:basis-auto">
                    <div class="join" role="radiogroup" aria-label={name}>
                      <For each={RIGHTS.filter((r) => rank(r) <= rank(cap(name)))}>
                        {(r) => (
                          <input
                            type="radio"
                            class="join-item btn btn-xs"
                            name={`${group}-${name}`}
                            aria-label={t(LABELS[r])}
                            checked={shown(name) === r}
                            disabled={props.disabled}
                            onChange={() => set(name, r)}
                          />
                        )}
                      </For>
                    </div>
                  </td>
                </tr>
              );
            }}
          </For>
          <For each={deleted()}>
            {(name) => (
              <tr class="flex flex-wrap items-center gap-x-2 gap-y-1 sm:table-row">
                <td class="font-semibold opacity-70">{name}</td>
                <td>
                  <span class="badge badge-sm badge-ghost whitespace-nowrap">{t("rights.deleted")}</span>
                </td>
                <td class="basis-full text-sm opacity-70 sm:basis-auto">{t("rights.deletedNote")}</td>
              </tr>
            )}
          </For>
        </tbody>
      </table>
    </div>
  );
}
