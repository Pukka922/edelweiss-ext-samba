# Captured outputs of Samba, OpenRC and busybox: users, shares, recycle bin, folder rights

These files are the unchanged output of Samba, OpenRC, busybox, avahi, wsdd, sshd and the kernel of Alpine, captured in
throwaway containers by `make test-alpine` (`scripts/test-alpine.sh`). The tests are `TestCapture` in
`integration/capture_test.go` (no extra capability), `TestCaptureACL` in `integration/capture_acl_test.go` and
`TestACLAgainstSamba` in `integration/acl_samba_test.go` (with `--cap-add SYS_ADMIN`, which the kernel needs for writing
`security.*` attributes), and `TestDiscoveryInterfaces` in `integration/e2e_test.go` (with `--cap-add NET_ADMIN`, for
its second interface). Do not edit or reformat them:
`.editorconfig` keeps editors from touching them, and `.gitattributes` keeps git from changing their line ends (`ssh.txt`
has the CR LF of ssh).

They come from `alpine:3.24` (3.24.2: Samba 4.23.8-r0, OpenRC 0.63.2, busybox 1.37.0-r31, avahi 0.9_rc4-r0, dbus
1.16.2-r2, wsdd 0.9-r0, OpenSSH 10.3_p1, apk-tools 3.0.8). `alpine:3.22` (3.22.6: Samba 4.21.9-r1, OpenRC 0.62.6,
busybox 1.37.0-r20, avahi 0.8-r21, dbus 1.16.2-r1, wsdd 0.8-r0, apk-tools 2.14.12) is captured as well and does the same
everywhere except where "Alpine 3.22" below says otherwise, so there are no 3.22 files but `avahi-daemon.conf-3.22`.

The commands run through `run.Exec`, the runner of the extension (no shell, `/` as the working directory, the
environment `PATH=/usr/sbin:/usr/bin:/sbin:/bin LC_ALL=C EINFO_COLOR=NO`); passwords go on stdin (`run.Cmd.Stdin`). The
shared folders are below `/srv` and belong to the host user `myUser` (UID 1000, group `myUser` 1000); each share has
`force user = myUser` and `force group = myUser`. The `[global]` section is the test's `globalConf`: `server role =
standalone server`, `workgroup = WORKGROUP`, `netbios name = CAPTURE`, `server min protocol = SMB3`, `disable netbios =
yes`, `smb ports = 445`, `map to guest = never`, `load printers = no`, `printing = bsd`, `printcap name = /dev/null`,
`disable spoolss = yes`, `log file = /var/log/samba/log.%m`, `max log size = 0`. The Samba users are pure Samba users
made as in item 3 below. A command that a release cannot do, or a refusal, is a result: the tests record its exit status
and text and pass.

**Container-only output:** in the containers, OpenRC (both releases) cannot make the cgroup of a service, and many
`rc-service` outputs carry a line `mkdir: can't create directory '/sys/fs/cgroup/openrc.<service>': Read-only file system`
before its `* Starting …` line (in `services.txt`, `interfaces.txt`, `reload.txt`, `crond.txt`, `ssh.txt`,
`acl-methods.txt` and `acl-peruser.txt`). A host does not print it; parsers and golden comparisons must ignore it.

## The gate

Gate: per-user NT ACLs under force user: no

`TestCaptureACL` prints this line (the last line of the block `acl-gate`) as "yes" or "no" only from a method whose ACL
smbd reads back with usera's and userb's ACEs on `sub`: "yes" when the rights held per user, "no" when the three users got
the same results. Without such a method it prints "unknown" and fails. Item 6 below has the evidence.

## Summary for the later tasks

1. **Packages for `requires.apk`:** `samba`, `avahi`, `dbus`, `wsdd`, on both releases (`apk search -x samba avahi dbus
   wsdd wsdd2` finds the first four; there is no `wsdd2`).
   - `samba` is a package without files that depends on `samba-server` (`/usr/sbin/smbd`, `/usr/sbin/nmbd`,
     `/usr/bin/smbstatus`), `samba-client` (`/usr/bin/smbclient`, `/usr/bin/smbcacls`, `/usr/bin/sharesec`) and
     `samba-common-tools` (`/usr/bin/pdbedit`, `/usr/bin/smbpasswd`, `/usr/bin/smbcontrol`, `/usr/bin/testparm`,
     `/usr/bin/net`).
   - The init scripts are in `samba-server-openrc` (`/etc/init.d/samba`, `/etc/conf.d/samba`), `avahi-openrc`
     (`/etc/init.d/avahi-daemon`), `dbus-openrc` (`/etc/init.d/dbus`) and `wsdd-openrc` (`/etc/init.d/wsdd`,
     `/etc/conf.d/wsdd`); apk installs them with the packages when `openrc` is installed.
   - `samba-tool` is only in `samba-dc` (the AD DC, with Python), which the extension does not need (item 6).
2. **The tdb files to persist in diskless:** `passdb backend = tdbsam`; the paths are the compiled-in defaults
   (`testparm -sv`, `smbd -b`):
   - `private dir = /var/lib/samba/private`: `passdb.tdb` (the Samba users and their NT hashes) and `secrets.tdb` (the
     machine SID), both 0600 root;
   - `state directory = /var/lib/samba`: `share_info.tdb` (the share ACLs of `sharesec`), `account_policy.tdb`,
     `group_mapping.tdb`, `registry.tdb`, all 0600 root;
   - `lock directory` and `cache directory` are `/var/cache/samba` (`gencache.tdb`, `locking.tdb`, `brlock.tdb`,
     `smbXsrv_*.tdb` and the like: runtime state), and `pid directory = /run/samba`; neither needs to survive a reboot;
   - `/var/lib/samba/private/msg.sock/` holds the message sockets of the running daemons, so an `lbu include` of the
     whole directory takes sockets along; the six files above are what has to persist (`paths.txt`).
