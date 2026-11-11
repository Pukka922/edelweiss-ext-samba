/**
 * The shapes of the extension's API, as internal/api answers them (view.go, navigator.go, accounts.go, jobs.go, recycle.go,
 * connections.go) and as the json tags of internal/jobs, internal/setup and internal/actions name them. Every list is a
 * list, never null.
 */

export type JobStatus = "running" | "done" | "failed" | "interrupted";

/** How far the running step of a job is (jobs.Progress): the label of the step and the percent its command reported. */
export interface JobProgressState {
  label: string;
  /** 0 to 100. */
  percent: number;
}

/** A job of the extension (jobs.State). */
export interface JobState {
  id: string;
  kind: string;
  /** What the job works on: the name of a share, of a user, "samba" for the setup. */
  target: string;
  /** What the job works on while it runs (share:<name>, config): the server refuses what uses the same. */
  uses?: string[];
  status: JobStatus;
  /** The labels of the steps: steps.<label> is their text. */
  steps: string[];
  /** The running or failed step, from 0. */
  step: number;
  code?: string;
  error?: string;
  /** The end of the output of the failed command. */
  output?: string;
  started: string;
  finished?: string;
  /** While a step waits for the user: what POST /jobs/<id>/confirm needs, and when the step gives up. */
  nonce?: string;
  deadline?: string;
  /** While a step runs that reports how far it is. */
  progress?: JobProgressState;
  /** While a step runs that a cancel stops: POST /jobs/<id>/cancel takes effect. */
  cancellable?: boolean;
}

/** The body of POST /jobs: the kind of the change and what it needs (actions.Request). */
export interface JobRequest {
  kind: string;
  [field: string]: unknown;
}

/** What a user may do in a share or a folder (samba.Right): nothing, read, or read and write. */
export type Right = "" | "read" | "write";

/**
 * One thing the setup looks at (setup.Item). id: config, samba, dbus, avahi, discovery, crond, cleanup_script, persist.
 * detail: what is open, as words separated by ", " (not_installed, not_enabled, not_started, daemon_list, interfaces,
 * no_local_interface, missing, unreadable, smb_conf, script, lbu_include).
 */
export interface SetupItem {
  id: string;
  ok: boolean;
  detail?: string;
}

/** The shares and the users of a Samba that was there before the setup (setup.Foreign). */
export interface Foreign {
  shares: string[];
  users: string[];
}

/** What the setup found (setup.Status); foreign only on a host that is not set up, in admin mode. */
export interface SetupStatus {
  setUp: boolean;
  items: SetupItem[];
  foreign?: Foreign;
}

/** The server settings (SettingsView). */
export interface SettingsView {
  name: string;
  workgroup: string;
  smb2: boolean;
  discovery: boolean;
  interfaces: string[];
}

/** The owner and the group of a folder (OwnerView); the names are "" where no account has the ID. */
export interface OwnerView {
  uid: number;
  gid: number;
  user: string;
  group: string;
}

/** The number and size of the files in a recycle bin (SummaryView); partial: more than were counted. */
export interface SummaryView {
  files: number;
  bytes: number;
  partial: boolean;
}

/** The warnings of a folder (paths.Warnings): the lists carry strings, as the server may know more than the page. */
export type WarningCode = "system" | "root_owned" | "holds_mount" | "ram";

/** Why the folder of a share cannot be served now (paths.Available). */
export type ShareReason = "" | "unmounted" | "missing" | "link" | "no_owner" | "kernel";

/**
 * A share of the config and its state (ShareView). skipped: why smb.conf leaves it out (samba.Skip*: no_users,
 * no_owner, bad_name, case_clash, bad_path), "" for a share that smbd serves. owner and recycleBin only for
 * a share that is available; recycleBin (the size of the bin) only with the recycle bin (recycle) on.
 */
export interface ShareView {
  name: string;
  path: string;
  comment: string;
  mount: string;
  rights: Record<string, Right>;
  hidden: boolean;
  recycle: boolean;
  recycleDays: number;
  perUser: boolean;
  folders: string[];
  available: boolean;
  reason: ShareReason;
  skipped: string;
  /** The warnings of its folder as it is now (WarningCode); root_owned only where the folder is read. */
  warnings: string[];
  /** With skipped "unconfirmed": the warnings (system, root_owned) of its folder that it never confirmed; [] otherwise. */
  unconfirmed: string[];
  owner?: OwnerView;
  recycleBin?: SummaryView;
}

/** The config of the extension (ConfigView): empty on a host that is not set up. */
export interface ConfigView {
  settings: SettingsView;
  shares: ShareView[];
}

/**
 * A Samba user (UserView): kind samba for a pure Samba user of the extension, host for a host user. noEntry: an account of
 * the extension whose Samba entry is gone (its removal failed halfway): it can only be deleted, and gets no rights.
 */
