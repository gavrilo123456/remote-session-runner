# LANES-L2-07 — Linux slice, device, project-quota, cache and log controls

**State:** BLOCKED — source S is implemented and the Mac source gates pass;
the required F acceptance on all three Ubuntu hosts is not run. There is no
L2-07 evidence closeout E and no L2-07 acceptance claim.

## Unit boundary and predecessor

- Ordered predecessor: L2-06 evidence closeout E
  `a59058a17aace95a3b0b23d4f0dadf9b20607f78`.
- Pre-unit Mac HEAD: `a59058a17aace95a3b0b23d4f0dadf9b20607f78`.
- L2-06 evidence at that HEAD: Git blob
  `42e04f613dddd7c94007f2716a318c19f7e8238e`.
- Design, phased plan, handbook and ledger blobs read at pre-unit HEAD:
  `f7404c2d5a0b35706f3a9e867991bffcb6171d93`,
  `082e73ac62a7815c3ed15778eb8162d81a43c347`,
  `d04a659df5c5834725ea05b9b047f4ad296b21bc`, and
  `d8c37ff086ff42d298035ee2f710320c37d9e32f` respectively.
- B021/B024 remain **BLOCKED** with their retained evidence unchanged.
  BUG-022/023 baseline units remain deferred. No mailbox publication or ACK,
  service, storage, boot, firewall, credential, or production Runner change was
  made.

## Source work

The Mac source now carries Linux device measurements with the ext4 UUID,
kernel major/minor, fixed mountpoint, byte and inode capacity/use, ext4 project
feature, and active project-quota mount state. The live observation must match
the measurement exactly. Linux resource envelopes bind bandwidth and IOPS
limits, and control-plan generation checks the full PRD-floor plus Dev/QAS and
management envelope against the selected device's measured throughput and IOPS
capacity. It accepts only fresh Dev, QAS, or host management policies bound to
a measured quota device; PRD is refused.

The lane installer renders deterministic systemd slice resource drop-ins and
service scope/mount dependencies. Only the Dev/QAS runner and queued-SSH
services receive delegated controllers. The management services receive fixed
slice and mount dependencies without controller delegation. Per-device I/O
uses the measured UUID path; the measurement retains the major/minor so a
later host preflight can compare the UUID path with the observed kernel device
number.

The quota proposal emits the seven fixed project entries and measured UUID
mount lines from PREP-00. It checks the 96 GiB data volume, the separate 20 GiB
backup volume, measured use against the project budget, and the 30% byte and
inode reserve. Project-map files are fragments for conflict-checked
installation; no quota limits were applied. It does not create paths, copy
caches, rewrite `/etc/fstab`, run `mkfs`, or mutate a host.

Policy and installer tests use synthetic UUIDs, major/minor identities and
capacity values in memory. They cover Dev/QAS and management mapping, exact
resource renderings, PRD refusal, changed/inactive devices, insufficient
volume size/inodes, use above quotas, and the 30% reserve. These fixtures do not
claim host measurements or F acceptance.

## Guarded fixture F runner source extension

The source now adds `make test-lanes-controls` and its `test-lanes-controls`
command for L2-07/L2-08. L2-07 requires the host-bound, owner-only manifest
outside Git and its exact owner-approved SHA-256. Validation binds the unit,
Ubuntu host, short approval expiry, fixed disposable paths, test-identity token,
OCI region/volume/attachment IDs, observed device path/serial, filesystem UUID,
major/minor, capacity and closed project/inode budgets. The live preflight
rejects symlinked paths, the root-device ancestry, wrong mounts or quota mode,
changed device identity, missing unified-v2 controllers, and a missing or
unbounded `system.slice` scope before any quota probe runs.

The runner places its F subprocess in a unique transient `system.slice` scope
with bounded CPU, memory, tasks and writes to only the two manifest devices.
The listener/session/wrapper/bridge workers use a fixture identity token,
receive no operator/provider environment, bind loopback only, and must appear
in that exact scope. The F procedure tests each declared byte/inode project
limit, bounded log capture, and the 30% reserve. It performs no provisioning,
format, mount, unmount, service or persistent cgroup operation. The Go test and
workers remain under the existing unprivileged `ubuntu` account; the token does
not create an OS user.

This source extension and its hermetic regressions do not pass the L2-07 F
gate. All three fixture volumes, the Linux-host cgroup-v2 maintenance/reboot,
factory-sandbox `io` propagation approval, and fresh device performance/UUID
facts are still absent.

The registered F Go test subprocess uses `data/go-build` for its build cache
and temporary files and `data/go-modules` for its module cache. The repository build command keeps
the normal Go toolchain cache; no shared-cache purge/copy or production cache
quota change is part of this unit. F-generated cache entries remain on the
disposable fixture devices for separately approved cleanup.

## Mac source gates

Execution machine: `mac-control-plane` source checkout, branch `dev`.
Toolchain: the Makefile-pinned Go 1.27.1 under
`/Users/tomasz.walczuk/Library/Application Support/RemoteSessionRunner/toolchains/`.