3. **The user-creation line:** `busybox addgroup -S edelweiss-samba` once (GID 103 in the image), then
   `busybox adduser -D -H -s /sbin/nologin -G edelweiss-samba <name>`. It writes
   `u1:x:1001:103::/home/u1:/sbin/nologin` to `/etc/passwd` (the home field is `/home/<name>`, which is not created),
   `u1:!:20761:0:99999:7:::` to `/etc/shadow`, and adds the name to the members of the group line:
   `edelweiss-samba:x:103:u1`. `/sbin/nologin` is a link to busybox. The Samba entry: `pdbedit -a -u <name> -t` with
   `<password>\n<password>\n` on stdin (exit 0, it prints the entry as `pdbedit -L -v` does). `users.txt`.
4. **Hiding:**
   - **Shares:** `access based share enum = yes` hides a share from a user who is not in its `valid users`, without
     any share ACL (`hide-lists-only-outsider`); a share ACL that leaves a user out hides the share as well and refuses
     the connection (`hide-acls-outsider`, `hide-connect-open-outsider`). `sharesec` takes SIDs only: `sharesec <share>
     --replace '<SID>:ALLOWED/0/READ,<SID>:ALLOWED/0/CHANGE'` (exit 0, no output); names, bare or as `CAPTURE\name`,
     fail with `failed to convert '<name>' to SID`, exit 255. A share ACL also narrows: `READ` there refuses a user of
     `write list` (`share-acl-narrows`). `browseable = no` hides a share from every list and still lets its users
     connect. `hiding.txt`.
   - **`#recycle` for users who may only read:** a per-user include works: the share section gets
     `include = /etc/samba/<file>-%U.conf`, and the file of each such user holds `veto files = /#recycle/`; the reader
     does not see `#recycle` and gets `NT_STATUS_OBJECT_NAME_NOT_FOUND` for it, the writer sees and lists it, and the
     writer's deletions still reach it (`recycle-hide-include-user`). A missing include file is no error. `hide files =
     /#recycle/` only sets the hidden attribute (`DH`) for everybody; `veto files = /#recycle/` in the share hides it
     from everybody, the recycle bin still working. An NT ACL on `#recycle` cannot tell readers from writers under
     `force user` (item 6, `acl-recycle-writer-only`). `recycle.txt`.
