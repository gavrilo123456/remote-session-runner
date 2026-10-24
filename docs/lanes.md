# Operating the existing Dev, QAS, and PRD lanes

This runbook covers the existing Mac and the existing `linux-host`,
`sandbox-host`, and `factory-sandbox` workers. Each machine has parallel PRD
(unsuffixed), Dev (`-dev`), and QAS (`-qas`) identities on its existing
account and filesystem. The three-lane deployment does not create hosts,
volumes, accounts, or a production upgrade.

For the normal **Dev → QAS → PRD** promotion flow, start with the short
[promotion guide](promotion-guide.md).

The source workflow commands are `build-release`, `promote-to`, and
`runner-release`. Use `build-release` to freeze and publish one release without
activation; use `promote-to ... --plan` to inspect full selected-lane scope
before any cohort mutation. The guide documents exact flags, resume, acceptance
prerequisites and rollback. PROMO source test results do not prove that any
installed lane accepted a release.

Catalog-managed releases use the
[Factory-style catalog](../040-improvments/070-factory-style-runner-releases/010-factory-style-runner-releases-detailed-design.md)
under `~/projects-runtime/runner-releases/` on each host. Each lane keeps its
own selector and existing configuration/data/mailbox/log/credential paths.
The [CAT implementation plan](../040-improvments/070-factory-style-runner-releases/015-factory-style-runner-releases-phased-implementation-plan.md)
and its evidence record implementation and acceptance. The build/install
sections below describe the earlier legacy layout; they are not the catalog
promotion procedure. Use the qualified catalog controller for a catalog-managed
installation. Do not copy binaries or change selectors manually.

## Fixed lane identities

| Machine | PRD | Dev | QAS | Data/configuration root |
| --- | --- | --- | --- | --- |
| Mac | `com.remote-session-runner.local` and `.locald` | `com.remote-session-runner.local-dev` and `.locald-dev` | `com.remote-session-runner.local-qas` and `.locald-qas` | `~/Library/Application Support/RemoteSessionRunner[-dev|-qas]` |
| Each Ubuntu worker | `runnerd.service` | `runnerd-dev.service` | `runnerd-qas.service` | `/home/ubuntu/.local/share/remote-session-runner[-dev|-qas]` |

The Mac uses `config/mac.yaml`, `state/local.db`, `run/local-api.sock`, and
`run/locald.sock` below each root. A worker uses `config/linux.yaml`,
`state/remote.db`, and `run/runnerd.sock`. Dev and QAS each keep their own
binary release, configuration, database/WAL/SHM, socket, mailbox, workspace,
logs, credentials, backups, and staging. PRD retains its existing paths and
external mailbox roots.

The Ubuntu direct HTTPS ports are PRD `8443`, Dev `8444`, and QAS `8445` on
the existing public host address. The queued bridge uses the existing SSH
listener on port `22`; each lane has its own forced-command key and socket.
The Mac is the client/control plane and does not add a public listener.

## Select the lane for ordinary work

Select the matching Mac CLI and complete Mac configuration together. The
selected configuration carries the immutable `release_lane`; do not edit it
to switch lanes. The environment/profile pair remains the one listed by that
configuration (for example, `factory-sandbox-dev` / `remote/factory-sandbox`)
even when QAS is selected.

```sh
# Mac — tomasz.walczuk — select Dev or QAS
lane=dev # set to qas for QAS
root="$HOME/Library/Application Support/RemoteSessionRunner-$lane"
RUNNER="$root/current/bin/runner"
CONFIG="$root/config/mac.yaml"

# Direct HTTPS route to factory-sandbox in the selected lane
"$RUNNER" --endpoint "factory-sandbox-poc-$lane" --config "$CONFIG" run \
  --environment factory-sandbox-dev \
  --target remote --profile factory-sandbox \
  -- 'printf "LANE_OK\\n" && id -un && hostname'

# Queued route to the same worker through the selected Mac lane
"$RUNNER" --endpoint local --config "$CONFIG" run \
  --environment factory-sandbox-dev \
  --target remote --profile factory-sandbox \
  -- 'printf "LANE_OK\\n" && id -un && hostname'
```

