# LANES-L2-08 — Mac APFS quotas, caches, snapshots and headroom

**State:** BLOCKED — source implementation and Mac source gates pass, but the
required native F acceptance has not run. No L2-08 acceptance or evidence
closeout E is claimed.

## Predecessor and scope

- Pre-unit Mac HEAD: `c7f4e6db3a68ed965fac063958e7e8bffcc33278`, clean `dev`.
- L2-07 source S `6f0db6d0aa831fc09162bf32ddd3cd45e6627e3e` remains **BLOCKED**;
  its exact blocker evidence at this predecessor is
  [`LANES-L2-07.md`](LANES-L2-07.md), blob
  `4b6555ec34039b13b2846d46891d6750850f0a36`. L2-07 has no E/PASS. L2-08
  source is independent of L2-07's Linux devices and cgroup prerequisites;
  this does not make either unit's F gate pass.
- `LANES-B021-02` and `LANES-B024-02` remain BLOCKED with their evidence
  preserved. BUG-022/023 remain deferred. No mailbox request or ACK, service,
  host storage, credentials, firewall, production Runner, or held evidence was
  changed.
- Source uses synthetic measured volumes and the durable byte-ledger tests;
  it reads no production credentials or mailbox state.

## Read inputs at the predecessor

The lane idea, detailed design, phased plan, execution handbook, `AGENTS.md`,
ledger, blocked L2-07 evidence, L2-06 evidence, and relevant policy, root
handoff, namespace, and installer code were read at the pre-unit HEAD.

| Input | Blob at pre-unit HEAD |
| --- | --- |
| `AGENTS.md` | `3402877131d9fcea513f5bae756582e4127ee892` |
| Lane initial idea | `503e54457228da265666ae48ceb2be22a0c0b81f` |
| Lane detailed design | `f7404c2d5a0b35706f3a9e867991bffcb6171d93` |
| Lane phased plan | `082e73ac62a7815c3ed15778eb8162d81a43c347` |
| Lane execution handbook | `d04a659df5c5834725ea05b9b047f4ad296b21bc` |
| Execution ledger | `10aeb478e7e0bf93272452bdc0c3ed56b2b03469` |
| L2-07 blocker evidence | `4b6555ec34039b13b2846d46891d6750850f0a36` |
| L2-06 evidence | `42e04f613dddd7c94007f2716a318c19f7e8238e` |
| `lanecapacity/policy.go` | `f4a40d7b1a405a68f0042c1aaa0db43ab9b8c463` |
| `lanecapacity/policy_test.go` | `535cf29582498a13b80d8267b2efe8ea4072479e` |
| `laneinstall/plan.go` | `55867423b98bc7ba6ff83f54c2c708dccfc1f1c1` |
| `laneinstall/linux_resources.go` | `9e54f99be41a752e2f568231ae0785448e900719` |
| `config/lane_namespace.go` | `29618b35c8c2c17621334673e5a8a212169c92a9` |
| `laneroot/validator.go` | `9646b7322f1394f14ae917d423bf5ac8c742b76c` |

## Source work

`StorageVolume` now carries the measured APFS quota separately from container
capacity. Policy validation requires the exact measured Dev/QAS quota for each
UUID and mountpoint and rejects a quota larger than measured volume capacity.
The policy digest and live-observation comparison bind that quota. The two Mac
Go caches remain at the fixed shared Makefile paths; each must be a measured
quota-bound APFS mount in the Data-volume container, and their observed quotas
must sum exactly to the shared-cache budget. A directory budget alone no longer
stands in for the filesystem bound.

`BuildMacStorageControlPlan` accepts only a fresh Dev or QAS Mac lane-runtime
policy. It binds both lane volumes to the fixed derived service roots and exact
measured quotas, lists both shared cache volume identities, preserves the
separate backup device and low-water values, and reports the 10 GiB snapshot
budget and full projected Data-volume equation. It refuses PRD, wrong paths,
changed quotas, unbounded caches, stale policy, and insufficient
`max(20 GiB, 15% of Data-volume capacity)` headroom. The result is a
non-mutating plan; volume creation, mounting, quota changes, snapshot cleanup,
cache movement, and service installation are outside this code path.

The `TestLanesL208*` regressions cover exact Dev/QAS bindings, both cache
quotas, the complete headroom calculation, separate-backup accounting, PRD
refusal, changed quota/mount evidence, and floor exhaustion. The policy tests
also reject cache quotas that no longer equal the policy budget.

## Guarded fixture F runner source extension

The shared `make test-lanes-controls` entry point now binds L2-08 to
`mac-control-plane`, requires an owner-only external manifest and its exact
approved SHA-256, and refuses any unit, host, path, expiry, UUID, mountpoint,
quota or device mismatch. The live APFS guard resolves every volume to its
container and physical store, requires `VirtualOrPhysical=Physical`,
`Internal=false`, and `WholeDisk=true`, and requires the test/cache volumes and
backup volume to use separate external physical disks, both distinct from the
production Data disk. It checks the production Data container read-only against
the full 70 GiB projected envelope and `max(20 GiB, 15%)` reserve before any
probe writes.

