import { useI18n } from "@edelweiss/sdk";
import { Show } from "solid-js";
import type { SetupItem } from "./api";
import { tSafe } from "./errors";
import { formatList } from "./format";
import { JobStatus } from "./Modal";
import type { Failure } from "./useJob";

/** The items of the setup that are a service, in the order they start. */
const SERVICES = ["dbus", "avahi", "samba", "discovery", "crond"];

/** The words of the detail of an item ("not_enabled, not_started"). */
const words = (item: SetupItem) => (item.detail ?? "").split(/[\s,]+/).filter(Boolean);

/**
 * The services that are stopped although the setup wants them running, with "Starten", which starts them as a job (the
 * page runs it: it outlives this alert, which goes once the services run). What the setup wants is in its items: with
 * the discovery off, or without a chosen interface that is local, it counts avahi and wsdd as they should be, so a
 * stopped one is no alert then; the discovery then says no_local_interface, which gets a text of its own (Explorer and
 * Finder do not find the server). The step of the start, and its failure until it is dismissed, stand here as well, also
 * once the services run. A readOnly page shows no button.
 */
export function ServiceAlert(props: {
  items: SetupItem[];
  readOnly: boolean;
  /** A job runs, or the start does. */
  busy: boolean;
  step?: string;
  failure?: Failure;
  onStart: () => void;
  onDismiss: () => void;
}) {
  const { t, locale } = useI18n();
  const stopped = () =>
    SERVICES.filter((id) => props.items.some((i) => i.id === id && words(i).includes("not_started")));
  const noLocal = () =>
    props.items.some((i) => i.id === "discovery" && words(i).includes("no_local_interface"));
  const stoppedText = () =>
    tSafe(t, stopped().length === 1 ? "services.stoppedOne" : "services.stopped", {
      services: formatList(
        stopped().map((id) => t(`services.name.${id}`)),
        locale(),
      ),
    });
  return (
    <>
      <Show when={stopped().length > 0 || props.step !== undefined || props.failure !== undefined}>
        <div class="flex flex-col gap-2">
          <Show when={stopped().length > 0}>
            <div class="alert alert-warning flex-col items-start sm:flex-row sm:items-center" role="alert">
              <span>{stoppedText()}</span>
              <Show when={!props.readOnly}>
                <button
                  type="button"
                  class="btn btn-sm"
                  disabled={props.busy}
                  onClick={() => props.onStart()}
                >
                  {t("services.start")}
                </button>
              </Show>
            </div>
          </Show>
          <JobStatus step={props.step} failure={props.failure} />
          <Show when={props.failure}>
            <button type="button" class="btn btn-xs btn-ghost self-start" onClick={() => props.onDismiss()}>
              {t("common.dismiss")}
            </button>
          </Show>
        </div>
      </Show>
      <Show when={noLocal()}>
        <div class="alert alert-info" role="status">
          {t("services.noLocalInterface")}
        </div>
      </Show>
    </>
  );
}
