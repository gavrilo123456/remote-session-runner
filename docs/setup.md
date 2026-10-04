# Setup and upgrade runbook

Use this runbook to install or refresh the controlled PoC. Versioned source,
tests, deployment files, and documentation are changed only in the Mac
checkout. Commit and push the Mac work first; then fast-forward the clean
Ubuntu checkout before any Ubuntu build, installation, or test. Runtime
configuration, credentials, logs, databases, and output stay outside tracked
source.

## Before you start

| Requirement | Mac | `linux-host` | `sandbox-host` |
| --- | --- | --- | --- |
| Account | `tomasz.walczuk` | `ubuntu` | `ubuntu` |
| Checkout | `/Users/tomasz.walczuk/projects/remote-session-runner` | `/home/ubuntu/projects/remote-session-runner` | `/home/ubuntu/projects/remote-session-runner` |
| Go toolchain | `.../RemoteSessionRunner/toolchains/go1.27.1/bin/go` | `.../remote-session-runner/toolchains/go1.27.1/bin/go` | `.../remote-session-runner/toolchains/go1.27.1/bin/go` |
| Service manager | GUI launchd | systemd with noninteractive `sudo` for unit installation | systemd with noninteractive `sudo` for unit installation |
| Direct bind and public endpoint | — | `10.0.0.200:8443`; `https://129.151.232.40:8443` | `10.0.0.14:8443`; `https://132.226.205.205:8443` |
| Direct route materials | Client CA, per-profile certificate, and private key | Server certificate/key, trusted client CA, principal map | Server certificate/key, trusted client CA, principal map |

The two accepted hosts have independent state, service configuration, mTLS
leaves, queued bridge authorization, and host-key pins. Keep their materials
separate. Every additional profile requires its own P157 gate.

```sh
# Mac — tomasz.walczuk
"/Users/tomasz.walczuk/Library/Application Support/RemoteSessionRunner/toolchains/go1.27.1/bin/go" version

# Accepted Ubuntu host — ubuntu
"/home/ubuntu/.local/share/remote-session-runner/toolchains/go1.27.1/bin/go" version
```

Expected platform suffixes are `darwin/arm64`, `linux/amd64` on `linux-host`,
and `linux/arm64` on `sandbox-host`. P157 supports only native Linux `amd64`
(`x86_64`) and `arm64` (`aarch64`) toolchains; it exact-matches the host
architecture rather than accepting a cross-compiled toolchain. The deployment
scripts do not download Go.

## 1. Synchronize a versioned revision

Perform this after reviewing and committing the intended change.

### Mac — `tomasz.walczuk`

```sh
cd /Users/tomasz.walczuk/projects/remote-session-runner
git status --short --branch
git -c core.sshCommand='ssh -i /Users/tomasz.walczuk/.ssh/gavrilo123456-github -o IdentitiesOnly=yes -o BatchMode=yes -o StrictHostKeyChecking=yes' push origin dev
git rev-parse HEAD
```

### Each accepted Ubuntu host — `ubuntu`

Only pull into a clean `dev` checkout. Do not edit tracked Runner files on
Ubuntu or copy source there.

```sh
cd /home/ubuntu/projects/remote-session-runner
git status --short --branch
git -c core.sshCommand='ssh -i /home/ubuntu/.ssh/gavrilo123456-github -o IdentitiesOnly=yes -o BatchMode=yes -o StrictHostKeyChecking=yes' pull --ff-only origin dev
git rev-parse HEAD
```

Compare the resulting commit IDs. If either checkout is dirty or the pull
cannot fast-forward, stop at that Git state. Do not reset, force-push, or run a
host deployment against an unpushed revision.

## 2. First Mac installation

### Mac — `tomasz.walczuk`

The first installer invocation creates the V1 compatibility config and stops:

```sh
cd /Users/tomasz.walczuk/projects/remote-session-runner
deploy/macos/install-launchagents.sh
```

Review the created owner-only file:

```text
/Users/tomasz.walczuk/Library/Application Support/RemoteSessionRunner/config/mac.yaml
```

Before the second invocation, provision these regular owner-only files beneath
the Mac service root. Do not print their contents:

```text
secrets/poc-ca.pem
secrets/direct-client.pem
secrets/direct-client.key
secrets/dispatcher_ed25519
secrets/ssh_known_hosts
```

Set config and secret files to mode `0600`, then install:

