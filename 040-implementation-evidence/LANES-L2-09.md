# LANES-L2-09 — lane preflight and staged-start rollback integration

**State:** BLOCKED — Source S and the hermetic H tier pass on Mac and all three
Ubuntu `dev` checkouts. The required native F acceptance is not run, so this
unit has no acceptance or evidence closeout E.

## Predecessor and scope

- Pre-unit Mac HEAD: `ae7da3b44847df10d1dc4ff483b6d741988b0ba4`, clean `dev`.
- Ordered L2-08 predecessor evidence is
  [`LANES-L2-08.md`](LANES-L2-08.md), blob
  `871fea76b91ed41f0536b89886c4c6942980e929`. L2-08 remains BLOCKED at native
  F because approved disposable APFS lane, cache, and backup fixtures do not
  exist.
- L2-07 remains BLOCKED at native F. Its exact blocker evidence is
  [`LANES-L2-07.md`](LANES-L2-07.md), blob
  `4b6555ec34039b13b2846d46891d6750850f0a36`; it records missing A01 devices,
  device performance measurements, and unresolved host cgroup prerequisites.
- `LANES-B021-02` and `LANES-B024-02` remain BLOCKED with evidence and held
  ACKs preserved. BUG-022/023 remain deferred. The B021 maintenance snapshot
  was not rebuilt.
- All work in this unit was source-only and hermetic. It used synthetic lane
  identities and `t.TempDir()` rollback fixtures. It read no credential file,
  wrote no mailbox request/ACK, and changed no service, production Runner,
  host configuration, storage, boot setting, firewall, or credential.

## Inputs read at the predecessor

The owner-amended phased plan, execution handbook, ledger, `AGENTS.md`, L2-07
and L2-08 blocker evidence, lane installers, endpoint derivation, and V3
profile validation were read at pre-unit HEAD.

| Input | Blob at pre-unit HEAD |
| --- | --- |
| `AGENTS.md` | `3402877131d9fcea513f5bae756582e4127ee892` |
| Detailed design | `f7404c2d5a0b35706f3a9e867991bffcb6171d93` |
| Phased plan | `082e73ac62a7815c3ed15778eb8162d81a43c347` |
| Execution handbook | `d04a659df5c5834725ea05b9b047f4ad296b21bc` |
| Execution ledger | `c4327cccc05ee6edfa5cb4fad1298710d9c12ca1` |
| L2-07 blocker evidence | `4b6555ec34039b13b2846d46891d6750850f0a36` |
| L2-08 blocker evidence | `871fea76b91ed41f0536b89886c4c6942980e929` |
| L2 test coverage manifest | `4672d5d2141a8d81e066b03d49a3dd971cfb07ea` |
| Fixed service plan | `55867423b98bc7ba6ff83f54c2c708dccfc1f1c1` |
| Linux capacity controls | `9e54f99be41a752e2f568231ae0785448e900719` |
| Mac storage controls | `a506c314a3a58c0ef7a28cd01852ab1f4024b9f5` |
| Direct endpoint map | `c6302c51d6ccfe22b0771e61ab80ecc88d6871b9` |
| V3 profile validation | `c7b2b15f3ad7d4fe661b17676d2c9a5ea7b8af88` |

## Source work

Added `lane-preflight`, a read-only Dev/QAS dry-run command. It accepts bounded
profile, policy, measurement, and live-observation files; repeatable peer
profiles bind the selected Linux or Mac configuration to its matching
cross-host credentials. Preflight requires the canonical host/lane pair,
fixed route and queue ports, exact lane-runtime policy and fresh measured
capacity controls. It derives service and resource paths; callers cannot
override a root, listener, unit, port, or controller. Profile validation
checks credential paths and fingerprints as references and never opens the
referenced certificate or key files. Its JSON report contains only fixed
artifact paths, modes, owners, content hashes, endpoint identities, and
explicit `action: none` / `native_acceptance: not_performed` fields.

The preflight rejects PRD selection, wrong host/lane profiles, missing or
duplicate Mac/Linux peers, mismatched paired certificate/key fingerprints,
foreign capacity policies, wrong service roots, host-global port-22 SSH paths,
and Linux resource artifacts outside the selected lane slice. Linux preflight
includes measured cgroup and project-quota proposals; Mac preflight includes
the measured APFS quota/cache/headroom contract. Neither path applies them.

