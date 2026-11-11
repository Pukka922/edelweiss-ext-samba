import { useI18n } from "@edelweiss/sdk";
import { Show } from "solid-js";
import type { Foreign } from "./api";
import { tSafe } from "./errors";
import { formatList } from "./format";
import { JobStatus } from "./Modal";
import type { Failure } from "./useJob";

/**
 * "Samba einrichten": what the page shows in place of its tabs on a host that the extension has not set up, with the
 * button that starts the setup job (the page runs it: the setup writes the config in the middle, which takes this dialog
 * away). A Samba that is there before is named first, its shares and its users (foreign, which the view has in admin mode
 * only), with what happens to it: it is kept with .edelweiss-bak, and the page starts empty. Before admin mode the dialog
 * says that admin mode shows such a Samba; the page asks for admin mode at the click, and shows what it reveals before it
 * sets up anything. The step of the setup and its failure (until dismissed) stand here; a readOnly page has no button.
 */
export function SetupDialog(props: {
  foreign?: Foreign;
  readOnly: boolean;
  admin: boolean;
  /** A job runs, or the setup does. */
  busy: boolean;
  step?: string;
  failure?: Failure;
  onStart: () => void;
  onDismiss: () => void;
}) {
  const { t, locale } = useI18n();
  /** What the Samba that is there has: the shares of its smb.conf and the users of its passdb, in words. */
  const foreignText = (f: Foreign) => {
    const parts: string[] = [];
    if (f.shares.length > 0) {
      const shares = formatList(
        f.shares.map((name) => tSafe(t, "setup.quoted", { name })),
        locale(),
      );
      parts.push(tSafe(t, f.shares.length === 1 ? "setup.foreignShare" : "setup.foreignShares", { shares }));
    }
    if (f.users.length > 0) {
      const users = f.users.join(", ");
      parts.push(
        f.users.length === 1
          ? tSafe(t, "setup.foreignUser", { users })
          : tSafe(t, "setup.foreignUsers", { n: f.users.length, users }),
      );
    }
    return tSafe(t, "setup.foreign", { what: formatList(parts, locale()) });
  };
  return (
    <section class="card card-border bg-base-100 max-w-2xl">
      <div class="card-body gap-3 p-4">
        <h3 class="font-semibold">{t("setup.title")}</h3>
        <p class="text-sm opacity-70">{t("setup.text")}</p>
        <Show when={props.foreign}>
          {(f) => (
            <div class="alert alert-warning text-sm" role="alert">
              {foreignText(f())}
            </div>
          )}
        </Show>
        <Show when={!props.admin && !props.readOnly}>
          <p class="text-sm opacity-70">{t("setup.adminFirst")}</p>
        </Show>
        <JobStatus step={props.step} failure={props.failure} />
        <Show when={props.failure}>
          <button type="button" class="btn btn-xs btn-ghost self-start" onClick={() => props.onDismiss()}>
            {t("common.dismiss")}
          </button>
        </Show>
        <div class="flex items-center justify-end gap-2">
          <Show when={!props.readOnly} fallback={<p class="text-sm opacity-70">{t("setup.adminOnly")}</p>}>
            <button
              type="button"
              class="btn btn-primary"
              disabled={props.busy}
              onClick={() => props.onStart()}
            >
              {t("setup.button")}
            </button>
          </Show>
        </div>
      </div>
    </section>
  );
}
