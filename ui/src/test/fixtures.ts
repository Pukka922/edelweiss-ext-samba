import type {
  BrowseResponse,
  ConnectionView,
  FolderEntry,
  FoldersResponse,
  JobState,
  MountView,
  OwnerView,
  RecycleEntry,
  RecycleResponse,
  SetupItem,
  ShareView,
  SummaryView,
  UserView,
  ViewResponse,
} from "../api";

/** A job: running at its first step unless over says otherwise. */
export const job = (over: Partial<JobState> = {}): JobState => ({
  id: "20261020T120000-ab12",
  kind: "setup",
  target: "samba",
  status: "running",
  steps: ["prepare", "lbu"],
  step: 0,
  started: "2026-10-20T12:00:00Z",
  ...over,
});

/** A gigabyte, as the page counts it (decimal). */
export const GB = 1000 ** 3;

/** The owner ben:users (1000:100), who has an account. */
export const ben = (): OwnerView => ({ uid: 1000, gid: 100, user: "ben", group: "users" });

/** A share of the preview, "Fotos" on a disk of its own, unless over says otherwise. */
export const share = (over: Partial<ShareView> = {}): ShareView => ({
  name: "Fotos",
  path: "/mnt/daten/Fotos",
  comment: "Familienfotos",
  mount: "/mnt/daten",
  rights: { anna: "write", ben: "write", oma: "read" },
  hidden: false,
  recycle: true,
  recycleDays: 30,
  perUser: false,
  folders: [],
  available: true,
  reason: "",
  skipped: "",
  warnings: [],
  unconfirmed: [],
  owner: ben(),
  recycleBin: { files: 340, bytes: 1.2 * GB, partial: false },
  ...over,
});

/**
 * The shares of the preview: Fotos, Filme (hidden, no recycle bin) and Backup-USB, whose disk is not mounted (so the
 * size of its recycle bin is not read).
 */
export const shares = (): ShareView[] => [
  share(),
  share({
    name: "Filme",
    path: "/mnt/daten/Filme",
    comment: "",
    rights: { anna: "write", tv: "read" },
    hidden: true,
    recycle: false,
    recycleDays: 0,
    recycleBin: undefined,
  }),
  share({
    name: "Backup-USB",
    path: "/mnt/usb1/backup",
    comment: "",
    mount: "/mnt/usb1",
    rights: { ben: "write" },
    available: false,
    reason: "unmounted",
    owner: undefined,
    recycleBin: undefined,
  }),
];

/** The Samba users of the preview: anna, oma and tv are pure Samba users, ben is a host user. */
export const users = (): UserView[] => [
  { name: "anna", kind: "samba", disabled: false },
  { name: "ben", kind: "host", disabled: false },
  { name: "oma", kind: "samba", disabled: false },
  { name: "tv", kind: "samba", disabled: false },
];

/** The mounts of the preview: two data disks and the system disk (/), which a share can be on too. */
export const mounts = (): MountView[] => [
  { point: "/mnt/daten", type: "ext4", total: 4000 * GB, free: 1200 * GB, xattr: true, ram: false },
  { point: "/mnt/usb1", type: "exfat", total: 1000 * GB, free: 400 * GB, xattr: false, ram: false },
  { point: "/", type: "ext4", total: 60 * GB, free: 25 * GB, xattr: true, ram: false },
];

const ITEM_IDS = ["config", "samba", "dbus", "avahi", "discovery", "crond", "cleanup_script", "persist"];

/** The items of the setup, all OK but those that open names, with their detail. */
export const items = (open: Record<string, string> = {}): SetupItem[] =>
  ITEM_IDS.map((id) => (open[id] === undefined ? { id, ok: true } : { id, ok: false, detail: open[id] }));

/** The view of a host that is set up, with the shares and the users of the preview, unless over says otherwise. */
export const view = (over: Partial<ViewResponse> = {}): ViewResponse => ({
  setup: { setUp: true, items: items() },
  config: {
    settings: { name: "nas", workgroup: "WORKGROUP", smb2: false, discovery: true, interfaces: ["eth0"] },
    shares: shares(),
  },
  users: users(),
  hostUsers: [],
  mounts: mounts(),
  ifaces: [
    { name: "eth0", bridge: false, discovery: true, addrs: [{ cidr: "192.168.1.10/24", private: true }] },
  ],
  services: { dbus: true, "avahi-daemon": true, samba: true, wsdd: true, crond: true },
  usersKnown: true,
  usersExt: { installed: true, version: "0.2.0" },
  ...over,
});

