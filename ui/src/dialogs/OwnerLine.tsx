import { useI18n } from "@edelweiss/sdk";
import { Show } from "solid-js";
import type { OwnerView } from "../api";
import { tSafe } from "../errors";

/** The owner of a folder, and what it means for a share of it. */
export interface Owner {
  o: OwnerView;
  state: "ok" | "missing";
}

/**
 * What the owner of a folder means for a share, by the server's rule for a share (checkShare): one without the name of its
 * user and of its group has no account (no_owner, the same for a share with folder rights). root has an account: a folder
 * of root is a share like any other, with the warning root_owned, which the dialog words. Undefined while there is no owner.
 */
export function ownerOf(o: OwnerView | undefined): Owner | undefined {
  if (!o) return undefined;
  if (o.user === "" || o.group === "") return { o, state: "missing" };
  return { o, state: "ok" };
}

/**
 * The owner of the folder, whom Samba accesses the files as (a share with folder rights: each user, in the group of the
 * folder): while it is read, and an owner without an account, which offers "Dienstkonto anlegen". Nothing for a folder
 * that is not read.
 */
export function OwnerLine(props: {
  reading: boolean;
  owner?: Owner;
  perUser: boolean;
  disabled: boolean;
  onAccount: (owner: OwnerView) => void;
}) {
  const { t } = useI18n();
  return (
    <Show
      when={props.owner}
      fallback={
        <Show when={props.reading}>
          <p class="text-xs opacity-70">{t("share.ownerReading")}</p>
        </Show>
      }
    >
      {(own) => (
        <div class="rounded-box bg-base-200 p-2 text-sm">
          <Show when={own().state === "ok"}>
            {props.perUser
              ? tSafe(t, "share.ownerPerUser", { group: own().o.group })
              : tSafe(t, "share.owner", { owner: `${own().o.user}:${own().o.group}` })}
          </Show>
          <Show when={own().state === "missing"}>
            <div class="flex flex-col items-start gap-2">
              <span class="text-warning">
                {tSafe(t, "share.ownerMissing", { uid: own().o.uid, gid: own().o.gid })}
              </span>
              <button
                type="button"
                class="btn btn-sm"
                disabled={props.disabled}
                onClick={() => props.onAccount(own().o)}
              >
                {t("share.serviceAccount")}
              </button>
            </div>
          </Show>
        </div>
      )}
    </Show>
  );
}
