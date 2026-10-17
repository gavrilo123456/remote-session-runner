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

## Delivery status

This commit is Source S; its own SHA is not embedded here. The required
Gitea-primary push, exact GitHub mirror verification, and three-host fast-forward
validation follow after S and are not claimed by this file version. Since the
required F gate remains blocked, this document is not an evidence closeout E
and the unit remains BLOCKED. A follow-up status entry can record post-push
source checks without calling the unit accepted. Future work may continue only
on a subsequent source unit whose source inputs are independent of the missing
Linux fixture/host gate; the blocked L2-07 evidence must remain cited as such.