export interface UserView {
  name: string;
  kind: "samba" | "host";
  disabled: boolean;
  noEntry?: boolean;
}

/** A host user without Samba access (HostUserView); not selectable, with the reason name, for a name Samba does not take. */
export interface HostUserView {
  name: string;
  uid: number;
  selectable: boolean;
  reason?: string;
}

/**
 * A mount a share can be on (MountView), "/" included: xattr says that it keeps what the folder rights need, ram that it
 * is in RAM (tmpfs, ramfs, zram).
 */
export interface MountView {
  point: string;
  type: string;
  total: number;
  free: number;
  xattr: boolean;
  ram: boolean;
}

/** An address of an interface with its prefix (AddrView). */
export interface AddrView {
  cidr: string;
  private: boolean;
}

/**
 * A network interface (IfaceView): bridge for a bridge of Docker or libvirt, which Samba never binds. discovery: Explorer and
 * Finder would find the server on it if it were chosen (setup.LocalInterfaces); false for a public IPv4 beside the private
 * one, and for IPv6 alone.
 */
export interface IfaceView {
  name: string;
  bridge: boolean;
  discovery: boolean;
  addrs: AddrView[];
}

/** Whether edelweiss-ext-users is installed, and its version (UsersExtView). */
export interface UsersExtView {
  installed: boolean;
  version: string;
}

/**
 * The answer of GET /view. usersKnown says whether the Samba users could be read (never by the process of a user without
 * admin mode): without them users and hostUsers are empty and say nothing. services: whether each service is started
 * (dbus, avahi-daemon, samba, wsdd, crond). job: the newest job while it runs or is interrupted.
 */
export interface ViewResponse {
  setup: SetupStatus;
  config: ConfigView;
  users: UserView[];
  hostUsers: HostUserView[];
  mounts: MountView[];
  ifaces: IfaceView[];
  services: Record<string, boolean>;
  usersKnown: boolean;
  usersExt: UsersExtView;
  job?: JobState;
}

/** A folder in the navigator (EntryView); hasChildren: it holds a folder. */
export interface EntryView {
  name: string;
  hasChildren: boolean;
}

/**
 * The answer of GET /browse?dir= (BrowseResponse): the folders in dir, the owner of dir, its warnings, and the share whose folder dir is
 * or is in (inShare): "Neuer Ordner" is refused there.
 */
export interface BrowseResponse {
  dir: string;
  entries: EntryView[];
  truncated: boolean;
  owner: OwnerView;
  inShare?: string;
  /** The warnings for a share of dir (WarningCode), in the order of the server: [] for none. */
  warnings: string[];
}

/** The answer of POST /browse/mkdir {dir, name}: the path of the new folder. */
export interface MkdirResponse {
  path: string;
}

/** A folder below a share (FolderEntry); own: it has rights of its own. */
export interface FolderEntry {
  name: string;
  hasChildren: boolean;
  own: boolean;
}

/** The rights of a folder as its ACL gives them (DiskRights). */
export interface DiskRights {
  rights: Record<string, Right>;
  mixed: boolean;
  partial: boolean;
}

/**
 * The answer of GET /folders?share=&dir= (FoldersResponse). own: dir has rights of its own; from: the folder whose
 * rights dir has ("" for the share's); rights: what the config gives in dir; max: what it may have at most; disk: what
 * the folder's ACL says, for a share that runs per user.
 */
export interface FoldersResponse {
  share: string;
  dir: string;
  entries: FolderEntry[];
  truncated: boolean;
  own: boolean;
  from: string;
  rights: Record<string, Right>;
  max: Record<string, Right>;
  disk?: DiskRights;
}

/** The answer of an account route (AccountAnswer): the job that follows, and the account made for a folder. */
export interface AccountAnswer {
  job?: JobState;
  /** Why the job of a delete that worked could not be started: the name stays in the shares until it is removed. */
  jobError?: string;
  user?: string;
  group?: string;
}

/** A file in a recycle bin (RecycleEntry): its path below the share as before the deletion. */
export interface RecycleEntry {
  path: string;
  size: number;
  deleted: string;
}

/**
 * The answer of GET /recycle?share= (RecycleResponse): the newest files of the bin, newest first; truncated: there are more
 * files than the list holds. summary.partial: the count did not see the whole bin.
 */
export interface RecycleResponse {
  share: string;
  items: RecycleEntry[];
  truncated: boolean;
  summary: SummaryView;
}

/** A client connected to a share (ConnectionView), with the files it holds open in it. */
export interface ConnectionView {
  user: string;
  machine: string;
  address: string;
  protocol: string;
  share: string;
  since: string;
  files: string[];
}

/** A share as share-add and share-edit take it (samba.Share without what the server records itself). */
export interface ShareRequest {
  name: string;
  path: string;
  comment: string;
  rights: Record<string, Right>;
  hidden: boolean;
  recycle: boolean;
  recycleDays: number;
}