Added a narrow staged-start protocol. It requires a rollback receipt before
staging, starts and checks the candidate admission-closed, and exposes no
open/admit or active-pointer operation. Any post-receipt failure invokes
rollback and then verifies restoration using a cancellation-independent,
bounded rollback context. A rollback failure is reported as
`ErrRollbackUnverified`; it cannot be mistaken for a successful recovery. The
only implementation exercised here is a `t.TempDir()` fixture. No production
backend or service start was run.

Seven H assertions cover the Mac and three Linux hosts in Dev and QAS, safe
dry-run output, wrong/foreign inputs, cross-host credential mismatch,
staged-start rollback with unchanged fixture PRD and SSH state, successful
closed start, rollback failure, immutable checked-plan access, and command
usage. The output contains neither credential paths nor fingerprints.

## Mac source gates

Execution machine: `mac-control-plane`, Go 1.27.1 from the Makefile-pinned
toolchain, with the shared Mac `GOCACHE` and `GOMODCACHE`.

| Command | Result |
| --- | --- |
| `make test-lanes UNIT=LANES-L2-09` | PASS; seven registered H assertions |
| `make check build` | PASS; full Go suite, `go vet ./...`, `make smoke`, and `go build ./src/cmd/...` |
| `git diff --check HEAD^ HEAD` | PASS |
| F acceptance | NOT RUN; requires isolated Linux and Mac native fixtures and is not dispatched by the H/N test runner |

Two test-compilation errors occurred during implementation and were corrected:
the new test helper first omitted its `net` import, then a paired-profile helper
call needed its new Mac-profile argument. The focused tests passed after both
corrections; no failing test was suppressed. The full gate run emitted only
non-fatal duplicate `-lproc` linker warnings for two Runner test binaries.

## Source S and synchronized validation

Source S is `3a31fed3415ab5a3d07728dddf7e621f6bddc16c`, committed on Mac `dev`
as `phase(LANES-L2-09): add fixed preflight and staged rollback`. The primary
Gitea push succeeded. Direct reads of both Gitea `origin/dev` and automatic
GitHub mirror `github/dev` resolved to the exact S SHA. Mac local
`origin/dev` tracking remained stale because Git could not create
`.git/refs/remotes/origin/dev.lock`; the actual remote refs were verified
directly.

Before pull, all three Ubuntu checkouts were clean on `dev`, with GitHub mirror
as `origin` and HEAD `ae7da3b44847df10d1dc4ff483b6d741988b0ba4`. Each
fast-forwarded with the configured explicit GitHub identity and strict host
key checking. At exact S each ran `make test-lanes UNIT=LANES-L2-09` and
`make check build`; both passed. Post-validation HEAD on each remained S and
each worktree remained clean. Remote commands used the configured explicit
Mac SSH identities and verified host keys. No tracked source was edited or
copied on Ubuntu.

| Machine | Before pull | Source S after validation | Result |
| --- | --- | --- | --- |
| `mac-control-plane` | `ae7da3b44847df10d1dc4ff483b6d741988b0ba4` | `3a31fed3415ab5a3d07728dddf7e621f6bddc16c` | H PASS; `make check build` PASS; clean `dev` |
| `linux-host` | `ae7da3b44847df10d1dc4ff483b6d741988b0ba4` | `3a31fed3415ab5a3d07728dddf7e621f6bddc16c` | fast-forward; H PASS; `make check build` PASS; clean `dev` |
| `sandbox-host` | `ae7da3b44847df10d1dc4ff483b6d741988b0ba4` | `3a31fed3415ab5a3d07728dddf7e621f6bddc16c` | fast-forward; H PASS; `make check build` PASS; clean `dev` |
| `factory-sandbox` | `ae7da3b44847df10d1dc4ff483b6d741988b0ba4` | `3a31fed3415ab5a3d07728dddf7e621f6bddc16c` | fast-forward; H PASS; `make check build` PASS; clean `dev` |

## F blocker and required follow-up

L2-09 F remains NOT RUN. The required fixture-only staged-start integration
must cover all L2 resource enforcement and prove the original services remain
unchanged on their native platforms. Its exact prerequisites are still
missing: L2-07's three-host A01 data/backup devices, device measurements and
host-specific cgroup/I/O prerequisites, and L2-08's Mac disposable APFS lane,
shared-cache, and backup volumes. The source and H results do not satisfy
those dependencies and do not claim native rollback or unchanged live
services. Provision and validate the prescribed isolated fixtures, then rerun
F against a selected clean synchronized revision before L2 acceptance and
evidence closeout E. Any later L3 source-only unit may proceed under the owner's
incremental-readiness decision only after its exact inputs and independence
from the missing native fixtures are recorded; L2 and all dependent live gates
remain blocked until F and E pass.