5. **The deletion time of the recycle bin: the change time (ctime)** of the file in `#recycle`, which the move into
   `#recycle` sets; the recycle cleanup uses it (`recycle-delete`, `recycle-times`).
   - **The other times:** the modification time stays the file's own (`recycle:touch_mtime = yes` would set it too).
     `recycle:touch = yes` sets the access time to the deletion as well, a few milliseconds after the ctime, but the
     atime is no reliable deletion time: under `relatime` (the mount option of the capture and the kernel's default) a
     read moves it once it is more than 24 hours old (the kernel's rule, not captured), and under `noatime` nothing
     moves it. Two reads right after the deletion left atime and ctime as they were.
   - **What else moves the ctime** of a file in `#recycle` (`recycle-times`): a change of its DOS attributes by a
     client (smbd writes `user.DOSATTRIB`), and any extended attribute written to it, as a job of the folder rights
     would write `security.NTACL` there; also, by the kernel's rules (not captured), a change of mode, owner or content,
     and a rename. A read, a listing and a second deletion of a file with the same name (which becomes `Copy #1 of
     t.txt`) leave it as it is.
   - **The layout:** with `recycle:keeptree = yes` the path below the share is kept (`#recycle/dir/a.txt`);
     `recycle:versions = yes` names the second one `Copy #1 of a.txt`. smbd creates `#recycle` and its folders 0700,
     owned by the user of `force user`; the files keep owner and mode. A file deleted inside `#recycle` is gone.
     `recycle.txt`.
6. **The ACL method and the gate:**
   - **The gate:** no, the line under "The gate" above.
   - **Evidence** (`acl-enforce.txt`, `acl-gate.txt`, the same on both releases): the folder `sub` of a share with
     `vfs objects = acl_xattr`, `acl_xattr:ignore system acls = yes`, `hide unreadable = yes`, `force user = myUser`
     and all three users in `write list` got the ACL "usera CHANGE, userb READ" (container and object inherit) on
     `sub` and the ACEs below it; with each method that stored it (on 3.24 all three, on 3.22 (b) and (c)) smbd reads
     back usera's and userb's ACEs on `sub` and below it (`acl-*-readback`). With each of them usera, userb and userc
     got the same results in `sub`: with (a) and (c) all refused and `sub` hidden from all three (`hide unreadable`);
     with (b) all three read the old file (smbcacls kept its `Everyone` READ) and none writes. With one more ACE, FULL
     for the folder owner `S-1-22-1-1000`, all three users list, read and write (the control, which the test requires).
     smbd checks the token of the user it works as (the forced user `myUser`), not the session user's. The root preexec
     sees `%U` = the session user and `%u` = `myUser` (`acl-preexec-log`).
   - **No option keeps the session token:** `testparm -sv` has no parameter for it (`acl-parameters` lists every
     parameter with force, inherit or acl in its name).
   - **What is enforced per user** is `valid users`, `read list`, `write list` and the share ACL, before force user takes
     over.
   - **Without `force user`** (only `force group = myUser`, `inherit owner = unix only`, a copy of the tree whose
     folders and files the group may write, mode 0775/0664), the same ACL holds per user: usera reads and writes, userb
     reads and is refused writing, userc is refused and does not see `sub`. New files are owned by `myUser` on disk and
     by usera's SID in the NT ACL (`acl-enforce-g`, `acl-enforce-g-new`). The users then need Unix write permission on
     the existing folders, that is, a change of their mode.
   - **The method:** (c) works. Writing `security.NTACL` from Go (version 1 of `xattr_NTACL`) needs root with
     `CAP_SYS_ADMIN` and no smbd, no account and no password. smbd reads it back as written, and it leaves owner, mode
     and mtime of every file unchanged (`acl-c-readback`, `acl-c-stats`). The byte layout is under "The folder-rights
     attribute" below; the encoder that wrote these blobs is `NTACL` and `Inherited` of package `internal/acl`, which
     the extension writes with and which `integration/capture_acl_test.go` uses (`TestNTACLCaptured` compares it with
     `acl-c-readback` byte for byte).
   - (b) works with limits. `smbcacls //127.0.0.1/edelweiss-acl$ <path> -A <file> --numeric --propagate-inheritance
     --set 'ACL:<SID>:ALLOWED/OI|CI/CHANGE,…'` runs as a hidden internal account (`admin users`) of a hidden share of
     `/srv`. It needs `-I remove` on the folder first, because without it smbcacls answers `Inheritance enabled at
     \acl\sub, can't apply set operation` (exit 1). It adds the inherited ACEs to the existing files and keeps their
     explicit ACEs (`Everyone` READ and the owner's FULL, taken from the mode), so existing files stay readable for
     everybody. Its CHANGE is `0x001301ff`. The password can come on stdin (`-U <account>`, prompt `Password for
     [WORKGROUP\edelweiss-acl]:`) or from the file of `-A`.
   - (a) does not work as shipped. `samba-tool ntacl set` needs `apk add samba-dc`. On 3.24 it fails with `ERROR: Unable
     to read domain SID from configuration files` (exit 255) and works only after `net setdomainsid <the local SID>`
     writes a domain SID into `secrets.tdb`; `--recursive` then writes the same explicit ACL, `P` and `OICI`, on every
     file. On Alpine 3.22 every `samba-tool` subcommand fails with `ModuleNotFoundError: No module named
     'cryptography'`.
   - New files and folders in a share with `acl_xattr:ignore system acls = yes` get mode 0766 and 0777 on disk
     (`acl-enforce-*-new`), against 0744 and 0755 without `acl_xattr` (`s1-files`); masks do not change that ("Per-user
     shares" below).
7. **The service order:** dbus, avahi-daemon, samba, wsdd (`services.txt`):
   - `avahi-daemon` needs `dbus` and `hostname` (OpenRC starts dbus by itself), and `wsdd` needs `samba` and `net`;
     `samba` needs `net`. In the container these start with a plain `rc-service <name> start`, with OpenRC's
     softlevel and its service `syslog`.
   - smbd announces `_smb._tcp` (port 445) and `_device-info._tcp` (`model=MacSamba` with `fruit:model`) through avahi
     only when avahi runs as smbd starts: started before avahi it announces nothing, until a restart of samba
     (`avahi-browse-samba-first`, `samba-restart`). So avahi-daemon comes before samba.
   - `rc-service samba restart` stops wsdd and starts it again.
   - `/etc/conf.d/samba` has `daemon_list="smbd nmbd"`, and nmbd runs and binds UDP 137 and 138 although `disable
     netbios = yes`. With `daemon_list="smbd"` only smbd starts, but the stop of a restart uses the new list too and
     leaves the old nmbd running (`daemon-list-smbd`); the list has to change while samba is stopped.
   - `rc-update add <name> default` works for all of them. `rc-service crond start` works with syslog started through
     OpenRC (`crond.txt`).

## Files

| File | What it holds |
|---|---|
| `packages.txt` | versions, `apk search -x`, `apk info -L`/`-R` of `samba` and its parts, the owner package of each tool and init script |
| `conf.d-samba`, `init.d-samba`, `smb.conf.default` | the files of the packages, as installed |
| `avahi-daemon.conf`, `avahi-daemon.conf-3.22` | `/etc/avahi/avahi-daemon.conf` of avahi as installed, on 3.24 and on 3.22 (`TestDiscoveryInterfaces`, before the setup writes `allow-interfaces` and `use-ipv6` into its `[server]`) |
| `testparm-sv-default.txt` | `testparm -sv` of the package's `smb.conf` |
| `paths.txt` | the path parameters of `testparm -sv`, Samba's directories before the start, and every file of `/etc/samba`, `/var/lib/samba`, `/var/cache/samba` and `/run/samba` after the capture (`stat -c '%A %U:%G %s %n'`) |
| `services.txt` | `depend()` of the init scripts, the starts, `rc-service … status`, `ps`, `netstat`, the system log, what avahi announces, the restarts, `daemon_list="smbd"`, `rc-update add`, the stop |
| `users.txt` | `busybox adduser` and the account files, `pdbedit -a`/`-L`/`-L -w`/`-x`, `smbpasswd -s`/`-d`/`-e` with the logins before and after, `pdbedit -a` of an entry that exists, the password `ä b:c -d` |
| `pdbedit-L-v.txt` | `pdbedit -L -v` with four users (from `TestCaptureACL`) |
| `shares.txt` | `testparm -s` of the share `s1`, and every operation as reader, outsider and writer, one connection each, with the files on disk |
| `share-names.txt` | `testparm -s` of a configuration with share names that the name rule has to decide on |
| `hiding.txt` | `smbclient -L` as each user with access based share enum, without and with share ACLs, and `sharesec` |
| `protocol.txt` | `smbclient -m NT1`, `SMB2`, `SMB3` against `server min protocol = SMB3` and `SMB2_02` |
| `missing-disk.txt` | `root preexec` with `root preexec close = yes`: the refusal, the text a preexec writes to stderr, and the arguments `%S` and `%U` give a preexec |
| `recycle.txt` | `vfs_recycle`: what a deletion leaves with its times, versions, a reader's deletion, a deletion inside `#recycle`, `recycle:touch_mtime`, what moves the times of a file in `#recycle` afterwards (`recycle-times`, in nanoseconds), and the three ways of hiding `#recycle` |
| `mac.txt` | `catia fruit streams_xattr` on the overlay: a stream, a resource fork, streams of 3, 8 and 70 KiB, and the attributes on disk |
| `smbstatus-none.json`, `smbstatus-idle.json`, `smbstatus-open.json` | `smbstatus --json` without a connection, with one that has no file open, and with a second one that holds a file open |
| `smbstatus.txt` | `smbstatus --help`, the text forms, `-b`, `-L --json`, and what the two clients printed |
| `reload.txt` | `smbclient -L` before and after `smbcontrol all reload-config` of a new share, `smbcontrol smbd reload-config`, `rc-service samba reload` |
| `proc-net-if_inet6`, `proc-net-fib_trie` | the files of the container (lo and eth0 172.17.0.2/16) |
| `interfaces.txt` | `/sys/class/net`, `ip -o addr`/`link`, `interfaces` with `bind interfaces only` (restart and reload), `hosts allow` |
| `crond.txt` | `rc-service crond start` and `status`, and crond's log line |
| `ssh.txt` | `rc-service sshd start`, ssh with the Samba password of a pure Samba user (`sshpass`) and in batch mode, sshd's log lines |
| `xattr.txt` | `setxattr` of `security.NTACL` and `user.*` (16 bytes and 8 KiB) on the overlay and on `/dev/shm`, without SYS_ADMIN |
| `errors.txt` | the error texts, one `=== error-<case> ===` block each |
| `acl-methods.txt` | `TestCaptureACL`: the start, the users, the attribute probe with SYS_ADMIN, the ACLs smbd made up before any was set, and methods (a), (b) and (c) with their readbacks (`smbcacls --numeric` and the attributes in hex) |
| `acl-enforce.txt` | the operations as usera, userb and userc after each method, with the owner's ACE, and without force user, with the new files |
| `acl-gate.txt` | the ACL on `#recycle`, the gate, the preexec log, the parameters of `testparm -sv` with force, inherit or acl |
| `acl-peruser.txt` | `TestACLAgainstSamba`: the conversion with the stats before and after and the default ACLs it wrote, the rendered per-user share, the ACLs read back, a DACL-only change by an NT owner without and with the share ACL, the operations as usera, userb and userc, the owners and modes of new files, the recycle bin and the folders it makes, with and without the recycle modes, two restores, an apply that keeps `sub`, the modes with `inherit permissions`, with a POSIX default ACL and without `ignore system acls` |

The files without marker lines (`conf.d-samba`, `init.d-samba`, `smb.conf.default`, `avahi-daemon.conf*`,
`testparm-sv-default.txt`, `pdbedit-L-v.txt`, the JSON files, `proc-net-*`) are the output of one command or a file,
byte for byte. The others keep the blocks
of the log, each below its `=== <name> ===` line; a block that ran a command through `c.result` starts with
`exit status: <n>`.

## What the capture found

### Users

- **Adding:** `pdbedit -a -u <name> -t` reads the password twice from stdin; different passwords give `Passwords do
  not match!` (exit 255), a name without a Unix account `Failed to add entry for user nosuch.` (exit 255).
- **An entry that exists:** `pdbedit -a` of it exits 0, prints the entry and sets the new password; the SID stays
  (`pdbedit-a-exists`).
- **SIDs:** `pdbedit -L -v` prints blocks below a `---------------` line with `Unix username:`, `Account Flags:` (`[U
  ]`, `[DU ]` when disabled) and `User SID:` (`S-1-5-21-<machine>-<RID>`, the RIDs counting from 1000). An entry that
  is removed and added again gets a new RID, so ACLs that name the old SID do not apply to the new entry.
- **Changes:** `smbpasswd -s <name>` reads the new password twice from stdin (exit 0, no output); afterwards only the
  new password logs in. `smbpasswd -d` prints `Disabled user u1.`, the login fails with `NT_STATUS_ACCOUNT_DISABLED`;
  `smbpasswd -e` prints `Enabled user u1.`. `pdbedit -x <name>` exits 0 without output and leaves the Unix account.
- **Errors:** `pdbedit -x nosuch`: `user nosuch does not exist in the passdb` (255); `smbpasswd -s nosuch` and `-d`:
  `Failed to find entry for user nosuch.` (1); `smbpasswd -s -a nosuch` without a Unix account: `Failed to add entry
  for user nosuch.` (1).
- **The password `ä b:c -d`** works through stdin for `pdbedit` and for the login (`odd-password`).
- **SSH:** sshd refuses a pure Samba user with its Samba password; sshd logs `User u2 not allowed because account is
  locked` (`auth.info`), then `Failed password for invalid user u2`, and ssh exits 5 (sshpass) or 255 (batch).

### Shares

- **Reader** (`read list`, share `read only = yes`) lists and gets; put is refused with `NT_STATUS_ACCESS_DENIED
  opening remote file \reader-new.txt`, del with `NT_STATUS_ACCESS_DENIED deleting remote file \old.txt`, mkdir with
  `NT_STATUS_ACCESS_DENIED making remote directory \reader-dir`, rmdir with `NT_STATUS_ACCESS_DENIED removing remote
  directory file \olddir`. smbclient exits 0 for the refused del, mkdir and rmdir, and 1 for a refused put: only the
  text tells.
- **Writer** (`write list`) does all of it.
- **Outsider** (not in `valid users`): `tree connect failed: NT_STATUS_ACCESS_DENIED`. A wrong password or an unknown
  user: `session setup failed: NT_STATUS_LOGON_FAILURE`; an unknown share: `tree connect failed:
  NT_STATUS_BAD_NETWORK_NAME`.
- **On disk** a new file is `myUser:myUser` 0744 and a new folder 0755 (the default `create mask` and `directory
  mask`).
- **Share names** (`share-names.txt`): testparm accepts spaces, umlauts, `;`, `'`, `$`, `=`, `/`, `\`, `#`, a name of 81
  characters, and `IPC$`, `Homes` and `GLOBAL` as section names; `[GLOBAL]` merges into `[global]` (its lines show up
  there).

### Protocol

`server min protocol = SMB3`: `smbclient -m SMB2` fails with `Protocol negotiation to server 127.0.0.1 (for a
protocol between SMB2_02 and SMB2) failed: NT_STATUS_NOT_SUPPORTED`, `-m SMB3` works. With `SMB2_02` both work. `-m NT1`
fails on the client side (`Max protocol NT1 is less than min protocol SMB2_02`). The dialect of the sessions is
`SMB3_11`.

### Missing disk

`root preexec = /bin/false` with `root preexec close = yes` refuses the connection: `tree connect failed:
NT_STATUS_ACCESS_DENIED`; the client gets no reason. What the preexec writes to stderr lands in smbd's log
(`/var/log/samba/log.*`, line by line as written). smbd
runs the preexec through a shell: `%S` of the share `My Share` gives two arguments unless quoted (`'%S'` gives `My
Share`), and the characters `'`, `;` and `$` of a share name become `_` (`it's;$x` → `it_s__x`). `%U` is the session
user.

### Mac

On the overlay, `vfs objects = catia fruit streams_xattr` stores a stream as `user.DosStream.<name>:$DATA` and the DOS
attributes as `user.DOSATTRIB`; a resource fork (`AFP_Resource`) goes to an AppleDouble file `._f.txt` (the default
`fruit:resource = file`). A stream of 3 KiB fits; one of 8 KiB fails with `NT_STATUS_DISK_FULL` and one of 70 KiB with
`NT_STATUS_FILE_SYSTEM_LIMITATION` (the attribute size of the file system under the overlay; `user.*` of 8 KiB gives
`ENOSPC` there, `xattr.txt`); both leave an empty stream, and the file's data is unharmed. The `fruit:` globals of
`mac-testparm` are accepted. `/dev/shm` (tmpfs) takes `user.*` attributes of 8 KiB; no file system without attributes
could be captured (that needs a mount).

### Connections

`smbstatus --json` (also `-j`) works on both releases: `sessions` (by session id: `username`, `groupname`, `uid`, `gid`,
`remote_machine`, `hostname` as `ipv4:127.0.0.1:<port>`, `session_dialect`, `creation_time` as
`2026-11-04T10:20:26.054233+00:00`, `encryption`, `signing`, `channels`), `tcons` (by tcon id: `service`, `session_id`,
`machine`, `connected_at`), `open_files` (by path: `service_path`, `filename`, `opens` with `uid` of the forced user,
`sharemode`, `access_mask`, `opened_at`). Without connections all three are `{}`. `smbstatus -L --json` has only
`open_files`. smbstatus needs root.

### Reload

A new smbd connection reads a changed `smb.conf` by itself: the new share is listed before `smbcontrol all
reload-config` (`reload-before`). `smbcontrol all reload-config` exits 0 without output, also when no smbd runs;
`smbcontrol smbd reload-config` then fails with `Can't find pid for destination 'smbd'` (exit 1). `rc-service samba
reload` sends HUP to each daemon of the list.

### Interfaces

- `/proc/net/if_inet6` has only `lo` (`::1`) in the container; `/proc/net/fib_trie` has the `Main:` and `Local:`
  tables with `/32 host LOCAL` for the addresses. `/sys/class/net/<name>` links to `../../devices/virtual/net/<name>`
  for both `lo` (type 772) and `eth0` (type 1, a veth).
- **`interfaces` and `bind interfaces only = yes`** take effect at a restart of samba only: after a reload smbd still
  listens on the old addresses; after a restart with `interfaces = lo` it listens on `127.0.0.1:445` and `::1:445`,
  and a connection to the address of eth0 fails with `do_connect: Connection to 172.17.0.2 failed (Error
  NT_STATUS_CONNECTION_REFUSED)`.
- **`hosts allow = 127.0.0.1 ::1`** refuses a client from 172.17.0.2 with `Protocol negotiation … failed:
  NT_STATUS_INVALID_NETWORK_RESPONSE`, and smbd logs `Denied connection from 172.17.0.2 (172.17.0.2)`. `hosts allow =
  127.0.0.1 ::1 10.0.0.0/8 172.16.0.0/12 192.168.0.0/16 fc00::/7 fe80::/10` is accepted by testparm as written, and
  lets in 172.17.0.2 and `::1`; it takes effect at a reload.
- wsdd listens on the address of eth0 (TCP 5357, UDP 3702 and the group 239.255.255.250:3702), whatever smb.conf's
  `interfaces` say; `WSDD_OPTS` in `/etc/conf.d/wsdd` limits it (`-i eth0`, as the file's comment shows). It reads the
  workgroup from Samba (`wsdd -w WORKGROUP`).

### The folder-rights attribute

- **The privilege:** without SYS_ADMIN, `setxattr` of `security.NTACL` fails with `EPERM`, also on `/dev/shm`; with it, it
  works (`acl-xattr-privilege`). A `user.*` attribute needs nothing.
- **The ACL before any is set:** smbd makes up the ACL of a file from its owner and mode: `OWNER:S-1-22-1-<uid>`,
  `GROUP:S-1-22-2-<gid>`, the owner FULL, the group and `S-1-1-0` (Everyone) READ, `S-1-5-18` FULL; a host user without
  a Samba entry is `S-1-22-1-<uid>`.
- **The format smbd reads** (`acl-c-readback`, written by `NTACL` of `internal/acl`): the NDR of `xattr_NTACL` version 1,
  all numbers little endian except the SID authority:
  - bytes 0–7: the version `01 00`, the union's switch value `01 00`, the referent id of the pointer to the descriptor,
    not zero (`00 00 02 00`);
  - from byte 8 the self-relative security descriptor: revision `01`, a zero byte, the control (u16), then four u32
    offsets of owner, group, SACL (0: none) and DACL. **They count from the start of the attribute, not of the
    descriptor**: with the descriptor at byte 8 the owner is at `0x1c`. Written the other way, smbd reads a wrong owner
    and no ACEs;
  - then the owner SID, the group SID and the DACL, in this order. A SID is its revision `01`, the number of
    sub-authorities, the authority as 6 bytes big endian (`00 00 00 00 00 16` for `S-1-22`), then each sub-authority
    as u32: `S-1-22-1-1000` is `01 02 00 00 00 00 00 16 01 00 00 00 e8 03 00 00`;
  - the DACL: its revision (u16, 2), its size with the ACEs (u16), the number of ACEs (u32), then the ACEs. An ACE: its
    type (u8, 0 = allowed), its flags (u8), its size (u16, 8 plus the SID: `0x24` for a user SID `S-1-5-21-a-b-c-rid`),
    the access mask (u32: `bf 01 13 00` for CHANGE `0x001301bf`, `a9 00 12 00` for READ), then the SID.

  Samba itself writes version 3 (`03 00 03 00 …`, the descriptor at byte 80, its offsets from the start of the attribute
  as well; `acl-b-inherit-remove`).
- **Flags:** a protected DACL has the control `0x9004` (`SR|PD|DP`); inherited children were written with `0x8404`
  (`SR|DI|DP`) and their ACEs with flag `0x10` (inherited, files) or `0x13` (OI|CI|inherited, folders). A new file made
  through smbd below such a folder gets the folder's inheritable ACEs as ACEs of its own, without the inherited flag:
  flags `0x0` for a file, `0x3` for a folder, control `0x8004` (`acl-enforce-*-new`); with `inherit owner = unix only`
  its NT owner is the session user's SID.
- **(a) and (b) on existing files:** `samba-tool ntacl set --recursive` writes `sub`'s ACL, `P` and `0x3` flags, as it is
  on every file and folder below it; smbcacls' `--propagate-inheritance` adds ACEs with `0x10`/`0x13` to the explicit
  ones the files had (`acl-b-readback`).

### Per-user shares

`TestACLAgainstSamba` (`acl-peruser.txt`; the same on both releases but the SIDs, times and transfer rates) runs the
extension's own code against smbd: the share `pu` of `/srv/pu` (owner `myUser`, 1000:1000) with data of before,
`--samba-tool convert`, `apply-acl` of the share's rights (usera and userb write, userc reads) on the whole tree, the
share section that `samba.Render` makes for a `PerUser` share (`pu-render`), and `apply-acl` of the narrowed rights of
`sub` (usera writes, userb reads, userc nothing). The SIDs come from `pdbedit -L -v`, as a job's plan takes them. Then
the share gets its share ACL as the setup sets it (`samba.ShareACLCmd`: usera and userb CHANGE, userc READ;
`pu-share-acl`).

- **convert** gives the files g+rw and the folders g+rwx, the folders of `#recycle` too, and leaves owner, group, mtime,
  content and the other bits, the files of `#recycle` with their change time, the links and what they point to
  (`pu-stats-before`, `pu-stats-converted`; the test compares a stat and hash manifest). Every folder, those of
  `#recycle` too, also gets the POSIX default ACL `user::rwx`, `group::rwx`, `other::r-x` (`system.posix_acl_default` =
  `0200000001000700ffffffff04000700ffffffff20000500ffffffff`, written from Go, byte for byte what setfacl wrote in
  `pu-mode-variants`; `pu-default-acls`); files get none, and modes and access ACLs stay. It only gives the bits and the
  default ACL to what belongs to the group of the share's folder (the group the users get), the bits to no regular file
  with more than one name (another name may be outside the share), and leaves a folder with a default ACL of its own as
  it is; its last line counts what it left alone: `skipped: other_group=0 hard_link=0 has_default_acl=0` here, where
  everything is `myUser`'s (the unit tests of `internal/acl` cover all three).
- **The rights hold per user** (`pu-enforce`): usera reads and writes in the share and in `sub`; userb writes in the
  share and only reads in `sub` (put, mkdir, overwrite and delete there: `NT_STATUS_ACCESS_DENIED`); userc reads in the
  share and may not write, does not see `sub` (hide unreadable) or `#recycle` (the veto of its include file), and is
  refused in `sub`. smbcacls reads the ACLs back as written (`pu-readback`).
- **Who owns new files:** on disk the owner and the group of the share's folder (`inherit owner = unix only`, `force
  group`), in the NT ACL the session user (`pu-new-modes`, `pu-dacl-without-share-acl`). The folders vfs_recycle makes
  are the exception (below).
- **The share ACL is what keeps users from changing ACLs.** The NT owner of an object gets WRITE_DAC without an ACE, and
  with `inherit owner = unix only` a user is the NT owner of every file the user makes. `smbcacls --set` sends no owner,
  so it asks for WRITE_DAC alone, as the Sicherheit tab of Windows does:
  - without a share ACL (smbd's default gives Everyone FULL at the share) usera sets the DACL of a file usera made to
    "usera FULL" (`pu-dacl-without-share-acl`, exit 0);
  - with the share ACL (CHANGE has no WRITE_DAC and no WRITE_OWNER) the same change, on such a file and on a file of
    before, fails with `Failed to open …: NT_STATUS_ACCESS_DENIED` (`pu-dacl-with-share-acl`).
  The folder rights never give FULL either, but without the share ACL that does not keep an NT owner out: a `PerUser`
  share must always have its share ACL. (`smbcacls -a`, `-M` and `-D` send the old owner along and so ask for
  WRITE_OWNER as well, which CHANGE does not give in any case: they prove nothing about WRITE_DAC.)
- **New files are 0664 and folders 0775, because of the default ACL that convert writes; no setting of smb.conf does
  it.** In the converted share, what usera and userb make through smbclient is 0664 (files) and 0775 (folders), and a
  new folder has the default ACL itself (`pu-new-modes`). Without the default ACL smbd makes files 0666 and folders
  0777, whatever `create mask = 0664` and `directory mask = 2775` say (`pu-mode-variants`, the share `pu-ip`). At the
  connect,
  `acl_xattr:ignore system acls = yes` sets `create mask = 0666`, `directory mask = 0777` and `inherit acls = yes`
  (smbd's log said so at log level 3 while this test was written: `connect_acl_xattr: setting 'create mask = 0666'`, and
  `setting 'directory mask = 0777', 'store dos attributes = yes' and all 'map ...' options to 'no'`). With `inherit acls`
  and inheritable ACEs in the NT ACL of the parent, smbd opens a new file with `0777 & create mask` and makes a folder
  with `0777 & directory mask`, and leaves the rest to a POSIX default ACL of the parent; its umask is 0. `inherit
  permissions = yes` does not help (`pu-mode-variants`: 0666 and 0777; at log level 10 `unix_mode` returned 0664 and the
  file was then opened with 0666). `force create mode` and `force directory mode` only add bits: that is Samba's
  documented behaviour, not a capture (a variant with them set to 0 was the default and changed nothing). A POSIX default
  ACL on the folder does limit it: with `default:user::rwx`, `default:group::rwx`, `default:other::r-x` new files are
  0664 and folders 0775, and a new folder gets the default ACL itself (`pu-mode-variants`). Without
  `acl_xattr:ignore system acls` (against the decision for these shares) acl_xattr maps the NT ACL to POSIX ACLs: new
  files are 0670 and folders 0770, with an entry per user, `group::---` and `other::---` (`pu-mode-variants`, the share
  `pu-nosys`). The masks of the renderer stay as they are, with no effect where acl_xattr is loaded; the default ACL on
  every folder of the converted share is what keeps world write off. A folder made on the host after the conversion gets
  it from its parent by the kernel; one with a default ACL of its own keeps that one. (The variants of
  `pu-mode-variants` have the convert's default ACL removed, to show smb.conf alone.)
- **The recycle bin:** vfs_recycle makes `#recycle` and its folders as the user who deletes, with `recycle:directory_mode`
  (default 0700) and `recycle:subdir_mode`. Another user cannot move a file into them, and vfs_recycle then removes the
  file for good while the client sees a delete that worked (`pu-recycle-without-modes`: userb's `two.txt` is neither in
  the folder nor in `#recycle`); the same happens with a `#recycle` of before whose folders the group may not write.
  With `recycle:directory_mode = 0770` and `recycle:subdir_mode = 0770` (the renderer writes them for a `PerUser` share)
  and the folders of the `#recycle` of before converted, every deletion of usera and userb arrives (`pu-recycle`).
  - **The folders vfs_recycle makes** (`#recycle/sub/a-dir` for usera's deletion of `sub\a-dir\inner.txt`) belong to the
    user who deleted, in the group of the share's folder: `usera:myUser 770` (0770 of `recycle:directory_mode`, capped
    by the default ACL of the folder above, which they also get). They have no `security.NTACL`: recycle sits
    below acl_xattr in `vfs objects` and makes them past it. smbd shows them with the ACL it makes from the mode: the
    owner (usera's SID) FULL, the group `S-1-22-2-1000` `0x001201bf`, `S-1-5-18` FULL (`pu-recycle`). The files in them
    keep the NT ACL they had where they were deleted.
- **Restore** makes the missing folders as the owner of the share's folder, with the mode of the nearest folder that is
  there, and the restored item gets the rights of where it lands: the topmost folder that restore made, with all in it,
  or else the item itself, gets the ACL it inherits from its folder (`pu-restore`: `sub\deep` with the ACEs of `sub`,
  flags `0x13`; usera reads the restored file, and userc may not list the folder). That holds for a file deleted before
  the share ran per user, which has no `security.NTACL` in `#recycle`: `sub/gone.txt` restored has the ACEs of `sub`
  (flags `0x10`), and usera and userb read it while userc is refused. The restored item also gets what the conversion
  gives: `sub/gone.txt` comes back 0664 from 0644, and `sub\deep` is 0775 with the default ACL.
- **An apply above a folder with rights of its own keeps it:** `apply-acl --folder "" --keep sub` puts the share's rights
  on the share again and leaves `sub` and all in it as they were (`pu-apply-share-keep`; `acl.Read` of `sub` then still
  has the narrowed rights).
- **What smbd makes is no mixed state:** below a folder with rights it gives a new file or folder the inherited ACEs as
  its own (flags `0x0` and `0x3`, no inherited flag), the session user as NT owner, in version 3; `acl.Read` takes them
  as inherited, and after all of the above it reports no mixed state for the share and for `sub`.

## Alpine 3.22

- **Versions:** see the top; `apk search -x` of a name that does not exist (`wsdd2`) exits 0 on 3.22 (apk-tools 2) and
  1 on 3.24 (apk-tools 3).
- **testparm:** 3.22 prints `smb ports = 445` where 3.24 prints `server smb transports = 445` for the same line `smb
  ports = 445`; Samba 4.23 renamed the parameter and keeps the old name.
- **smbstatus JSON:** the `channels` of 3.22 have no `transport` key.
- **wsdd 0.8:** right after `rc-service samba restart`, `rc-service wsdd status` says `starting` (exit 8) instead of
  `started`.
- **samba-tool:** every subcommand fails (`ModuleNotFoundError: No module named 'cryptography'`).
- **avahi 0.8:** its `avahi-daemon.conf` (`avahi-daemon.conf-3.22`) has `enable-wide-area=yes` in `[wide-area]`, where
  0.9 has the commented `#enable-wide-area=no`, and lacks the commented `#host-name-from-machine-id=no` of `[server]`.
- Everything else, the gate included, is the same.

## Recapture

```sh
TEST_ALPINE_LOG=alpine.log make test-alpine
```

The log has a section per test and image (`== TestCapture on alpine:3.24`), and in it the blocks of the test, each
below its `=== <name> ===` line (`=== end ===` closes the last one; `=== statuses ===` holds the exit status of every
command, `=== blocks-without-final-newline ===` the blocks that lacked a final newline: none in this capture). One
block without its marker line, for example `smbstatus-json-open` (the file `smbstatus-open.json`):

```sh
awk -v test=TestCapture -v image=alpine:3.24 -v want=smbstatus-json-open '
  /^== / { sec = ($0 == "== " test " on " image); on = 0; next }
  sec && /^=== .+ ===$/ { on = ($0 == "=== " want " ==="); next }
  sec && on' alpine.log > smbstatus-open.json
```

For the files of one command, a first line `exit status: 0` is dropped; the files of several blocks keep the marker
lines:

| File | Test | Blocks |
|---|---|---|
| `packages.txt` | TestCapture | `versions`, `apk-search`, `apk-info-L-samba`, `apk-info-R-samba`, `apk-info-L-parts`, `tools`, `init-scripts` |
| `conf.d-samba`, `init.d-samba`, `smb.conf.default`, `testparm-sv-default.txt` | TestCapture | `conf.d-samba`, `init.d-samba`, `smb.conf-default`, `testparm-sv-default` |
| `paths.txt` | TestCapture | `testparm-paths-default`, `samba-dirs-before-start`, `databases`, `testparm-paths` |
| `services.txt` | TestCapture | `init-depend`, `start-*`, `services-status`, `apk-add-avahi-tools`, `avahi-browse-samba-first`, `samba-restart`, `wsdd-after-samba-restart`, `daemon-list-smbd`, `rc-update-add`, `samba-stop` |
| `users.txt` | TestCapture | `adduser-u1`, `accounts-u1`, `pdbedit-a`, `pdbedit-L`, `pdbedit-L-w`, `login-u1*`, `smbpasswd-*`, `pdbedit-x`, `pdbedit-a-again`, `pdbedit-a-exists`, `odd-password` |
| `pdbedit-L-v.txt` | TestCaptureACL | `acl-users` |
| `shares.txt`, `share-names.txt`, `hiding.txt`, `protocol.txt` | TestCapture | `testparm-s1` and `s1-*`; `share-names-testparm`; `hide-*`, `sharesec-*`, `share-acl-narrows`; `protocol-*` |
| `missing-disk.txt`, `recycle.txt`, `mac.txt` | TestCapture | `missing-disk-*`, `preexec-args`; `recycle-*`; `mac-*` |
| `smbstatus-*.json`, `smbstatus.txt` | TestCapture | `smbstatus-json-none`, `-idle`, `-open`; `smbstatus-help`, `smbstatus-json-none-status`, `smbstatus-text`, `smbstatus-b`, `smbstatus-L-json`, `smbstatus-clients` |
| `reload.txt`, `interfaces.txt`, `proc-net-*` | TestCapture | `reload*`; `sys-class-net`, `ip-addr`, `bind-*`, `hosts-allow-*`; `proc-net-if_inet6`, `proc-net-fib_trie` |
| `crond.txt`, `ssh.txt`, `xattr.txt`, `errors.txt` | TestCapture | `crond-*`; `sshd-start`, `apk-add-sshpass`, `ssh-password`, `ssh-batch`, `sshd-log`; `xattr-privilege`; `error-*` |
| `acl-methods.txt` | TestCaptureACL | `acl-start`, `acl-xattr-privilege`, `acl-synthesized`, `acl-stats-before`, `acl-a-*`, `error-acl-b-set-inheriting`, `acl-b-*`, `acl-c-*` |
| `acl-enforce.txt`, `acl-gate.txt` | TestCaptureACL | `acl-enforce-*`, `acl-owner-ace-*`, `acl-g-*`; `acl-gate`, `acl-recycle-*`, `acl-preexec-log`, `acl-parameters` |
| `acl-peruser.txt` | TestACLAgainstSamba | `pu-*` |
| `avahi-daemon.conf`, `avahi-daemon.conf-3.22` | TestDiscoveryInterfaces | `avahi-daemon.conf` (of alpine:3.24 and of alpine:3.22) |

The other blocks of the log (`smbd-b`, the statuses) are not committed; the facts above come from them too.