The Mac F test writes only to the approved disposable APFS volumes, exercises
their byte quotas, bounded log capture, and fixture-container reserve, then
rechecks device identities and the read-only Data floor. It uses the
manifest-bound fixture identity token under the invoking account, passes no
operator/provider environment to Go tests or disk inventory helpers, and
creates no OS user. It does not create/delete APFS snapshots: the 10 GiB
snapshot allowance is part of the conservative Data projection, while current
snapshot inventory/usage remains a fresh operational measurement gate. No
production Data files, shared caches, services, or snapshots are modified.
The registered F Go test subprocess uses the `go-build` volume for its build
cache and temporary files and the `go-modules` volume for its module cache.
Both are on the disposable external test disk. The outer repository build uses
the normal Mac Go toolchain cache; it is neither purged nor copied. F-generated
cache entries remain on the fixture disks for separately approved cleanup.

The source extension does not pass L2-08 F. No disposable external APFS disks
or volumes exist, so the native quota/log procedure remains NOT RUN.

## Mac source gates

Execution machine: `mac-control-plane`, Mac `dev`, Go 1.27.1 from the
Makefile-pinned toolchain, with the shared Mac `GOCACHE` and `GOMODCACHE`.

| Command | Result |
| --- | --- |
| Focused `go test ./src/internal/lanecapacity ./src/internal/laneinstall -run 'Test(L208|LanesL208)' -count=1` | PASS |
| `make check build` | PASS; full Go suite, `go vet ./...`, and `go build ./src/cmd/...` exit 0 |
| `gofmt` using the pinned Go 1.27.1 tool | PASS |
| `git diff --check` | PASS |
| `make test-lanes UNIT=LANES-L2-08 LANES_TIER=F` | NOT RUN; the registered F acceptance requires approved native disposable APFS fixtures and the dispatcher only runs H/N assertions |

The initial `gofmt` attempt failed because it is not on the shell `PATH`; it
was rerun with the pinned toolchain. The first focused Go invocation used the
default cache and was denied access; the rerun used the Makefile's shared
`/private/tmp/remote-session-runner-gocache` and
`/private/tmp/remote-session-runner-gomodcache` and passed. No test failure was
suppressed.

## F blockers — Mac

PREP-00 records that no disposable APFS fixture volumes are provisioned. The
native F gate still needs an explicitly approved, isolated fixture set with
measured UUID, mountpoint, container/device identity, and actual quota for both
fixed Dev/QAS roots, both fixed shared-cache paths, and bounded backup storage.
The fixtures must be distinct from the unsuffixed PRD root and existing shared
caches. No such volume was created or mounted during this source unit.

F must then fill the Dev/QAS and cache quotas, prove stable byte refusal and
wrong-mount rejection, exercise bounded log and backup behavior, remeasure
snapshots/headroom, and verify the unsuffixed PRD configuration and service
remain unchanged. It must also retain `compute_reservation: none`; this unit
does not claim Mac CPU, memory, PID, or aggregate-I/O reservation. Static
policy tests and this plan do not substitute for those native checks.

## Source delivery status

Source S is `ae386b1a774d9bceb3aa3d137b611a8abec16bb2` on Mac `dev`.
The primary Gitea `origin/dev` push succeeded and direct `ls-remote` reads of
both `origin/dev` and the automatic `github/dev` mirror returned this exact
SHA. Mac local `origin/dev` tracking metadata remained stale because Git could
not create `.git/refs/remotes/origin/dev.lock`; this did not affect the
verified remote refs or the commit.

Before the pulls, `linux-host`, `sandbox-host`, and `factory-sandbox` each had
a clean `dev` checkout at predecessor
`c7f4e6db3a68ed965fac063958e7e8bffcc33278`. Each host fast-forwarded from its
GitHub `origin/dev` to Source S. `make check build` passed on Mac and on all
three hosts at Source S: full Go tests, `go vet ./...`, and `go build
./src/cmd/...` exited 0. Post-validation `HEAD` on every host remained exactly
Source S and each worktree was clean. Remote commands used the configured
explicit SSH identities and host-key verification. No tracked source was
edited or copied on Ubuntu.

| Machine | Before pull | Source S after validation | Result |
| --- | --- | --- | --- |
| `mac-control-plane` | `c7f4e6db3a68ed965fac063958e7e8bffcc33278` | `ae386b1a774d9bceb3aa3d137b611a8abec16bb2` | `make check build` PASS; clean `dev` |
| `linux-host` | `c7f4e6db3a68ed965fac063958e7e8bffcc33278` | `ae386b1a774d9bceb3aa3d137b611a8abec16bb2` | fast-forward; `make check build` PASS; clean `dev` |
| `sandbox-host` | `c7f4e6db3a68ed965fac063958e7e8bffcc33278` | `ae386b1a774d9bceb3aa3d137b611a8abec16bb2` | fast-forward; `make check build` PASS; clean `dev` |
| `factory-sandbox` | `c7f4e6db3a68ed965fac063958e7e8bffcc33278` | `ae386b1a774d9bceb3aa3d137b611a8abec16bb2` | fast-forward; `make check build` PASS; clean `dev` |

This evidence is a docs-only follow-up after the Source S validations. L2-08
remains BLOCKED until the native F gate passes. This source work does not
close the L2-07 Linux F gate or imply an L2-07 evidence closeout.