Use `linux-poc-$lane` with `linux-host` / `linux-host-dev`, or
`sandbox-poc-$lane` with `sandbox-host` / `sandbox-host-dev` for the other
workers. The direct endpoint and target profile must match. Use the selected
lane's config for both direct and queued work. For catalog-managed PRD, use
the unsuffixed `RemoteSessionRunner/current/bin/runner` with
`RemoteSessionRunner/config/mac.yaml`; the existing V2 environment names and
routes remain unchanged. The [CLI guide](user-guide.md) documents command,
session, event, cancellation, and mailbox operations.

Check each lane's readiness before using it:

```sh
# Mac — tomasz.walczuk — set root to the selected PRD, Dev, or QAS root
curl --silent --show-error --fail --unix-socket "$root/run/local-api.sock" \
  http://runner/health/ready
curl --silent --show-error --fail --unix-socket "$root/run/locald.sock" \
  http://runner/health/ready

# Target Ubuntu worker — ubuntu — set root to the selected PRD, Dev, or QAS root
curl --silent --show-error --fail --unix-socket "$root/run/runnerd.sock" \
  http://runner/health/ready
```

Readiness does not prove that retained capacity is free. Check the active
session/command, queue, intent, and mailbox metrics before a change. A selected
lane with uncertain or retained work must not be restarted or updated.

## Inspect and restart one Dev or QAS service

The ordinary `runner-lane status/start/stop/install/rollback` commands accept
only `dev` and `qas`. The separate `prd-*` maintenance commands are documented
in the [PRD runbook](../deploy/PRD_UPGRADE.md). Each route derives one
canonical host's service identity and has no arbitrary service-name argument.
Its `stop` and `install` paths verify selected-lane terminal state; use it
instead of manually stopping a Mac agent or systemd unit.

```sh
# Mac — tomasz.walczuk
cd /Users/tomasz.walczuk/projects/remote-session-runner
GO="/Users/tomasz.walczuk/Library/Application Support/RemoteSessionRunner/toolchains/go1.27.1/bin/go"
GOCACHE=/private/tmp/remote-session-runner-gocache \
GOMODCACHE=/private/tmp/remote-session-runner-gomodcache GOTOOLCHAIN=local \
  "$GO" run ./src/cmd/runner-lane status --host mac-control-plane --lane dev
GOCACHE=/private/tmp/remote-session-runner-gocache \
GOMODCACHE=/private/tmp/remote-session-runner-gomodcache GOTOOLCHAIN=local \
  "$GO" run ./src/cmd/runner-lane stop --host mac-control-plane --lane dev
GOCACHE=/private/tmp/remote-session-runner-gocache \
GOMODCACHE=/private/tmp/remote-session-runner-gomodcache GOTOOLCHAIN=local \
  "$GO" run ./src/cmd/runner-lane start --host mac-control-plane --lane dev

# Target Ubuntu worker — ubuntu — choose exactly one host and lane
cd /home/ubuntu/projects/remote-session-runner
GO=/home/ubuntu/.local/share/remote-session-runner/toolchains/go1.27.1/bin/go
"$GO" run ./src/cmd/runner-lane status --host sandbox-host --lane qas
"$GO" run ./src/cmd/runner-lane stop --host sandbox-host --lane qas
"$GO" run ./src/cmd/runner-lane start --host sandbox-host --lane qas
```

Run Ubuntu commands on the named target host, not through a Runner request.
`runner-lane stop` gracefully stops only the selected Dev/QAS service and
proves its stopped inventory is quiescent. `start` requests only that lane
from its service manager. Verify its readiness afterward and compare the other
lane and PRD service identities before declaring isolation. A start command
can return before the private health socket begins accepting requests; poll
readiness briefly before treating that as a startup failure.

## Build and install a Dev/QAS candidate

Build on the target machine from its clean, synchronized `dev` checkout.
Keep the candidate outside Git and record the source revision, host, toolchain,
manifest hash, and bundle hash. The candidate is immutable; rebuilding makes a
new candidate and requires repeating acceptance before promotion.

When building after an evidence-only commit has advanced `origin/dev`, create a
clean detached worktree at the frozen source revision and pass that exact SHA
with `--source-revision`. The pinned build guard requires that the commit is an
ancestor of `origin/dev` and that all changes after it are Markdown-only; the
ordinary build path continues to require `dev` exactly synchronized with
`origin/dev`.

