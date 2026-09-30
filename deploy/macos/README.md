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

## V2 multi-inbox candidate activation

`mac.v2.yaml.example` is the policy template for named inboxes and profiles. It
intentionally starts with only `linux-host`, so the checked-in template carries
no sandbox credentials. The active owner-only P155/P157 policy also contains
the separately accepted `sandbox-host`. Create a reviewed owner-only candidate
at `config/mac.next.yaml`, retain all existing registered roots, and run:

```sh
deploy/macos/install-launchagents.sh --config \
  "/Users/tomasz.walczuk/Library/Application Support/RemoteSessionRunner/config/mac.next.yaml"
```

The installed P155/P157 policy has:

| Inbox | Root | Default | Allowed contexts |
| --- | --- | --- | --- |
| `default` | `.../RemoteSessionRunner/mailbox` | `mac-local` | `mac-local`, `ubuntu-current` |
| `analytics` | `.../RemoteSessionRunner/mailboxes/analytics` | `mac-local` | `mac-local`, `ubuntu-current`, `ubuntu-sandbox` |

`ubuntu-sandbox` selects `sandbox-dev` / `remote/sandbox-host`. For mailbox
work, `analytics` permits that explicit override and `default` retains
`mac-local` and `ubuntu-current`. The accepted `sandbox-poc` direct endpoint
and its separate secret paths remain in the owner-only active configuration.
The P157 host result is recorded in
[`040-implementation-evidence/P157-sandbox-host.md`](../../040-implementation-evidence/P157-sandbox-host.md).

The installer validates the candidate while the active configuration runs. It
then stops mailbox ingress, waits for sockets to disappear, and runs a final
read-only retained-mailbox check. A schema-24 database is read as implicit
`default` during that check; no migration or registry write occurs before the
activation boundary.

At activation, the candidate root set is registered and the database may
migrate to schema 27 before the staged candidate becomes active `mac.yaml`. A
V1 binary cannot safely reopen that namespaced schema. The transition is not a
single atomic transaction: before activation, a failure restores the previous
services; after activation begins, the installer retains candidate state for
repair/re-run and does not revive V1. It leaves the separately supplied
`mac.next.yaml` in place after success. Do not copy over active `mac.yaml`.

The mailbox registry is append-only for this PoC. Adding a root is supported;
removing, renaming, or moving an inbox needs an explicit later migration after
its work and artifacts are retired. `sandbox-host` passed its separate P157
host gate. A later remote host still needs its own P157 gate even if its profile
appears in a candidate.

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

See [docs/setup.md](../../docs/setup.md) for the full install and upgrade
runbook and [docs/mailbox.md](../../docs/mailbox.md) for native mailbox use.
