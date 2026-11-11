import type { Accessor } from "solid-js";
import type { ShareView, UserView, ViewResponse } from "./api";
import { AddHostUserDialog } from "./dialogs/AddHostUserDialog";
import { AddSambaUserDialog } from "./dialogs/AddSambaUserDialog";
import { DeleteShareDialog } from "./dialogs/DeleteShareDialog";
import { DeleteUserDialog } from "./dialogs/DeleteUserDialog";
import { FolderRightsDialog } from "./dialogs/FolderRightsDialog";
import { PasswordDialog } from "./dialogs/PasswordDialog";
import { RecycleDialog } from "./dialogs/RecycleDialog";
import { ShareDialog } from "./dialogs/ShareDialog";
import { SyncPasswordDialog } from "./dialogs/SyncPasswordDialog";
import type { ShareAction } from "./ShareRow";

/**
 * The dialog the page has open: a new share, the edit, the delete or the recycle bin of a share as the view had it at the click,
 * the rights of a folder below a share ("" is the share's own), a new Samba user or host user, or the password or the delete of
 * a user as the view had it at the click.
 */
export type Open =
  | { kind: "add" }
  | { kind: ShareAction; share: ShareView }
  | { kind: "folder"; share: ShareView; folder: string }
  | { kind: "user-add" | "host-add" }
  | { kind: "password" | "user-delete"; user: UserView };

/**
 * The dialog of the page that open names, with what the page gives every dialog: the view (the page has one while a dialog is
 * open), whether a job runs in it (busy), and the page's answers to the end of a change. onDone closes the dialog, says so and
 * reads the view again; onChanged says so and reads it again, for a change that leaves the dialog open; onReload reads it
 * again. A folder-rights job that ended is told to the folder trees (onFolderEnded), and so is a disk that cannot keep folder
 * rights (onNoAcl).
 */
export function PageDialogs(props: {
  open: Open;
  view: Accessor<ViewResponse>;
  busy: Accessor<boolean>;
  onClose: () => void;
  onDone: (message: string) => void;
  onChanged: (message: string) => void;
  onReload: () => void;
  onFolderEnded: (share: string, folder: string) => void;
  onNoAcl: (share: string, reason: string) => void;
}) {
  const d = props.open;
  const common = {
    view: props.view,
    busy: props.busy,
    onClose: props.onClose,
    onDone: props.onDone,
    onReload: props.onReload,
  };
  switch (d.kind) {
    case "delete":
      return <DeleteShareDialog share={d.share} {...common} />;
    case "recycle":
      return (
        <RecycleDialog
          share={d.share}
          view={props.view}
          busy={props.busy}
          onClose={props.onClose}
          onChanged={props.onChanged}
        />
      );
    case "folder":
      return (
        <FolderRightsDialog
          share={d.share}
          folder={d.folder}
          {...common}
          onEnded={() => props.onFolderEnded(d.share.name, d.folder)}
          onNoAcl={(reason) => props.onNoAcl(d.share.name, reason)}
        />
      );
    case "add":
    case "edit":
      return (
        <ShareDialog
          share={d.kind === "edit" ? d.share : undefined}
          {...common}
          onChanged={props.onChanged}
        />
      );
    case "user-add":
      return (
        <AddSambaUserDialog
          busy={props.busy}
          onClose={props.onClose}
          onDone={props.onDone}
          onReload={props.onReload}
        />
      );
    case "host-add":
      return <AddHostUserDialog {...common} />;
    case "password":
      return d.user.kind === "host" ? (
        <SyncPasswordDialog user={d.user} {...common} />
      ) : (
        <PasswordDialog user={d.user} {...common} />
      );
    case "user-delete":
      return <DeleteUserDialog user={d.user} {...common} />;
  }
}