```sh
# Target Ubuntu worker — ubuntu — example for sandbox-host Dev
cd /home/ubuntu/projects/remote-session-runner
GO=/home/ubuntu/.local/share/remote-session-runner/toolchains/go1.27.1/bin/go
candidate="/tmp/runner-lane-sandbox-host-dev-<source-sha>"
source_sha="<frozen-source-sha>"
"$GO" run ./src/cmd/runner-lane bundle-build \
  --host sandbox-host --lane dev --repo "$PWD" \
  --output-dir "$candidate" --go "$GO" --source-revision "$source_sha"
"$GO" run ./src/cmd/runner-lane bundle-stage \
  --host sandbox-host --lane dev --bundle-dir "$candidate"
"$GO" run ./src/cmd/runner-lane install \
  --host sandbox-host --lane dev --bundle-dir "$candidate"
```

`install` runs the selected-lane stop/quiescence guard, takes a consistent
configuration/database backup below that lane's `backups` directory, installs
only the validated host bundle, and leaves the service stopped. Its JSON result
reports the active and previous bundle hashes and the exact backup directory.
Start only the selected lane, then verify its readiness and bounded live
acceptance before sending work to it. Candidate construction, source delivery,
and installation details are recorded in the relevant `SIMPLE-*` evidence.

To roll back a Dev/QAS release, first prove that service has no active or
retained work. Use the exact backup directory returned by that install:

```sh
# Target Ubuntu worker — ubuntu — substitute the exact recorded backup path
cd /home/ubuntu/projects/remote-session-runner
GO=/home/ubuntu/.local/share/remote-session-runner/toolchains/go1.27.1/bin/go
"$GO" run ./src/cmd/runner-lane rollback \
  --host sandbox-host --lane dev --backup-dir \
  "/home/ubuntu/.local/share/remote-session-runner-dev/backups/<recorded-backup>"
"$GO" run ./src/cmd/runner-lane start --host sandbox-host --lane dev
```

Rollback restores the previous immutable executable bundle only. It does not
overwrite the lane's configuration or SQLite database. It refuses a missing
or incompatible previous bundle, changed activation pointer, or nonempty
restart inventory. Preserve later accepted work; do not restore old database
files, manually clear capacity, or replay requests to make rollback pass.
Use the analogous `--host` and `--lane` values on Mac or another worker.

## Stable PRD and production changes

Keep unsuffixed PRD configuration, database, mailbox, port and service identities
independent of Dev/QAS. Read the live installed revision, health and work
inventory before an operation; historical SIMPLE and bug evidence is not a
current-health substitute.

For catalog-managed PRD, use the supported CAT cohort procedure in the
[release design, section 5](../040-improvments/070-factory-style-runner-releases/010-factory-style-runner-releases-detailed-design.md#5-promotion-and-selected-lane-upgrade)
and its phased plan. The separate legacy `prd-*` procedures in
[PRD_UPGRADE.md](../deploy/PRD_UPGRADE.md) retain their own layout guards;
they are not substitutes for catalog promotion. Ordinary Dev/QAS
`--lane` commands remain closed to PRD. Normal production promotion accepts
the exact QAS-qualified per-host bytes through an approved package with consistent backup,
compatible rollback, affected-producer disposition and fresh selected-service
guards. Do not manually replace binaries or substitute a source-building script.

Legacy regular `bin` files and catalog selectors have different launch and
backup/rollback contracts. Determine the selected installation before choosing
a procedure; preserve runtime state. Prior incident results and retained-work
dispositions stay in their own evidence; no recovery or bug closure follows
from this runbook change.

## Resource limits

The active application policy permits at most two sessions and one running
command per Dev/QAS host and rotates each component log at 16 MiB with four
retained files. These controls do not reserve CPU, RAM, or disk and do not
provide hostile-code isolation. The existing Linux services have no
`CPUQuota`, `MemoryHigh`, or `MemoryMax`. Their current `TasksMax` values are
1035 on `linux-host`, 7000 on `sandbox-host`, and 396 on `factory-sandbox` for
each service; these are per-unit task ceilings, not CPU/memory lane
partitions. See the measured
[SIMPLE-07 closeout](../040-implementation-evidence/SIMPLE-07.md) and
[SIMPLE-01 inventory](../040-implementation-evidence/SIMPLE-01.md); remeasure
space and health before an update.