```sh
# Mac — tomasz.walczuk
cd /Users/tomasz.walczuk/projects/remote-session-runner
deploy/macos/install-launchagents.sh
```

The installer builds `runner`, `runner-local`, and `runner-locald`, installs
two LaunchAgents, and starts them in the current GUI user domain. Use a quiet
window because service shutdown first drains and then cancels or closes bounded
remaining work.

After it stops mailbox ingress and before executor bootout, the installer runs
the staged `runner-locald preflight-restart` command against the **active**
`mac.yaml` and its active `local.db`, not the candidate configuration. The
guard runs even when launchd does not report a loaded locald, because durable
work can outlive a stopped executor. The
SQLite read-only gate requires zero active session slots, active command slots,
queued commands, and resumable one-off jobs in `creating_session`,
`accepting_command`, `awaiting_command`, or `closing_session`. It accepts the
supported pre-upgrade schema-24 or current schema without migrating either.

Any nonzero count, unreadable database, unsupported schema, or path uncertainty
blocks the refresh before `runner-locald` is stopped. A missing database is
allowed only for a true first local-executor install: no loaded old locald,
previous locald binary, LaunchAgent plist, socket, database, or SQLite sidecar
may exist. The preflight does not call `doctor`, write a health record, recover
capacity, clean up work, or release a slot. It protects queued local work from
an installer refresh; it is not a recovery procedure.

### Approved Mac offline recovery when no work must survive

`--recover-stalled` is an installer-only exception for a fully idle Mac
authority whose complete retained capacity consists of explicitly named,
terminal `lost` session/command pairs. It must not be used for a queued,
running, unknown, or otherwise preserved request.

From the authoritative Mac checkout, give every retained pair exactly once:

```sh
# Mac — tomasz.walczuk
cd /Users/tomasz.walczuk/projects/remote-session-runner
deploy/macos/install-launchagents.sh --recover-stalled \
  --lost-pair 'sess-EXACT-LOST-1:cmd-EXACT-LOST-1' \
  --lost-pair 'sess-EXACT-LOST-2:cmd-EXACT-LOST-2'
```

The installer builds and stages the candidate first, stops the Router and
local executor in order, and proves the old processes are exited or inert
before the staged shared recovery is allowed to touch `local.db`. That recovery
requires an exact idle inventory and selected runtime ownership proof; it
never executes or replays a stored script. Its postflight must show zero active
session slots, command slots, running commands, live reservations, and
nonterminal jobs before the ordinary LaunchAgent restart begins.

If capacity was released but an owner-only marker/workspace finalization remains
pending, retry the same installer command only with every pending pair named
again and only after the fresh idle inventory passes. That retry finalizes the
retained cleanup; it does not signal a runtime or replay a script. Any missing,
extra, or unrelated pending pair is refused.

