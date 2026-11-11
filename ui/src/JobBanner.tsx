import { useI18n } from "@edelweiss/sdk";
import { Match, Switch } from "solid-js";
import type { JobState } from "./api";
import { stepLabel, stepText, tSafe } from "./errors";

/**
 * The job of the view: a running one with its kind, target and step (a dialog may run it, or another tab, or it went
 * on while the page was closed), an interrupted one as a warning with the step it stopped at. A job is interrupted when
 * its status says so (its runner is gone), or when it failed with the code interrupted (a SIGTERM ended the runner,
 * which wrote the end): the two look the same. A job that is done or failed otherwise has nothing to say here: the
 * dialog that ran it has told, and the view does not carry it.
 */
export function JobBanner(props: { job: JobState }) {
  const { t } = useI18n();
  const kind = () => t(`kinds.${props.job.kind}`);
  const interrupted = () =>
    props.job.status === "interrupted" || (props.job.status === "failed" && props.job.code === "interrupted");
  return (
    <Switch>
      <Match when={props.job.status === "running"}>
        <div class="alert alert-info">
          <span class="loading loading-spinner loading-sm" aria-hidden="true" />
          <div class="flex flex-col">
            <span class="font-semibold">
              {tSafe(t, "job.running", { kind: kind(), target: props.job.target })}
            </span>
            <span class="text-sm">{stepText(props.job, t)}</span>
          </div>
        </div>
      </Match>
      <Match when={interrupted()}>
        <div class="alert alert-warning" role="alert">
          {tSafe(t, "job.interrupted", {
            kind: kind(),
            target: props.job.target,
            n: props.job.step + 1,
            m: props.job.steps.length,
            label: stepLabel(t, props.job.steps[props.job.step]),
          })}
        </div>
      </Match>
    </Switch>
  );
}
