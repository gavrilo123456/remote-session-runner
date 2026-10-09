# Mac continuity backup and restore

This kit preserves the Mac side of Remote Session Runner across a disk wipe.
It makes one offline continuity archive containing the current durable Mac
authority, every configured mailbox, the mTLS and queued-SSH material, the
selected Go toolchain, and a Git bundle of the exact Runner source revision.

The archive contains private keys and certificates. Write it only to an
encrypted external volume under your control. Do not put it in Git, email, or
an unencrypted cloud folder.

## What the archive contains

The backup script discovers configured mailbox roots from the active
`mac.yaml`. In the current installation that includes these four roots:

```text
/Users/tomasz.walczuk/Library/Application Support/RemoteSessionRunner/mailbox
/Users/tomasz.walczuk/Library/Application Support/RemoteSessionRunner/mailboxes/analytics
/Users/tomasz.walczuk/projects/slidestud.io/tmp/mailbox-
/Users/tomasz.walczuk/projects/agents-work-dispatcher/tmp/mailbox-
```

It archives:

```text
RemoteSessionRunner/config/
RemoteSessionRunner/secrets/
RemoteSessionRunner/state/          # local.db and any WAL, SHM, or journal file
RemoteSessionRunner/mailbox/
RemoteSessionRunner/mailboxes/
RemoteSessionRunner/backups/
RemoteSessionRunner/toolchains/     # needed before the installer can rebuild binaries
the external mailbox roots discovered from mac.yaml
a Git bundle for the clean dev source revision
selected ~/.ssh material by default
```

Any configured mailbox that is a custom child of `RemoteSessionRunner/` is
captured separately and restored at its recorded path. The script refuses a
backup destination inside any configured mailbox root, so the archive staging
directory cannot accidentally be included in a mailbox copy.

The default SSH set is `config`, `known_hosts`, `gavrilo123456-github`, and
`dev.slidestud.io`, plus `remote-session-runner` when it exists. Use
`--include-ssh-directory` only when the SSH configuration uses `Include` or
you deliberately want every durable SSH configuration and key file preserved.
Runtime SSH sockets are always excluded. Use `--without-ssh` only when those
credentials are already stored and recoverable elsewhere.

For this Mac, a second private copy of the secret subset is prepared at:

```text
/Users/tomasz.walczuk/.secrets/remote-session-runner-recovery
```

It contains `mac.yaml`, the Runner `secrets/` directory, selected SSH files,
and a complete static `~/.ssh` copy. Runtime SSH-agent sockets are excluded
because they cannot survive a reboot. It is `0700`/`0600` owner-only material.
Copy this directory to the same encrypted external volume as the archive before
wiping the disk. It is a convenient second copy, not a replacement for the
continuity archive: it does not contain durable state, mailbox history, the
toolchain, or the source bundle.

It intentionally excludes generated binaries, sockets, temporary scripts,
workspaces, logs, and installed LaunchAgent plists. The installer rebuilds or
recreates those after restoration. A restored database cannot reattach a
process interrupted by the disk wipe; it retains its truthful durable records
and reconciliation state instead.

## Before the disk wipe

1. Pause every mailbox publisher and LLM that can write into the four mailbox
   roots. The script checks external mailbox trees for changes while copying,
   but it cannot prevent a writer from publishing after its final check.
2. Wait for normal work to finish. The script refuses if Mac Router metrics
   show active sessions, active commands, queued commands, or queued intents.
   It allows retained terminal artifacts and historical receipts to remain in
   mailbox storage, because those are part of the durable record.
3. Use an encrypted external volume. From the clean Runner `dev` checkout:

   ```sh
   cd /Users/tomasz.walczuk/projects/remote-session-runner
   deploy/backup/backup-macos-runner.sh \
     --destination /Volumes/EncryptedBackup/remote-session-runner \
     --include-ssh-directory
   ```

   The command stops `runner-local` first, confirms that `runner-locald` is
   quiescent, stops `runner-locald`, then writes a timestamped `.tar.gz` and
   matching `.sha256` file. It leaves both Mac Runner services stopped after a
   successful backup, which is the desired state before a wipe. If it fails at
   a proven safe boundary, it restarts the services that it had stopped. If a
   LaunchAgent bootout succeeds but the old process boundary cannot be proven,
   it deliberately leaves that service stopped and prints the recovery warning;
   do not start a replacement until the old process has exited.

   The active SSH configuration uses `Include`, so this Mac needs
   `--include-ssh-directory`. The archive retains directories and regular files
   in `~/.ssh`, while excluding only runtime Unix sockets such as SSH-agent
   sockets. Reload any agent identities after the restore. It refuses a backup
   if an active Include resolves outside `~/.ssh`, so that material is never
   silently omitted; move it into `~/.ssh` or preserve it separately first.

