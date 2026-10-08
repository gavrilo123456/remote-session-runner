# macOS launchd deployment

The Mac runs `runner-local` (Unix API, Router, and one runtime per configured
mailbox) and `runner-locald` (local execution authority) as `tomasz.walczuk` in
the GUI launchd domain. Commands use that account's OS permissions; a workspace
is not confinement.

## First install

From the repository root, run:

```sh
deploy/macos/install-launchagents.sh
```

When `config/mac.yaml` is absent, the first run creates it from
`mac.yaml.example` and exits without starting services. That V1 file is a
compatibility bootstrap for the legacy `default` root. Review it, provision
external owner-only secret files, then rerun the installer. The installer builds
`runner`, `runner-local`, and `runner-locald`, validates the LaunchAgent plists,
and bootstraps them with fixed executable/config paths, an owner-only umask,
and logs under the service root.

For an installation that starts services, the checkout must be clean `dev` at
the synchronized `origin/dev` revision. The installer embeds that full revision
in the binaries and verifies that the live `runner-local` response through
`local-api.sock` has the same `build_revision`. This verifies the process that
will accept mailbox work. `--version`, a file checksum, or a fresh `doctor`
process does not attest an already-running LaunchAgent. See the exact
[Mac revision-attestation command](../../docs/setup.md#attest-the-running-mac-revision).

## V2 multi-inbox candidate activation

`mac.v2.yaml.example` is the policy template for named inboxes and profiles. It
intentionally starts with only `linux-host`, so the checked-in template carries
no sandbox credentials. The active owner-only V2 policy was established by
P155/P157/P158, extended by P159 direct workspace ingress, and extended again
by P165/P166 private malformed-ingress diagnostics. It contains the separately
accepted `sandbox-host` and the accepted external `slidestud-io` mailbox.
Create a reviewed owner-only candidate at
`config/mac.next.yaml`, retain all existing registered roots, and run:

```sh
deploy/macos/install-launchagents.sh --config \
  "/Users/tomasz.walczuk/Library/Application Support/RemoteSessionRunner/config/mac.next.yaml"
```

The installed current V2 policy has:

| Inbox | Root | Default | Allowed contexts |
| --- | --- | --- | --- |
| `default` | `.../RemoteSessionRunner/mailbox` | `mac-local` | `mac-local`, `ubuntu-current` |
| `analytics` | `.../RemoteSessionRunner/mailboxes/analytics` | `mac-local` | `mac-local`, `ubuntu-current`, `ubuntu-sandbox` |
| `slidestud-io` | `/Users/tomasz.walczuk/projects/slidestud.io/tmp/mailbox-` | `ubuntu-sandbox` | `ubuntu-sandbox`, `mac-local`, `ubuntu-current` |

`ubuntu-sandbox` selects `sandbox-dev` / `remote/sandbox-host`. For mailbox
work, `analytics` permits that explicit override, while `slidestud-io` uses it
as the default and `default` retains `mac-local` and `ubuntu-current`. The
accepted `sandbox-poc` direct endpoint and its separate secret paths remain in
the owner-only active configuration. The P157 host result and P158 external
mailbox result are recorded in
[`040-implementation-evidence/P157-sandbox-host.md`](../../040-implementation-evidence/P157-sandbox-host.md)
and [`040-implementation-evidence/P158-slidestud-external-mailbox.md`](../../040-implementation-evidence/P158-slidestud-external-mailbox.md).
P159 direct-workspace ingress and P165/P166 diagnostics are recorded in
[`040-implementation-evidence/P159.md`](../../040-implementation-evidence/P159.md),
[`040-implementation-evidence/P165.md`](../../040-implementation-evidence/P165.md),
and [`040-implementation-evidence/P166.md`](../../040-implementation-evidence/P166.md).

The installer validates the candidate while the active configuration runs. It
first performs a descriptor-based, non-mutating check of every mailbox path,
then stops mailbox ingress and waits for its socket to disappear. Before
executor bootout, the staged `runner-locald preflight-restart` reads the
authority selected by the **active** `mac.yaml` in SQLite read-only mode, not
the candidate database setting. The guard runs even when launchd does not
report a loaded locald. It requires zero active session slots, active
command slots, queued commands, and resumable one-off jobs in
`creating_session`, `accepting_command`, `awaiting_command`, or
`closing_session`. It accepts the supported schema-24 or current schema without
migration.

A nonzero count, unreadable database, unsupported schema, or database-path
uncertainty refuses the refresh. A missing database is accepted only for a true
first local-executor install with no loaded old locald, prior locald binary,
LaunchAgent plist, socket, database, or SQLite sidecar. The guard does not
write health data, recover capacity, clean up work, or release a slot. The
installer then runs its final read-only retained-mailbox check. A schema-24
database is read as implicit `default` during that check; no migration or
registry write occurs before the activation boundary.

For the exceptional fully idle retained-lost repair, use only
`install-launchagents.sh --recover-stalled` as documented in
[`docs/setup.md`](../../docs/setup.md#approved-mac-offline-recovery-when-no-work-must-survive).
After the old Router and locald are proven inert, the installer installs the
stopped candidate binaries before recovery can migrate `local.db`. A refusal at
that handoff leaves both LaunchAgents stopped. Confirm that both labels and
their private sockets are absent, correct the complete recovery evidence, and
rerun the identical installer command; do not start an older binary or edit
SQLite.

At activation, the complete candidate root set is registered durably before a
missing external mailbox tree is created. Runner creates or verifies only the
configured root and its `inbox`, `outbox`, `events`, `acks`, and `diagnostics`
children. The database may migrate to the current schema before the staged candidate becomes active
`mac.yaml`. A V1 binary cannot safely reopen that namespaced schema. The
transition is not a single atomic transaction: before activation, a failure
restores the previous services; after activation begins, the installer retains
candidate state for repair/re-run and does not revive V1. It leaves the
separately supplied `mac.next.yaml` in place after success. Do not copy over
active `mac.yaml`.

A non-default mailbox root can be either
`<service-root>/mailboxes/<inbox-id>` or a clean absolute path outside the
service root. For an external root, every ancestor must already be a real
directory without group or other write access, and the immediate parent must
belong to `tomasz.walczuk`. Runner never changes those ancestors. The root and
its five children are owner-owned `0700`; symlinks, unsafe existing paths, and
missing parents fail closed. If the root is below another repository, exclude
its runtime directory locally from that repository's VCS view.

The mailbox registry is append-only for this PoC. Adding a root is supported;
removing, renaming, or moving an inbox needs an explicit later migration after
its work and artifacts are retired. P158 accepted mailbox ingress only;
`sandbox-host` passed its separate P157 host gate. A later remote host still
needs its own P157 gate even if its profile appears in a candidate.

## Stop and restart behavior

Unload the jobs with:

```sh
launchctl bootout "gui/$(id -u)" "$HOME/Library/LaunchAgents/com.remote-session-runner.local.plist"
launchctl bootout "gui/$(id -u)" "$HOME/Library/LaunchAgents/com.remote-session-runner.locald.plist"
```

On SIGTERM, each service stops new work, drains for eight seconds, uses normal
runtime cancellation/close if necessary, checks its committed audit tail, and
then closes streams and removes its socket. The shared cleanup stage is bounded
to five seconds; the 13-second total fits within each plist's 15-second
`ExitTimeOut`. Run `make test-p131-macos-shutdown` on the selected Mac account
to exercise the real LaunchAgent stop/restart path.

Do not restart the Mac Router merely to make an accepted one-off response look
newer. A restart withdraws any previously persisted active run projection until
the new Router completes a fresh identity-checked read-only authority query:
the shared Mac authority in `local.db` for Mac-local work (`runner-locald` is
the executor), or the selected `runnerd` for queued remote work. It does not
replay the mutation. The active status is not ACK eligible; wait for terminal
proof. See [mailbox recovery](../../docs/mailbox.md#recovery-and-retention).

See [docs/setup.md](../../docs/setup.md) for the full install and upgrade
runbook and [docs/mailbox.md](../../docs/mailbox.md) for native mailbox use.