| Command | Result |
| --- | --- |
| Focused `go test ./src/internal/lanecapacity ./src/internal/laneinstall -count=1` | PASS |
| `make check build` | PASS; all Go tests, `go vet ./...`, and `go build ./src/cmd/...` exit 0 |
| `git diff --check` | PASS |
| `make test-lanes UNIT=LANES-L2-07 LANES_TIER=H` | Not an acceptance test: dispatch correctly rejects H because this unit's handbook gate is F. The H source tests ran under the full Go suite. |

The first full suite attempt inside the default sandbox failed because the
unrelated read-only process-state test
`TestBUG020MacShellExitWithInheritedControlWriterSettlesOnce` could not execute
`/bin/ps` (`operation not permitted`). Rerunning `make check build` with the
already authorized local test execution available outside that sandbox passed.
This was an execution-sandbox restriction, not an L2-07 source failure.

## F blockers — all three Ubuntu hosts

The required L2-07 F tier remains NOT RUN. PREP-00 evidence
[`LANES-PREP-00.md`](LANES-PREP-00.md) records that no disposable A01 fixtures
are provisioned. Each Ubuntu host needs an approved, separate 96 GiB ext4
`project,quota` data device mounted with `prjquota`, plus an independent backup
device of at least 20 GiB. The new devices must be matched to their OCI volume
identities, and their throughput/IOPS capabilities must be supplied before
numerical live caps can be accepted. The exact UUIDs, major/minor values and
host performance data do not exist yet.

Host-specific blockers remain distinct:

- `linux-host` currently has a hybrid cgroup hierarchy. PREP-00 records active
  Docker/containerd consumers; compatibility validation, a separately
  approved boot change/reboot, and console/out-of-band recovery are prerequisites
  to the real cgroup-v2 acceptance. No GRUB or service change was made.
- `factory-sandbox` is cgroup-v2, but `io` does not currently propagate through
  `system.slice`. The exact new-scope configuration and its impact on sibling
  services require the recorded review/approval before the host test.
- `sandbox-host` remains cgroup-v2. Its existing unsuffixed PRD service's empty
  `io.max` is not a lane failure and was not changed; F must prove caps only on
  the new management, Dev and QAS scopes.

No failure is waived. F must run against the exact S revision on each clean
Ubuntu checkout, proving actual listener/session/wrapper/bridge membership,
CPU, memory, task and per-device I/O bounds, byte/inode/cache/log saturation,
and unchanged PRD behavior. It is not replaced by static rendering or the
Mac's synthetic fixtures.

## Source delivery and synchronized checks

Source S is `6f0db6d0aa831fc09162bf32ddd3cd45e6627e3e`, committed on Mac `dev`
as `phase(LANES-L2-07): add measured Linux resource plans`. The primary Gitea
`origin/dev` and automatic GitHub mirror `github/dev` both resolve to that
exact SHA. Mac `dev` is clean at S; its local `origin/dev` tracking ref remained
one commit behind after the successful push, so the remote refs were verified
directly rather than treating that stale local tracking ref as the primary
push result.

Before pulling, `linux-host`, `sandbox-host`, and `factory-sandbox` were each
clean on `dev` at predecessor `a59058a17aace95a3b0b23d4f0dadf9b20607f78`.
Each fast-forwarded `origin/dev` using the configured explicit Git identity
`/home/ubuntu/.ssh/gavrilo123456-github`, with identity-only, batch, and strict
host-key checking. At exact S, each host ran `make check build`; all three
commands exited 0. This ran the full Go test suite, `go vet ./...`, and
`go build ./src/cmd/...`. Focused post-run reads confirm all three checkouts
remain clean on `dev` at exact S.

| Machine | Result at exact S |
| --- | --- |
| `mac-control-plane` | `make check build` PASS; `git diff --check` PASS |
| `linux-host` | Fast-forward PASS; `make check build` PASS; clean `dev`, HEAD S |
| `sandbox-host` | Fast-forward PASS; `make check build` PASS; clean `dev`, HEAD S |
| `factory-sandbox` | Fast-forward PASS; `make check build` PASS; clean `dev`, HEAD S |

Ubuntu validation used the authorized SSH fallback because these are
checkout-local Go commands and do not require mailbox work. The commands ran
on the synchronized host checkouts; no tracked files were edited or copied on
Ubuntu. The executed commands were `make check build`, followed by
`git rev-parse HEAD` and `git status --short --branch` on each host.

The required L2-07 F gate remains NOT RUN because the disposable A01 volumes,
measured device performance, and host-specific controller prerequisites listed
above do not exist yet. This evidence update records Source S delivery and
source checks only. It is not an evidence closeout E, does not claim L2-07
acceptance, and does not waive F. The next source unit may proceed only if its
source inputs are independent of these exact blockers; it must cite this
blocked predecessor without claiming an L2-07 E or PASS.