4. Verify the archive before disconnecting the backup volume:

   ```sh
   deploy/backup/verify-macos-runner-backup.sh \
     --archive /Volumes/EncryptedBackup/remote-session-runner/remote-session-runner-macos-YYYYMMDDTHHMMSSZ.tar.gz
   ```

Keep both the archive and its adjacent `.sha256` file.

## Restore after macOS is reinstalled

The account name must again be `tomasz.walczuk`. The active configuration uses
absolute paths under that account. Restore or clone the `slidestud.io` and
`agents-work-dispatcher` project checkouts first, so their `tmp/` parents exist
for the external mailbox roots.

The Git bundle in the archive is the authoritative source revision for this
restore. Verify the outer checksum before extracting anything, then extract
only enough of the archive to create the source checkout:

```sh
archive=/Volumes/EncryptedBackup/remote-session-runner/remote-session-runner-macos-YYYYMMDDTHHMMSSZ.tar.gz
recovery=$(mktemp -d /private/tmp/remote-session-runner-recovery.XXXXXX)

expected=$(/usr/bin/awk 'NR == 1 { print $1; exit }' "$archive.sha256")
actual=$(/usr/bin/shasum -a 256 "$archive" | /usr/bin/awk '{ print $1 }')
test "$actual" = "$expected"

tar -xzf "$archive" -C "$recovery" \
  source/remote-session-runner.bundle metadata/manifest.txt
source_origin=$(/usr/bin/sed -n 's/^source_origin=//p' "$recovery/metadata/manifest.txt")
test -n "$source_origin"
mkdir -p /Users/tomasz.walczuk/projects
git clone --branch dev "$recovery/source/remote-session-runner.bundle" \
  /Users/tomasz.walczuk/projects/remote-session-runner
git -C /Users/tomasz.walczuk/projects/remote-session-runner \
  remote set-url origin "$source_origin"
```

Cloning from the bundle retains the archived `origin/dev` reference needed by
the first installer check. `remote set-url` restores normal GitHub transport
without fetching. Do not fetch or pull until the first install and health
checks have succeeded.

Then restore the runtime material. This command refuses to overlay an existing
service root, an existing external mailbox root, or a loaded Runner service.

```sh
cd /Users/tomasz.walczuk/projects/remote-session-runner
deploy/backup/restore-macos-runner.sh \
  --archive "$archive" \
  --apply
```

It verifies the outer archive checksum and every member checksum again,
preflights every target, stages all runtime, mailbox, and SSH material, and
then publishes it. If a late publish step fails, it removes only paths claimed
by that restore attempt. Keep all mailbox publishers stopped during this
window. It restores recorded file modes but does **not** start Runner. Start it
only after the restore succeeds:

```sh
deploy/macos/install-launchagents.sh
```

The installer rebuilds `runner`, `runner-local`, and `runner-locald` with the
restored Go toolchain, recreates LaunchAgents and sockets, then starts both
services. It requires the restored checkout to be clean `dev` at the exact
bundle revision.

Finally, check both private Mac readiness endpoints before publishing work:

```sh
root='/Users/tomasz.walczuk/Library/Application Support/RemoteSessionRunner'
curl --silent --show-error --fail --unix-socket "$root/run/local-api.sock" http://runner/health/ready
curl --silent --show-error --fail --unix-socket "$root/run/locald.sock" http://locald/health/ready
```

Run a fresh harmless mailbox request only after those checks are ready. Do not
replay an old request merely because it survived in the restored inbox or
outbox.

## Scope boundary

This is a Mac continuity backup. The two Ubuntu hosts are not copied by this
kit; a Mac disk wipe leaves their Runner state and services in place. The
restored Mac needs the preserved mTLS, dispatcher SSH, and host-key material to
reconnect to them.