Do not call `launchctl` directly, invoke `runner-locald recover-stalled` via
`go run` or the installed binary, edit/copy SQLite, manually release capacity,
or signal a recorded child process. If the installer refuses any check, leave
the state unchanged and investigate. After a successful restart, attest the
installed revision and use a new harmless mailbox request for live acceptance;
do not replay historical lost work. See the [operations recovery
boundary](operations.md#explicit-mac-offline-recovery-for-a-fully-idle-retained-lost-set)
and [BUG-014](../050-bugs/014-idle-mac-retained-lost-capacity-recovery.md) for
the full constraints and verification record.

Verify both services:

```sh
# Mac — tomasz.walczuk
root='/Users/tomasz.walczuk/Library/Application Support/RemoteSessionRunner'
launchctl print "gui/$(id -u)/com.remote-session-runner.local"
launchctl print "gui/$(id -u)/com.remote-session-runner.locald"
curl --silent --show-error --fail --unix-socket "$root/run/local-api.sock" http://runner/health/ready
curl --silent --show-error --fail --unix-socket "$root/run/locald.sock" http://runner/health/ready
```

### Attest the running Mac revision

The installer requires a clean `dev` checkout whose `HEAD` equals `origin/dev`,
embeds that full 40-character revision in the installed binaries, and verifies
the live Router after it starts. Keep the installer result and run this
read-only confirmation whenever a stale LaunchAgent is possible:

```sh
# Mac — tomasz.walczuk
repo='/Users/tomasz.walczuk/projects/remote-session-runner'
root='/Users/tomasz.walczuk/Library/Application Support/RemoteSessionRunner'
expected="$(git -C "$repo" rev-parse HEAD)"
test "$(git -C "$repo" branch --show-current)" = dev
test -z "$(git -C "$repo" status --porcelain)"
test "$expected" = "$(git -C "$repo" rev-parse origin/dev)"
actual="$(
  curl --silent --show-error --fail \
    --unix-socket "$root/run/local-api.sock" \
    http://runner/health/ready |
    /usr/bin/python3 -c 'import json, sys; print(json.load(sys.stdin).get("build_revision", ""))'
)"
test "$actual" = "$expected"
printf 'runner-local live build_revision=%s\n' "$actual"
```

This reads the process that owns `local-api.sock`; it is the required running
service provenance check. `runner-local --version`, a file checksum, or a
fresh `doctor` invocation does not identify that existing LaunchAgent. A
`build_revision` of `unattested` is not valid current-source evidence.

## 3. Upgrade to version 2 or add an inbox

### Mac — `tomasz.walczuk`

Use a reviewed candidate, not an in-place edit of the active config. Create
one only when it does not already exist:

```sh
candidate='/Users/tomasz.walczuk/Library/Application Support/RemoteSessionRunner/config/mac.next.yaml'
if test -e "$candidate"; then
  printf '%s\n' 'Candidate already exists; review it instead of overwriting it.'
else
  cp deploy/macos/mac.v2.yaml.example "$candidate"
  chmod 600 "$candidate"
fi
```

Edit the candidate as the owner to include the complete V2 policy. The current
V2 policy, accepted through P155/P157/P158 and extended by P159/P165/P166,
contains the legacy `default` root, the extra `analytics` root, the external
`slidestud-io` root at
`/Users/tomasz.walczuk/projects/slidestud.io/tmp/mailbox-`, and the accepted
`ubuntu-sandbox` context; see
[configuration](configuration.md#version-2-installed-multi-inbox-policy). Keep
every existing registered inbox ID/root unchanged. Do not write a candidate over
`config/mac.yaml`.

For an external non-default mailbox root, choose a clean absolute path outside
the Runner service root. Its ancestors must already be real directories without
group or other write access, and its immediate parent must belong to
`tomasz.walczuk`. Do not create or chmod those ancestors for Runner. If the root
is under another checkout, add a local VCS exclude for its runtime directory.
The installer creates the root and its five mailbox children only after the
candidate is durable.

Activate the candidate:

```sh
# Mac — tomasz.walczuk
cd /Users/tomasz.walczuk/projects/remote-session-runner
deploy/macos/install-launchagents.sh --config \
  "/Users/tomasz.walczuk/Library/Application Support/RemoteSessionRunner/config/mac.next.yaml"
```

The upgrade sequence is deliberate:

1. validate the candidate and perform a descriptor-based, non-mutating
   mailbox-path preflight while the old ingress is live;
2. quiesce the services and perform a final **read-only** retained-mailbox
   check;
3. enter the no-rollback activation boundary and register the complete
   candidate mailbox set durably;
4. create or verify only the candidate roots and their `inbox`, `outbox`,
   `events`, `acks`, and `diagnostics` children, hand off `mac.yaml`, and
   start the V2
   services.

For a schema-24 database, the final pre-boundary check treats retained work as
implicit `default` without writing it. The post-boundary activation can migrate
to the current schema and record the inbox registry. A V1 binary cannot safely reopen
that namespaced database. Before the boundary, a failed candidate restores the
prior services. After it, the staged candidate is retained for repair/re-run;
do not attempt to revive V1. The independently reviewed `mac.next.yaml`
remains after a successful install while a staged copy becomes active
`mac.yaml`.

Verify every configured mailbox tree and the running service health before
publishing new work. Use the native mailbox-client integration in [the mailbox
guide](mailbox.md) when possible. A workspace integration may instead publish
the documented exact-`0644` JSON-plus-marker pairs inside a configured `0700`
mailbox tree; it does not inherit the native publisher's durability guarantees.

## 4. Install or refresh an accepted Ubuntu service

### Target Ubuntu host — `ubuntu`

Run this on the selected host only after its own owner-only configuration and
the matching clean `dev` checkout are ready. `linux-host` uses
`10.0.0.200:8443`; `sandbox-host` uses `10.0.0.14:8443`.

Before first installation, provision these regular owner-only `0600` files:

```text
/home/ubuntu/.local/share/remote-session-runner/config/linux.yaml
/home/ubuntu/.local/share/remote-session-runner/config/client-principals.yaml
/home/ubuntu/.local/share/remote-session-runner/secrets/server.pem
/home/ubuntu/.local/share/remote-session-runner/secrets/server.key
/home/ubuntu/.local/share/remote-session-runner/secrets/client-ca.pem
```

Install from the synchronized checkout:

```sh
# Target Ubuntu host — ubuntu
cd /home/ubuntu/projects/remote-session-runner
deploy/linux/install-systemd-service.sh
sudo systemctl status runnerd.service --no-pager
```

On an active service, the installer runs the checked-in zero-active-work test
before build and immediately before restart. It refuses deployment if it sees
sessions, commands, unreleased slots, or unfinished jobs. This is a
point-in-time maintenance gate; normal graceful shutdown gives an honest
outcome to work accepted after the check.

Verify the private service and selected listener:

```sh
# Target Ubuntu host — ubuntu
root='/home/ubuntu/.local/share/remote-session-runner'
curl --silent --show-error --fail --unix-socket "$root/run/runnerd.sock" http://runner/health/ready
# Set this for the selected profile before running the check:
expected_bind='10.0.0.200:8443' # linux-host
# expected_bind='10.0.0.14:8443' # sandbox-host
ss -lntH | grep "$expected_bind"
```

### Attest the running Ubuntu revision

The Linux installer applies the same clean-`dev`, embedded-revision, live
health contract. Confirm that the socket belongs to the revision you pushed
from the Mac:

```sh
# Target Ubuntu host — ubuntu
repo='/home/ubuntu/projects/remote-session-runner'
root='/home/ubuntu/.local/share/remote-session-runner'
expected="$(git -C "$repo" rev-parse HEAD)"
test "$(git -C "$repo" branch --show-current)" = dev
test -z "$(git -C "$repo" status --porcelain)"
test "$expected" = "$(git -C "$repo" rev-parse origin/dev)"
actual="$(
  curl --silent --show-error --fail \
    --unix-socket "$root/run/runnerd.sock" \
    http://runner/health/ready |
    /usr/bin/python3 -c 'import json, sys; print(json.load(sys.stdin).get("build_revision", ""))'
)"
test "$actual" = "$expected"
printf 'runnerd live build_revision=%s\n' "$actual"
```

`runnerd --version`, bridge status, and a separately launched `doctor` do not
prove the running systemd process revision. A matching live `build_revision`
proves only revision identity; it does not replace a direct route or mailbox
end-to-end test. B008-P6 remains unrun until its own fresh harmless request,
terminal outbox, event, ACK, and zero-work gates are recorded.

## 5. Verify the direct Runner application route

### Mac — `tomasz.walczuk`

```sh
root='/Users/tomasz.walczuk/Library/Application Support/RemoteSessionRunner'
curl --silent --show-error --fail --max-time 15 \
  --cacert "$root/secrets/poc-ca.pem" \
  --cert "$root/secrets/direct-client.pem" \
  --key "$root/secrets/direct-client.key" \
  https://129.151.232.40:8443/health/ready
```

A successful result proves the `linux-host` Runner readiness endpoint, mTLS
identity, and public path. The earlier temporary TLS probe proved transport
only. Neither result proves queued bridge routing or mailbox delivery.

For the separately accepted `sandbox-host`, use its dedicated client leaf and
endpoint:

```sh
# Mac — tomasz.walczuk
root='/Users/tomasz.walczuk/Library/Application Support/RemoteSessionRunner'
curl --silent --show-error --fail --max-time 15 \
  --cacert "$root/secrets/poc-ca.pem" \
  --cert "$root/secrets/sandbox-direct-client.pem" \
  --key "$root/secrets/sandbox-direct-client.key" \
  https://132.226.205.205:8443/health/ready
```

This is a Runner application readiness check for `sandbox-poc`, not the
earlier temporary transport probe. It does not prove the queued bridge or
mailbox delivery.

## 6. Enable or refresh a queued bridge

The queued route is a separate, permanent restricted SSH bridge. It is required
for `runner --endpoint local` remote work and mailbox remote overrides. It is
not a general SSH shell and is independent of direct mTLS. Each accepted
remote profile has its own bridge authorization and dispatcher key.

### Target Ubuntu host — `ubuntu`: verify or refresh

```sh
cd /home/ubuntu/projects/remote-session-runner
deploy/ssh/install-queued-bridge.sh status
```

The status command is read-only with respect to bridge authorization and
persistent configuration. It checks the selected account, active service,
private socket, path modes, pinned dispatcher identity, exact restricted
`authorized_keys` entry, controller map, wrapper, and source revision. A
source checkout advance must be followed by the normal Linux deployment so the
bridge binary and manifest match the checked-out source revision.

For a first installation, follow the bridge guide's public-key streaming
procedure exactly:

```text
deploy/ssh/install-queued-bridge.sh enable --dispatcher-public-key-stdin
```

It accepts only the dispatcher **public** key, records a constrained forced
command, and never prints or transfers the private key. See
[deploy/ssh/README.md](../deploy/ssh/README.md) for the exact controlled
procedure.

### Mac — `tomasz.walczuk`: route smoke test for `linux-host`

```sh
runner='/Users/tomasz.walczuk/Library/Application Support/RemoteSessionRunner/bin/runner'
"$runner" --endpoint local run \
  --environment linux-dev \
  --target remote \
  --profile linux-host \
  -- 'id -un && hostname'
```

The output must identify `ubuntu`. This is a queued route check. It is separate
from the direct mTLS health check.

For `sandbox-host`, use the profile-specific queued context:

```sh
# Mac — tomasz.walczuk
runner='/Users/tomasz.walczuk/Library/Application Support/RemoteSessionRunner/bin/runner'
"$runner" --endpoint local run \
  --environment sandbox-dev \
  --target remote \
  --profile sandbox-host \
  -- 'id -un && hostname && uname -m'
```

The output must identify `ubuntu`, `oracle-gustaw-janecki-ubuntu-flex-02`, and
`aarch64`. This CLI command directly selects the queued `sandbox-host` route.
For mailbox work, `analytics` permits the `ubuntu-sandbox` override;
`slidestud-io` defaults to it; and `default` retains `mac-local` and
`ubuntu-current`.

## 7. Onboard each additional remote host (P157)

`sandbox-host` passed P157 on 2026-09-30 as the separately accepted ARM64
Ubuntu host for `sandbox-dev` / `remote/sandbox-host`. Its direct endpoint is
`sandbox-poc` at `https://132.226.205.205:8443`, and its queued context is
`ubuntu-sandbox`. See the [P157 evidence record](../040-implementation-evidence/P157-sandbox-host.md).

A new SSH alias, endpoint, or config profile is only a candidate. A reviewed
candidate activation and one controlled safe request may be part of P157; they
do not make the host generally available until the gate passes.

For each new profile, complete a separate P157 evidence record:

1. create independent owner-only Linux configuration, state root, mTLS
   certificate/principal map or pinned bridge materials, and service;
2. add its exact environment, target profile, and `remote_hosts` route to a
   reviewed Mac V2 candidate. Add it to a mailbox allow-list only when it has
   a queued bridge; a direct-only profile is validated by its direct endpoint
   and CLI rather than a mailbox request;
3. activate that reviewed candidate for the controlled P157 test, and
   fast-forward the exact host's clean checkout before installing its service
   from that checkout;
4. prove `ubuntu`, the expected listener or restricted bridge, host-key pin,
   per-profile route health, and a safe end-to-end request selecting that exact
   target; and
5. record the evidence, including the target profile, without printing private
   keys or treating another host's acceptance as evidence.

For a new Linux host, first install the selected native Go 1.27.1 toolchain
outside Git and verify its exact `go version` platform suffix. The service and
queued-bridge installers accept only `linux/amd64` on `x86_64`/`amd64` or
`linux/arm64` on `aarch64`/`arm64`. They reuse the selected `ubuntu` account's
normal Go build and module caches at `/home/ubuntu/.cache/go-build` and
`/home/ubuntu/go/pkg/mod`; they do not create or delete a per-invocation
module cache. The service installer preserves the immediate
zero-active-work-to-restart boundary for an active service. Neither installer
downloads a toolchain or deletes shared Go caches.

A host that is unavailable or has not been attempted is `NOT RUN`. A required
gate that runs and fails is `FAIL`: stop, preserve the exact evidence, and do
not mark the profile available. It must never reuse an accepted host's
certificates, state database, bridge authorization, or proof.

For normal use after setup, follow the [CLI guide](user-guide.md) or the
[mailbox guide](mailbox.md). The current accepted status is indexed in
[current-host evidence](current-host-evidence.md).
