# edelweiss-ext-samba

> **Note:** This project is vibe coded, for lack of time, and made only for my personal use: to share the data disks of
> my Raspberry Pi and my x86 box with Windows, macOS and phones. Anyone is welcome to use it, but at their own risk.

Samba shares set up like on a NAS, as an extension of [edelweiss](https://github.com/Pukka922/edelweiss), the web
administration interface for Alpine Linux. Its page **Shares** ("Freigaben", menu section System) shares folders of the
data disks over the network without SSH, for Windows, macOS and phones. Texts are in English and German.

It is one static binary with a Go backend (`main.go`, `routes.go`, `internal/`) and a SolidJS interface (`ui/`), built
with the edelweiss SDKs.
[Developing edelweiss extensions](https://github.com/Pukka922/edelweiss/blob/main/docs/extension-development.md)
explains its parts. Built on the infrastructure of
[edelweiss-ext-kvm](https://github.com/Pukka922/edelweiss-ext-kvm) and the account packages of
[edelweiss-ext-users](https://github.com/Pukka922/edelweiss-ext-users).

The tests run the real Samba of Alpine in throwaway containers (see Tests). What only hardware shows (a Windows PC and a
Mac that find the host, phones, a Raspberry Pi in diskless mode after a reboot, an unplugged USB disk) is the checklist
at the end of Tests, which is for you to run.

## What it does

- **Shares.** A share is a folder of a mounted data disk with a name in the network, an optional description, and a table
  of rights: for each Samba user "no access", "read" or "read and write". A share without rights for a user is hidden
  from that user; the switch "hidden" hides it from everyone (it is reached by `\\host\name` only). Deleting a share
  removes the share and nothing else: the folder and its files stay.
- **Users.** One list of "Samba users" with two kinds: host users with Samba access (an existing user with a UID from
  1000, whose Linux password you give once; Samba gets the same password), and pure Samba users that the extension
  creates (no home directory, shell `/sbin/nologin`, a locked Linux password, so no SSH, no console and no edelweiss
  sign-in; the Samba password is their only one).
- **An existing folder is shared as it is.** Samba works as the owner and the group of the shared folder (`force user`,
  `force group`), and the extension never changes the owner, the mode or the content of the files in it. New files are
  0644 and new folders 0755 (`map archive = no`: the DOS attributes stay in an extended attribute, not in the mode). A
  folder of root is refused (nothing is shared as root); a folder whose owner or group has no account on this host (a
  disk from another machine) can be shared after you confirm a service account `smb-<uid>` with exactly that ID, which
  has no login. Its group is the group of the folder's GID, or a new `smb-<gid>`; the IDs lie in 1000 to 65532, so new
  users of the host do not get them afterwards. A NAS disk whose files belong to the group `users` (GID 100, as on
  Synology and QNAP) gets an account in `users`; any other group of the system is refused, and the dialog names it. "New
  folder" in the navigator makes a folder with the owner and group of its parent, or, below a folder of root, with the
  account `edelweiss-share`. "New folder" is not offered inside the folder of a share: a share of it could never be
  made.
- **System folders are never shared.** The navigator offers the mounted data disks only, and on them no folder of the
  system: `/usr`, `/var/lib`, `/var/log`, `/var/cache`, `/var/spool`, `/var/tmp`, `/var/run`, `/var/lock` and
  `/var/empty` (with `/proc`, `/sys`, `/dev`, `/run`, `/tmp`, `/etc` and `/boot`) are not listed, not opened, and refused
  as the folder of a share or for "New folder", also when `/var` is a disk of its own, as in Alpine's data mode. `/var`
  itself is offered there, but only its other folders can be shared.
- **Folder rights.** A share can be opened as a tree of folders; each folder gets rights of its own (see Folder rights).
- **Recycle bin.** A file deleted over the network lands in `#recycle` of the share, with its folder path, for a number
  of days (30 by default). The admin sees the bin, restores single files and empties it (see Recycle bin).
- **Connections.** Who is connected from which device, with which protocol, to which share, since when, and which files
  are open (admin mode).
- **Settings.** The name in the network, the workgroup, the switch "allow older devices (SMB2)", the display in the
  network (Windows, Mac) and the interfaces Samba answers on.
- **Reachability.** Local networks only (see Reachability).
- **Diskless.** Samba's databases and the extension's changes survive a reboot of a diskless or data install (see
  Diskless and data mode).
- **Windows and Mac find the host.** WS-Discovery (`wsdd`) for the Explorer's "Network", and Bonjour (`avahi`) for the
  sidebar of the Finder. The Mac adjustments (`vfs_fruit` with `streams_xattr`, so no `._` files) are on for shares on disks with extended attributes; the conversion of special characters in file names (`catia`) is on for every share.

Protocol: SMB3 only (`server min protocol = SMB3`). With the switch "allow older devices" it is SMB2 (`SMB2_02`). SMB1
never. There is no guest access (a device that cannot sign in gets a Samba user of its own with "read"), no WebDAV,
NFS, FTP or DLNA, no Time Machine, no `[homes]`, no quotas and no Active Directory.

### The page

Four tabs: Shares, Users, Connections, Settings. The head shows whether Samba runs and the name in the network
(`\\name`). Every signed-in user sees the page; changes, "New folder" in the navigator, the recycle bin and the
connections need admin mode (the page asks for it when you click). Before Samba is set up the page shows the dialog
"Samba einrichten" instead of the tabs; afterwards it only warns when one of the services is stopped, with "Starten".

## Requirements

- edelweiss 0.2.0 or newer.
- Alpine Linux 3.22 or newer.
- The packages `samba` (the file server and its tools), `avahi` with `dbus` (the Mac finds the host) and `wsdd` (Windows
  finds it). edelweiss installs the extension only when they are there; otherwise it names the missing ones in an
  `apk add` line to run as root, with none of them installed:

  ```sh
  apk add samba avahi dbus wsdd
  ```

  On a diskless or data install, run `lbu commit` afterwards.
- [edelweiss-ext-users](https://github.com/Pukka922/edelweiss-ext-users) 0.2.0 or newer is recommended (see Living with
  edelweiss-ext-users 0.2.0). The page Users warns when an older one is installed.

## Setup

The first time the page opens, "Samba einrichten" runs one job:

- A Samba that is already there is backed up first: the dialog names the shares of its `smb.conf` and the users of its database, where it has any, and `smb.conf` and
  Samba's databases (`passdb.tdb`, `share_info.tdb`, `secrets.tdb`) get the suffix `.edelweiss-bak`; an existing backup
  is never replaced. The extension then starts empty. **Nothing is taken over.** A running `samba` service is stopped
  before the new configuration is written and started again with it.
- The extension writes `/etc/samba/smb.conf` completely itself from then on; it owns that file. An `smb.conf` that is
  not its own when it writes (one put in place after the setup), and a database that differs from its backup at a second
  setup, are kept as the next free `.edelweiss-bak.1`, `.edelweiss-bak.2`, … before they are replaced or removed. The
  files the setup only edits (`/etc/conf.d/samba`, `/etc/conf.d/wsdd`, `avahi-daemon.conf`) keep their first backup alone.
- In diskless and data mode Samba's databases are kept (see Diskless and data mode), and `lbu commit` runs.
- The services (`samba`, `avahi-daemon` with `dbus`, `wsdd`) go into the runlevel `default` and start.
- A periodic script `/etc/periodic/daily/edelweiss-samba-recycle` (with `crond`) cleans the recycle bins.

## Users

- **Add a host user:** the page asks for the user's Linux password and checks it against `/etc/shadow` (sha256 and
  sha512-crypt, bcrypt, md5-crypt and yescrypt); Samba gets the same password. A password with spaces, colons, umlauts or
  a leading `-` works. Passwords go to `pdbedit` and `smbpasswd` on standard input only: never into an argument, the
  environment, a log, an error text or a job.
- **Remove the Samba access** deletes only the Samba entry; the user stays.
- **Create a Samba user** (name and password): the Linux account is made with `busybox adduser` in the system group
  `edelweiss-samba`, which is the marker by which the accounts of this extension are recognised. The name must not be one
  of an existing user or group. "Change password" sets the Samba password; "Delete" removes the Samba entry and the Linux
  account.
- **Deleting a user** takes the name out of all shares and folder rights; the dialog names them.
- **After a user was deleted in edelweiss-ext-users** its name has no access any more. In admin mode the page marks it
  "gelöscht" in the shares and their rights, and offers "Verwaiste Namen entfernen", which takes the names out of all
  shares and folder rights; nothing is removed without that click. When one with the same name is created, the new one
  has no Samba password and no rights until it is added again: the extension drops names that are gone before it adds one.
- **An account of the extension without a Samba entry** (a removal that failed halfway) is listed "ohne Samba-Eintrag"
  and can be deleted on the page.
- The extension writes `/etc/passwd`, `/etc/shadow` and the group file with the same logic and the same locks as
  edelweiss-ext-users (`/run/edelweiss-ext-users.lock`), so the two never write the accounts at once.

## Folder rights

A share can be opened as a tree of its folders (only folders, no links). A folder gets rights of its own with the same
table as the share ("no access", "read", "read and write"), which hold for everything below it until a subfolder has its
own. "Rights of the share" takes them away again.

- **Only narrowing.** A folder's rights can only be narrower than the share's: the table offers a user at most the right
  that the share gives. When a share's right changes, the narrower of the two holds for every folder. A folder on which
  a user has no right is not shown to that user.
- **How it works.** Samba keeps NT ACLs as the extended attribute `security.NTACL` (`vfs_acl_xattr`) and checks them
  itself. The extension writes that attribute itself, which takes root with `CAP_SYS_ADMIN` (on a host given; in a
  container only with `--cap-add SYS_ADMIN`, see below), no Samba account and no running smbd. Users get "Change", not
  "Full control", so they cannot change the rights in the Windows tab "Security".
- **Only on file systems with extended attributes** (ext4, xfs, btrfs). On vfat and exfat the tree is for looking only,
  the share has no folder rights, and the dialog says why; the share runs without `vfs_fruit` and `streams_xattr`, so
  the Mac may write `._` files there.
- **The first folder right of a share is a conversion**, after a confirmation ("the existing files of this share are made
  readable and writable for the group; owner and content stay"). This is the one exception to "the extension changes
  nothing on existing files":
  - existing files get `g+rw` and directories `g+rwx`; owner, group, content and times stay;
  - every directory of the share gets a POSIX default ACL (`user::rwx,group::rwx,other::r-x`), so that new files get
    0664 and new directories 0775 and nothing new is world-writable (`acl_xattr` would force 0666 and 0777; masks do not
    help), and new subdirectories inherit it;
  - the share then runs per user: no `force user`, `force group` is the group of the folder, `inherit owner = unix only`,
    and `acl_xattr:ignore system acls = yes`;
  - the conversion leaves out some entries and counts them in its log line: files of another group (`other_group`), files
    with several names (`hard_link`), and folders that already have an ACL of their own as a default (`has_default_acl`).
    The page does not show these counts in 0.1.0;
  - a file with several names (a hard link) gets the rights all the same, when they are applied: it has one ACL for all
    its names, so it has the rights of the folder of the last apply that reached it, also where another of its names lies
    in a folder with other rights. The log line of the job counts them (`applied: hard_link=<n>`);
  - the dialog counts the entries it will change up to 10 000 ("more than" beyond);
  - it runs as a job with progress and can be cancelled. A cancelled job leaves a mixed state, which the page shows with
    "apply again".
- **Taking all folder rights away** leaves the share per user; the modes are not put back.
- **A folder moved in Windows** inside a share takes its rights along, as Windows does.
- **A disk swapped under a share with folder rights:** editing the share onto the new disk is refused. Remove the share
  and add it again, then set its folder rights again.
- **In a container without `SYS_ADMIN`** the kernel refuses writing `security.*` attributes; the page says "Die Platte
  dieser Freigabe kann keine Ordner-Rechte halten: …" with the reason that the extension may not write the `security.*`
  attributes there. The edelweiss dev environment has `SYS_ADMIN` (from its own `dev/compose.yaml`), so folder rights
  work there. On a normal Alpine host nothing is needed.

## Recycle bin

- A switch per share, with the period in days (30 by default). Shares keep a `#recycle` folder with `keeptree`: the path
  below the share is kept. A file deleted again while one of its name is in the bin is kept as `Copy #1 of <name>`
  (`recycle:versions`), and is restored under its name.
- Only users who may write see `#recycle`; for readers it is vetoed.
- The deletion time is the change time (ctime) of the file in `#recycle`.
- The admin sees every file with its old path, size and deletion time. "Restore" puts a file back at its old place and
  makes missing folders. A name conflict never overwrites: the restored file gets the suffix " (wiederhergestellt)"
  before the extension. On a file system that cannot rename without overwriting (NFS, some FUSE file systems) a restore
  is refused, and nothing is moved. "Empty the recycle bin" asks first.
- After the period the files are deleted, with no page open: the periodic script runs the binary of the extension. Empty
  folders in the bin go too.
- Switching the bin off leaves the files in `#recycle`; the dialog says so.
- The cleanup, a restore and the folder-rights jobs never write into a share whose disk is not mounted: they check the disk
  before they start, and again on the folder they open (its file system has to be the one the share recorded), so a
  disk that goes in between is not written either.

## Reachability

- **Samba** accepts connections only from private ranges (`hosts allow`: IPv4 10/8, 172.16/12, 192.168/16, IPv6 fc00::/7
  and fe80::/10) and loopback. A VPN in a private range is included.
- **Interfaces:** the settings list the interfaces with their addresses. By default the ones with private addresses are
  chosen, without the bridges of Docker and libvirt (`docker0`, `br-*`, `virbr*`, `veth*`), which cannot be chosen. **A
  new interface is not included by itself**: a VPN that is set up later shows "nicht freigegeben" until you choose it.
- **Containers and VMs on a Docker or libvirt bridge are refused.** Linux takes a packet for the host's LAN address on
  whatever interface it arrives, so a container on `docker0` or a VM on libvirt's `virbr0` would reach Samba through it,
  from a private address. `hosts allow` therefore excepts the networks of those bridges (`hosts allow = … EXCEPT
  172.17.0.0/16 192.168.122.0/24`), as they are when the configuration is written; link-local networks are not excepted,
  as they are on every interface and stay on their link. A bridge made later (Docker makes a new `br-*` network at
  `docker compose up`) is refused at the share by the root preexec, which reads the bridges at every connection, until the
  next change of the configuration excepts it in `hosts allow` too. A LAN bridge such as `br0` of edelweiss-ext-kvm does
  not match the names above and is an interface like any other.
- **WS-Discovery and mDNS** answer only on chosen interfaces that are purely local (private IPv4 addresses only), and only
  over IPv4. A LAN with IPv6 only can still connect by name or address. With the display in the network off, or no local
  interface chosen, `wsdd` and `avahi-daemon` stop and leave the runlevel, and smbd does not announce itself
  (`multicast dns register = no`).
- **An avahi-daemon that ran before the setup** (in the runlevel or started) serves something else too: the extension
  never stops it or takes it out of the runlevel. With the display in the network on, it still writes `allow-interfaces`
  (the chosen local interfaces) and `use-ipv6=no` into its `[server]` section, which then hold for the other services of
  that avahi too.
- **An unplugged disk:** a share whose folder is on a mount point that is not mounted shows "Platte fehlt", Samba refuses
  every connection to it (`root preexec close`), and nothing is written into the empty mount point. When the disk is
  back, the share works again without anything done.

## Diskless and data mode

The extension detects sys, diskless and data mode itself. In diskless and data mode every change of `/etc` (the
configuration, the accounts) is saved at once with `lbu commit`. In diskless mode, `/var` is RAM: the setup adds
`/var/lib/samba` (the Samba users, the share ACLs, the machine SID) with `lbu include`, so that they survive a reboot.

## Living with ext-users 0.2.0

When edelweiss-ext-users 0.2.0 is installed, the two extensions keep the accounts in step. Its README has the details; in
short:

- **One password for host users.** Setting the password in the Users page sets the Samba password too. After a `passwd`
  on the console, use "Passwort angleichen" on the page Users of this extension.
- **Locking a user locks Samba as well**, and unlocking gives it back. When the two locks differ (a Samba step that
  failed, a `passwd -l` on the console), the Users page offers "Samba-Sperre angleichen": Samba follows the lock of
  the Linux password, and the Linux account stays as it is.
- **Badges:** the Users page of edelweiss-ext-users marks users with "Samba" and pure Samba users with "Samba only".
- **Deleting** a user removes the Samba entry; renaming is refused while the user has Samba access. This page then
  shows the name as "gelöscht" in the shares and rights where it is still listed, and "Verwaiste Namen entfernen"
  takes it out of all of them.
- **The accounts of Freigaben are protected there** (pure Samba users, `edelweiss-share`, `smb-<uid>`): changing their
  password, shell or groups (also as members of a group), renaming and deleting are refused; manage them on this page.
- **After ext-samba is uninstalled** its accounts stay. Removing the extension in edelweiss keeps its config: delete
  `/etc/edelweiss-ext-samba` by hand too. Once `/etc/edelweiss-ext-samba/config.json` is gone, the Users page of
  edelweiss-ext-users can delete a pure Samba user or a service account (with `pdbedit -x` first while `pdbedit` is
  installed); everything else stays refused. `edelweiss-share` is a system account there: remove it by hand,
  `pdbedit -x edelweiss-share` while Samba is still there, then `deluser edelweiss-share`.
- Without edelweiss-ext-users 0.2.0 the page Users of this extension warns: an older version does not recognise pure Samba
  users and would not protect them.

## Building

Requirements: Go 1.26, Node.js 22 with npm, make and a C compiler (`gcc`: `make test` runs the Go tests with the race
detector, which needs cgo).

```sh
make build                    # the UI, then static binaries for amd64 and arm64 in build/
make test                     # Go (race detector), vitest and the test of the release guards (scripts/)
make lint                     # go vet, gofmt, Biome, TypeScript, actionlint
make release VERSION=0.1.0    # release files in dist/: edelweiss-ext-samba_0.1.0_linux_<amd64|arm64>, checksums.txt
```

The Go module requires `github.com/Pukka922/edelweiss/sdk`, and `ui/package.json` names the JS SDK by its download
URL at the edelweiss release. Both come from GitHub (the Go module proxy and `npm ci`), so a fresh clone builds with
`make build test lint`.

## Development

To develop against an edelweiss checkout next to this repository (`../edelweiss`; `EDELWEISS_DIR=<path>` for another
place) instead of the published SDKs, for example for an SDK change that is not released yet:

```sh
make dev-sdk
make build test lint
```

`make dev-sdk`:

- writes a `go.work` (git-ignored) whose `replace` points the Go SDK to `../edelweiss/sdk`;
- builds the JS SDK tarball there (`make sdk-pack`);
- installs it into `ui/node_modules` with `npm install --no-save --no-package-lock`, so `ui/package.json` stays
  unchanged and no `package-lock.json` is written.

To switch back to the published SDKs, run `rm -f go.work go.work.sum && rm -rf ui/node_modules`. The next `make`
runs `npm ci`.


### Trying it in the edelweiss dev environment

The edelweiss dev environment (`make dev` in `../edelweiss`, then http://localhost:9090) is an Alpine container without
OpenRC. Two files of this repository prepare it for this extension:

- **`dev/compose.samba.yaml`**, an overlay for `dev/compose.yaml`, adds two things to the dev container:
  - `init: true`. Docker's init becomes PID 1, and the entrypoint, which execs edelweissd, runs as its child. edelweissd
    does not reap the processes that end, so as PID 1 it would leave them as zombies (every daemon that stops). The init
    reaps them.
  - the named volume `samba-dev` at `/srv/samba-dev`. That is a real disk file system (ext4 on the WSL host), which the
    navigator of the page offers as a data disk; the root of the container is an overlay, which it does not. Make the
    folder to share below it with "New folder".

  The overlay adds no `cap_add`. The dev container has `SYS_ADMIN` from edelweiss's own `dev/compose.yaml`, so the folder
  rights work there as on a host.
- **`dev/samba-dev.sh`** prepares the container; it does **not** set Samba up, because the browser test starts with the
  button "Samba einrichten" of the page. It installs OpenRC (`apk add openrc`) and marks it present
  (`/run/openrc/softlevel`), so that the setup can run `rc-update` and `rc-service`; appends `rc_cgroup_mode="none"` and
  `rc_controller_cgroups="NO"` to `/etc/rc.conf`, once, as the cgroup tree of a container is read-only; installs the init
  scripts of busybox (`busybox-openrc`) and starts busybox `syslogd` as OpenRC's service `syslog`, as on an Alpine host
  (the setup starts `crond`, whose service needs it); and installs the packages of `requires.apk` (`samba`, `avahi`,
  `dbus`, `wsdd`), so that edelweiss finds the requirements of the extension met. It can run again at any time.

Build a release binary with a pre-release version, put it where the container sees it, and run these steps in the
edelweiss checkout (use `linux_arm64` on an arm64 machine). `make dev` creates the container without the overlay, so the
`up -d` after it creates it again with the init and the volume. A new container has nothing installed, so the order is:
`make dev`, the overlay, the script, the install, and then the setup in the browser:

```sh
make release VERSION=0.1.0-rc.1                       # in this repository
mkdir -p ../edelweiss/build
cp dist/edelweiss-ext-samba_0.1.0-rc.1_linux_amd64 ../edelweiss/build/
cd ../edelweiss
make dev
docker compose -f dev/compose.yaml -f ../edelweiss-ext-samba/dev/compose.samba.yaml up -d
docker compose -f dev/compose.yaml -f ../edelweiss-ext-samba/dev/compose.samba.yaml exec -T edelweiss sh -s \
  < ../edelweiss-ext-samba/dev/samba-dev.sh
docker compose -f dev/compose.yaml -f ../edelweiss-ext-samba/dev/compose.samba.yaml exec edelweiss \
  /work/build/dev/edelweissd ext install /work/build/edelweiss-ext-samba_0.1.0-rc.1_linux_amd64 \
  --config /work/dev/edelweiss.dev.toml
```

Then open http://localhost:9090, sign in, switch to admin mode and run "Samba einrichten". `make dev` also builds the
system overview, so it needs a checkout of [edelweiss-ext-system](https://github.com/Pukka922/edelweiss-ext-system) at
`../edelweiss-ext-system` (or `EXT_SYSTEM_DIR=<path>`). The running edelweissd picks an installed extension up within a
moment; install again after a change, which replaces the extension and stops its processes. Every `make dev` recreates
the dev container: run the steps from `make dev` on again afterwards. The volume survives `make dev-down`;
`docker volume rm edelweiss-dev_samba-dev` empties the disk. Remove `dist/` when you are done (`rm -rf dist`).

`dev/samba-dev.sh` refuses to run anywhere but in the edelweiss dev container. It needs a container marker (`/.dockerenv`
or `/run/.containerenv`) and one of two layouts: PID 1 is `edelweissd`, or PID 1 is docker's init (`docker-init`, or
`tini`) and a process named `edelweissd` has PID 1 as its parent (the layout that `init: true` makes). Anything else is
refused: the script names what it found, says "nothing done" and exits 1 before it changes anything.

Files have LF line endings (`.gitattributes`, `.editorconfig`), also in a Windows checkout.

## Tests

Nothing in the tests installs Samba, avahi or wsdd, starts `smbd`, or runs `pdbedit`, `smbpasswd`, `rc-service`, `lbu`,
`adduser` or `deluser` on the machine you work on.

- **Unit tests** (`make test`): Go with the race detector, against temporary trees of `/etc`, `/proc`, `/run` and the
  data disks, and a fake runner: the parsers against the captured outputs of the real Samba (`internal/samba/testdata`,
  whose README says how they were captured), the rendering of `smb.conf` and the interface rules, the account files and
  the password check, the setup as a plan, the jobs and their runner (locks, cancel), the folder rights (the encoder of
  `security.NTACL` byte for byte against the capture), the recycle bin, every action as its plan, and the API. Then the UI
  tests (vitest) and the test of the release guards.
- **Container tests** (`make test-alpine`): the real Samba, avahi, wsdd, OpenRC and busybox of Alpine 3.22 and 3.24 in
  throwaway containers (below).
- **Browser test** in the edelweiss dev environment with `dev/samba-dev.sh` (see Development): the page and its dialogs by
  hand, as a user with and without admin mode, folder rights included (the dev container has `SYS_ADMIN`).
- **By hand on hardware**, what neither a container nor the dev environment has (the checklist below).

### Tests in throwaway containers

`make test-alpine` (`scripts/test-alpine.sh`) runs the tests of `integration/` (build tag `integration`) with a test
binary built on your machine: one `docker run --rm --init alpine:<tag>` per scenario and image (`alpine:3.24` and
`alpine:3.22`), removed afterwards. A scenario has no extra privileges, or the capability that the table in the script
names: `NET_ADMIN` for a second or a public address, `SYS_ADMIN` only for the folder-rights scenarios (the kernel lets
only that capability write `security.*` attributes); never `--privileged`, never a device. The container first installs
what the scenario needs with apk, which is its only network access. It gets a data disk at `/srv/data`: an anonymous
volume of Docker (a real ext4 mount of Docker's data root, which keeps the attributes), which `--rm` removes with the
container, so nothing of it stays on your machine. The commands run through the runner of the extension, so the
tools get exactly the arguments and the environment the extension gives them. `scripts/test-alpine.sh <scenario>…` runs
the named scenarios, `TESTS=<text>` only those whose name contains the text, `TEST_ALPINE_IMAGES="alpine:3.24"` uses these
images only, `TEST_ALPINE_LOG=<file>` keeps the whole output, and a filter that matches nothing is an error. A scenario
counts as passed only when its output has the `--- PASS:` line of its test.

| Scenario | What it shows |
|---|---|
| `TestCapture` | the outputs the parsers read (users, shares, hiding, the recycle bin, OpenRC and busybox, avahi, wsdd, sshd), written to the log for the testdata |
| `TestCaptureACL` | `SYS_ADMIN`: the gate of the folder rights, three ways to write the NT ACLs against what smbd enforces per user |
| `TestACLAgainstSamba` | `SYS_ADMIN`: the folder rights as the extension writes them (conversion, rights on the tree, a share per user) against the real smbd, with `smbclient` as three users |
| `TestEndToEnd` | the extension from a host with the packages, through its API and the jobs of its own binary: the setup, host and pure Samba users, shares, what each user may do with `smbclient`, hidden shares, the recycle bin and its restores, connections (`smbstatus`), SMB2 off and on, removing users, and "New folder" below a folder of root |
| `TestExistingDataUntouched` | data of another program (a user's folder, a Docker volume UID) after add, edit, restore, the cleanup and emptying the bin: owner, mode, content and times as before, by a stat and sha256 manifest |
| `TestMissingDisk` | an unplugged disk (a mount point that is not mounted): the share is refused at the connect, the page says so, and nothing is written there (not by the cleanup, a restore or a job) |
| `TestHostsAllow` | `NET_ADMIN`: `smbclient` from a public address is refused, from a private one and from loopback it is let in; a client in the network of a bridge `docker0` made after the last change of the configuration is refused at the share (the root preexec), and after the next change at the connect (`hosts allow … EXCEPT`, which `testparm` takes) |
| `TestOddPassword` | passwords with spaces, `:`, umlauts and a leading `-` work for Samba and for the shadow check, and are never in a process argument, a job, an error text or a log |
| `TestSambaOnlyLogin` | a pure Samba user cannot sign in with its Samba password to the system (shell, locked password, sshd), a host user can |
| `TestForeignSambaBackedUp` | a Samba that runs before the setup: `smb.conf` and the databases are kept as `.edelweiss-bak` byte for byte, the extension starts empty, and a second setup leaves the backups |
| `TestDiscoveryInterfaces` | `NET_ADMIN`: wsdd and avahi answer on the chosen interfaces only, and stop when none is chosen |
| `TestEndToEndACL` | `SYS_ADMIN`: the folder rights through the API: `needs_convert` (with the count and `capped`), the conversion as a job with progress and a cancel, rights per user for three users with `smbclient`, a restore into a folder that is gone, and the manifest of the data of before, which shows only the bits of the conversion |

### Hardware checklist

For you, on the machines you run it on. Note what you see where a line says "note".

**Windows 10 and 11**

- [ ] Set up a fresh host ("Samba einrichten"): the host appears in the Explorer under "Network" after a minute or so
  (WS-Discovery). Opening `\\<name>` asks for a sign-in.
- [ ] Sign in as a user with "read and write": open, create, change and delete a file. As a user with "read": open only,
  creating and deleting is refused. As a user without rights: the share is not listed, and `\\<name>\<share>` is refused.
- [ ] A hidden share is not listed and opens by `\\<name>\<share>`.
- [ ] Delete a file in the Explorer: it is in the recycle bin of the share on the page (old path, size, time) and "Restore"
  puts it back; a name conflict gives "(wiederhergestellt)".
- [ ] Folder rights: after one is set (with its confirmation), the tab "Security" of the folder in the Explorer shows the
  rights; changing them there is refused (users have "Change", not "Full control"). A user without rights does not see the
  folder; a folder moved inside the share takes its rights along.

**macOS**

- [ ] The host shows in the sidebar of the Finder under "Network" (Bonjour); connect with Cmd-K to `smb://<name>`.
- [ ] No `._` files appear on the share, also after copying a folder with files from the Mac, and file names with umlauts
  and special characters are kept (note: and on a vfat or exfat disk, which has no `vfs_fruit`).

**Phones**

- [ ] The Files app of an iPhone and a file app of an Android phone connect to `smb://<host>` (or the address), sign in, list,
  open, and, with "read and write", upload a photo.

**Raspberry Pi in diskless mode**

- [ ] After the setup, reboot the Pi: the services run, the Samba users, the shares, their rights and the folder rights are
  there (`/var/lib/samba` is in `lbu list`, and the changes were committed). A share on a USB disk works after the boot.
- [ ] A scheduled clean-up of a recycle bin with a short period (1 day) works: `crond` runs after the boot and the old
  files are gone the next day.

**An unplugged USB disk**

- [ ] Pull the disk of a share: the page shows "Platte fehlt", a connection to the share is refused, and nothing appears
  in the empty mount point (`ls -A` on it). Plug it in and mount it: the share works again without anything done.
- [ ] Pull the disk while a client is connected to its share and has a folder of it open: note what the client sees and
  whether anything appears in the empty mount point (`ls -A` on it, also after the client tries to write).

**On every host**

- [ ] The reachability: with a VPN in a private range up, the interface shows "nicht freigegeben" until you choose it; a
  connection from the internet side (a public address on another interface) is refused; Docker and libvirt bridges are not
  offered.
- [ ] A VM (edelweiss-ext-kvm, libvirt's default network) and a Docker container connect to the host's LAN address
  (`smbclient -L //<LAN address> -U <user>`): refused. Do it once before and once after a change of the settings, and
  once with a Docker network made after the last change (`docker network create`).
- [ ] The switch "allow older devices (SMB2)" lets a device that needs SMB2 connect; with it off, only SMB3.
- [ ] Locking a user in the Users page of edelweiss-ext-users locks Samba too (the user cannot connect), unlocking gives it
  back; after a `passwd` on the console "Passwort angleichen" makes the Samba password the same.
- [ ] A client of a user is connected (a file open in the Explorer) while the user is locked, or deleted: note whether
  the open session can still read and write, and when it ends (the connections tab shows it).
- [ ] Folder rights on a host: the conversion of a share with real data (a photo collection) changes nothing but the group
  bits and the new default ACLs; owner, group, times and content of the files are as before (compare `stat` of a few files).

## Releasing

No file needs changing for a release: the git tag is the only source of the version.

1. Check the version with `make release-check VERSION=<version>`. It has to be a plain `x.y.z` or `x.y.z-pre` by the
   rules of Go (no leading zeros, no empty pre-release identifier, no `+build` metadata).
2. Push `main`, then the tag, which decides the version: `git push origin main`, then
   `git tag v<version> && git push origin v<version>`.

`manifest.json` holds the placeholder version `0.0.0-dev`. The release build puts the version of the tag into the
binaries (`-ldflags -X main.version`) and checks it by running the binary for the build machine with
`--edelweiss-manifest`, because edelweiss refuses a binary whose manifest version differs from the version in the name
of the release file. A local `make build` without `VERSION` reports `0.0.0-dev`.

The release workflow (`.github/workflows/release.yml`) has two jobs, so that the build cannot use the write token.
`build` has a read-only token, checks the tag name, runs `make release-check`, `make lint test` and `make release`, and
hands `dist/` on as a workflow artifact. `publish` builds nothing: it holds the write token, downloads the artifact and
runs `make gh-release`, which checks that `dist/` holds exactly `checksums.txt` and the two binaries of this version and
creates the GitHub release `v<version>`. A version with a `-` (`0.1.0-rc.1`) is published as a GitHub pre-release: the
installer takes the latest release, which leaves pre-releases out.

**Published versions are immutable.** Once a release may have been fetched, never move or delete the tag `v<version>`
and never replace or delete the files of the release: edelweiss refuses a release that changed after it showed the
SHA-256 of its file, and sum.golang.org makes `go get` fail with "checksum mismatch" for a moved tag. A broken release
is fixed with a new patch version.

The CI workflow (`.github/workflows/ci.yml`) runs `make lint test build` on every push and pull request.

## First publication

1. Create the public repository `Pukka922/edelweiss-ext-samba` on GitHub. Recommended before the first tag: a tag
   ruleset (Settings → Rules → Rulesets, target: tags) that blocks deleting and updating the tags `v*`. Then push
   `main` and the tag of the version:

   ```sh
   git remote add origin https://github.com/Pukka922/edelweiss-ext-samba.git
   git push -u origin main
   git tag v0.1.0
   git push origin v0.1.0
   ```

   When the release workflow has finished, `edelweissd ext install Pukka922/edelweiss-ext-samba` installs the
   extension, as does **Settings → Extensions → Install from GitHub** in the web interface (admin mode).

## License

MIT, Copyright (c) 2026 Pukka922, see LICENSE.
