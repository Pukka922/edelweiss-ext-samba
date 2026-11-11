import type { JobRequest } from "../api";
import { Buttons } from "../Buttons";
import { JobStatus, Modal } from "../Modal";
import { useJobRun } from "../useJob";

/**
 * A change that is confirmed before it is sent, as a job: the title, what it does (text) and the button (label, red for
 * a danger). Confirming sends the request and shows the step that runs, then tells the page the text of the end (done);
 * a job that fails or a request that is refused shows why in the dialog, with the output of the failed command, and the
 * button tries again. Cancel and Escape close it unless the job runs.
 */
export function JobConfirmDialog(props: {
  title: string;
  text: string;
  label: string;
  danger?: boolean;
  request: JobRequest;
  done: string;
  onClose: () => void;
  onDone: (message: string) => void;
}) {
  const job = useJobRun((message) => props.onDone(message));
  return (
    <Modal
      title={props.title}
      busy={job.busy()}
      onClose={props.onClose}
      actions={
        <Buttons
          busy={job.busy()}
          danger={props.danger}
          label={props.label}
          onClose={props.onClose}
          onConfirm={() => void job.run(props.request, () => props.done)}
        />
      }
    >
      <p>{props.text}</p>
      <JobStatus step={job.step()} failure={job.failure()} />
    </Modal>
  );
}