/** The view of a host that is not set up yet: nothing of the extension is there. foreign: what a Samba before it has. */
export const notSetUp = (foreign?: { shares: string[]; users: string[] }): ViewResponse =>
  view({
    setup: {
      setUp: false,
      items: items({ config: "missing, smb_conf", samba: "not_enabled, not_started", crond: "not_started" }),
      foreign,
    },
    config: {
      settings: { name: "", workgroup: "", smb2: false, discovery: false, interfaces: [] },
      shares: [],
    },
    users: [],
    services: { dbus: false, "avahi-daemon": false, samba: false, wsdd: false, crond: false },
  });

/** The route of GET /browse for dir, as stubApi takes it. */
export const browseRoute = (dir: string) => `GET /browse?dir=${encodeURIComponent(dir)}`;

/** The answer of GET /browse for dir: its folders (none have children unless named so) and its owner (ben). */
export const browse = (
  dir: string,
  names: string[] = [],
  over: Partial<BrowseResponse> = {},
): BrowseResponse => ({
  dir,
  entries: names.map((name) => ({ name, hasChildren: false })),
  truncated: false,
  owner: ben(),
  warnings: [],
  ...over,
});

/** The route of GET /folders for a folder below a share ("" is the share's folder), as stubApi takes it. */
export const foldersRoute = (shareName: string, dir: string) =>
  `GET /folders?share=${encodeURIComponent(shareName)}&dir=${encodeURIComponent(dir)}`;

/**
 * The answer of GET /folders for a folder of Fotos: its folders (a name alone has no children and no rights of its own),
 * and the rights of the folder as the share gives them (no rights of its own, the share's rights, at most the share's),
 * unless over says otherwise.
 */
export const folders = (
  dir: string,
  entries: (string | FolderEntry)[] = [],
  over: Partial<FoldersResponse> = {},
): FoldersResponse => ({
  share: "Fotos",
  dir,
  entries: entries.map((e) => (typeof e === "string" ? { name: e, hasChildren: false, own: false } : e)),
  truncated: false,
  own: false,
  from: "",
  rights: { anna: "write", ben: "write", oma: "read" },
  max: { anna: "write", ben: "write", oma: "read" },
  ...over,
});

/** The route of GET /recycle for a share, as stubApi takes it. */
export const recycleRoute = (shareName: string) => `GET /recycle?share=${encodeURIComponent(shareName)}`;

/** The files of the recycle bin of the preview: two photos deleted on 2 November 2026, a calendar on 23 October. */
export const recycleItems = (): RecycleEntry[] => [
  { path: "2024/Sommer/IMG_2041.jpg", size: 4_100_000, deleted: "2026-11-02T12:00:00Z" },
  { path: "2024/Sommer/IMG_2042.jpg", size: 3_900_000, deleted: "2026-11-02T11:00:00Z" },
  { path: "Kalender.pdf", size: 1_000_000, deleted: "2026-10-23T12:00:00Z" },
];

/**
 * The answer of GET /recycle for Fotos: the files of the preview and the size of the bin (1.2 GB in 340 files); truncated: the
 * list holds only the newest of them.
 */
export const recycleBin = (
  items: RecycleEntry[] = recycleItems(),
  summary: Partial<SummaryView> = {},
  truncated = false,
): RecycleResponse => ({
  share: "Fotos",
  items,
  truncated,
  summary: { files: 340, bytes: 1.2 * GB, partial: false, ...summary },
});

/** The clients of the preview: anna on her laptop since 10:42 today (4 November 2026, local time), tv since 20:15 yesterday. */
export const connections = (): ConnectionView[] => [
  {
    user: "anna",
    machine: "ANNA-LAPTOP",
    address: "192.168.1.23",
    protocol: "SMB3_11",
    share: "Fotos",
    since: new Date(2026, 10, 4, 10, 42).toISOString(),
    files: ["2024/Sommer/IMG_2050.jpg"],
  },
  {
    user: "tv",
    machine: "192.168.1.40",
    address: "192.168.1.40",
    protocol: "SMB3_00",
    share: "Filme",
    since: new Date(2026, 10, 3, 20, 15).toISOString(),
    files: ["Serie/S01E03.mkv"],
  },
];
