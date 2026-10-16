# LANES-B024-01 — verify the Mac one-off teardown correction

**State:** PASS — the source regression gate and Source S delivery are
verified. This evidence-only closeout E records the completed unit; its own
delivery is the final gate before the next unit starts.

## Preflight and bounded scope

- Unit started from clean Mac `dev` HEAD
  `437554ccc3d372240b751c323f17bca9611cc0e8` on 2026-10-16.
- The exact blocked predecessor is
  [`LANES-L0.md`](LANES-L0.md), blob
  `fa06a2475d9281fd4802a8cd53735a0bc31671f4` at that HEAD. L0 is **BLOCKED / NOT
  STARTED** because PREP-00 did not pass. The PREP blocker record is
  [`LANES-PREP-00.md`](LANES-PREP-00.md), blob
  `2308c519bd802f0565186b4af651d2306bf736ba`.
- This source-and-evidence revalidation is independent of OCI access, future
  lane storage, cgroup provisioning, producer fencing, and L0 capacity
  equations: the complete original BUG-024 report, exact retained projections,
  source correction, regression fixtures, and prior delivery/test evidence are
  already present. It does not claim L0 passed or perform an operation that
  depends on L0.
- This unit made no installed-service, mailbox, storage, firewall, credential,
  or boot change. It published no Runner request or ACK. The original held ACK
  and the four already-consumed fresh verification ACKs remain unchanged.

### Read inventory and revisions

The required lane and original PoC planning documents, repository instructions,
ledger, blocked predecessor evidence, BUG-024 report/evidence, acceptance
manifest, and the recovery implementation and tests were read before this
unit's verification.

| Input | Blob at preflight HEAD |
| --- | --- |
| `AGENTS.md` | `73cd911515483e3b5c43cb0ce4e4ea0ed9bb8ff8` |
| Lane initial idea | `a0f67a0747ee49d71bf318784162970fe68f35e3` |
| Lane detailed design | `dcfd8acb56fc5154cc207dbf06e2432c87a81442` |
| Lane phased plan | `ec333494acf3fc9aa6ebc79499a7630083ea6066` |
| Lane execution handbook | `13d9501a0d8ae14f3c9a6dbf4848b6527ab6c84a` |
| Original PoC initial design | `308dfb82a25029a786f8d8341e6a89940a2eba0b` |
| Original PoC detailed design | `587654e9b71948519cc6a9e8d0c4813405e42e29` |
| Original PoC phased plan | `260bc76b32da215bfc8ad0f1adf05889aa6c244f` |
| Execution ledger | `3f420c7d6a10de31eff47b4207717cb15d15eb22` |
| `LANES-L0.md` blocker evidence | `fa06a2475d9281fd4802a8cd53735a0bc31671f4` |
| `LANES-PREP-00.md` | `2308c519bd802f0565186b4af651d2306bf736ba` |
| BUG-024 evidence | `a36d05d18e35a723ee9f5fa40e82dbb120a63445` |
| BUG-024 acceptance manifest | `20047ffecb1ea0801c0228a24ccfc22c32c8099a` |
| BUG-024 report | `59d95f49ee38f9decc75a21b7f9c1d67ffd960c9` |
| `src/internal/runtime/mac.go` | `7da4096d5d034f05c141991cae0c58d10c00a4b9` |
| `src/internal/runtime/bug024_mac_cleanup_test.go` | `8ded6792807823e597ae963e8d79dc94abbdd267` |
| `src/internal/execution/service.go` | `fd3ccc4af9114d611209c4defe27c73fd7f54899` |
| `src/internal/execution/completed_teardown_recovery.go` and test | `79e0d94bc6681ccb357052ab80dd5eedd156d5e1`; `e405974ebd66ec9ee33791b45677d3ae56f11ba8` |
| `src/internal/store/completed_teardown_recovery.go` and test | `867a98179a2a5693690197b798a2dea092367ea2`; `fde0102873253eb56a80b3bc514172a1ad2c1b7b` |
| `src/internal/runnerlocald/recover_completed_teardown.go` and test | `7b5d800fcc0c60b6f9dccbe9c56cac0428ae5640`; `47d6a574b64f0639e6fe7de0f620c6ceecb4adce` |
| `deploy/macos/install-launchagents.sh` | `7ccb26f63651ac69b400a3cdbaded718dfb911b0` |
| `Makefile` | `b20ca0c435b536332bdd0217b3297fe3ae6e80c2` |

The current `src`, `deploy`, and `Makefile` trees are identical to the tested
source cohort `d90155bd8a9391a14759b420c7a49b48f5a0d0a8` (`git diff` is empty
for those paths). No source or test correction was needed in this unit.

## Exact incident state and correction

The immutable original pair is request
`req-asf-ext-20261015-harness-01`, job
`job-f0917cc13fe809c27d626401a952f5ed`, session
`sess-5b9be3c1c9186dc2a78edee42238927b`, and command
`cmd-bd164c1d3a600569a58baddfe9336ed4`. Its command succeeded with exit 0 and
complete, untruncated output, while its job and teardown were `lost`; response
revision was 3 and the final/available event cursor was 7. The historical
response SHA-256 is
`1000fbdd268fc0c65b70a75a11d30d7e9ec323ab89f7072b54a0fa43de6c520f`; the
seven-event prefix SHA-256 is
`9c23b99831dd3f6d0bed99513a2c3c0f6645a8e3487e26b6935c00d774531eb1`. Its ACK
is deliberately **HELD**.

The approved original-state preflights recorded one retained session
reservation, zero command slots/queues/resumable jobs, and the owner marker for
PID/process group `80710`; read-only process inspection found no group member.
Those facts proved retained capacity, not the physical cause. The exact
historical physical cause remains **unknown**. Source tracing found a
reproducible Mac cleanup observation race: a redirected child may remain
briefly after Bash reap and command-output EOF. The correction waits up to
500 ms for the owned process group to disappear without sending another
signal; persistent members, inspection errors, or cancellation retain the
ownership marker and workspace.

The separate supported recovery path accepts only the exact successful,
complete command with a lost teardown and retained session reservation. It
proves ownership for the complete idle set before releasing only session
capacity, preserves the command/job/event outcome, and finalizes the marker and
workspace after the durable release. It never replays the script. Original
response/event hashes and the held ACK were verified unchanged after recovery.

The source correction is commit
`ca79ddea9a9010cdbf083710653a8db6dae1e600`; the complete tested source cohort
is `d90155bd8a9391a14759b420c7a49b48f5a0d0a8`. The installed Mac acceptance
recorded revision `77910358a149d6e8fcdf4694498ebdf6c1f07f58`. These are reused
as existing BUG-024 evidence, not presented as new service operations in this
unit.

## R and H evidence

**R — exact original disposition state:** the read-only preflight, exact
original response/event projections, owner marker/process observation, and
post-recovery hash/capacity proof are retained in
[`BUG-024.md`](BUG-024.md#approved-maintenance-commands-and-preflight-history).
The postflight established the supported exact-pair disposition while leaving
the historical result and held ACK intact. This is the narrow BUG-024 original
work trace; it is not the full L0 inventory or a substitute for L0.

**H — source regression and correction:**

- Current Mac precheck: `make check-go` **PASS**; Go `1.27.1`, Darwin arm64.
- Current Mac focused command, exit 0:

  ```sh
  GOCACHE=/private/tmp/remote-session-runner-gocache \
  GOMODCACHE=/private/tmp/remote-session-runner-gomodcache GOTOOLCHAIN=local \
  '/Users/tomasz.walczuk/Library/Application Support/RemoteSessionRunner/toolchains/go1.27.1/bin/go' \
    test ./src/internal/store ./src/internal/execution ./src/internal/runtime ./src/internal/runnerlocald \
    -run 'Test(BUG024|CompletedTeardown)' -count=1
  ```

  Results: `store` 2.333s, `execution` 0.993s, `runtime` 1.663s, and
  `runnerlocald` 1.882s — all **PASS**. The native Darwin fixture exercised
  transient descendant exit, persistent-descendant retention/retry,
  cancellation retention, and exactly-once execution in a temporary root.
- Existing full validation at the identical tested source SHA `d90155b` is
  retained in `BUG-024.md`: `make test` and `make vet` passed on Mac and all
  three Ubuntu hosts; focused race checks passed on Mac, `linux-host`, and
  `sandbox-host`. `factory-sandbox` full tests and vet passed. Its additional
  race attempt is recorded **NOT RUN**, because `CGO_ENABLED=0` and no C
  compiler was available; this is not represented as a PASS. The race evidence
  covers the Mac behavior and both Linux architectures, while factory remains
  explicitly limited.
- The tested source handoff was `d90155b`; the subsequent BUG-024 evidence
  handoff was `77910358`. Their primary Gitea push, exact GitHub mirror, and
  clean three-host fast-forward results are recorded in `BUG-024.md`. The
  source cohort has not changed since.

The original route manifest remains authoritative: the four fresh verification
requests `req-bug024-20261016-{mac,linux,sandbox,factory}-01` each have their
ACK recorded as consumed. Retained response/event files are archives, not
pending ACKs. No new request or ACK was created for this unit.

## Source S delivery and evidence closeout E

This evidence, ledger update and handoff update are the unit's
documentation-only Source S; the existing source fix and regression cohort
above are the tested code S.

| Source S delivery check | Result |
| --- | --- |
| Mac `dev` | `5e5a07f1bc87a4cd52062d6f8eb729dce199e824`, clean; `git diff --cached --check` and `git show --check` passed |
| Primary Gitea `origin/dev` | Explicit-key push `437554c..5e5a07f`; `ls-remote` returned exact S |
| GitHub mirror `github/dev` | Explicit-key `ls-remote` returned exact S before host pulls |
| `linux-host` | Clean `dev`, expected GitHub origin, pre-pull `437554ccc3d372240b751c323f17bca9611cc0e8`; explicit-identity `pull --ff-only origin dev`; post-pull `HEAD` and `origin/dev` both S, clean |
| `sandbox-host` | Same pre-pull branch/worktree/origin/SHA guards; explicit-identity `pull --ff-only origin dev`; post-pull `HEAD` and `origin/dev` both S, clean |
| `factory-sandbox` | Same pre-pull branch/worktree/origin/SHA guards; explicit-identity `pull --ff-only origin dev`; post-pull `HEAD` and `origin/dev` both S, clean |

Mac-to-host SSH used the verified configured hosts and explicit identities:
`remote-session-runner` / `~/.ssh/remote-session-runner` for `linux-host`,
`sandbox.env` / `~/.ssh/dev.slidestud.io` for `sandbox-host`, and
`factory-sandbox` / `~/.ssh/factory-sandbox` for `factory-sandbox`; all used
`IdentitiesOnly=yes`, `BatchMode=yes`, `StrictHostKeyChecking=yes`, and
`UpdateHostKeys=no`. Ubuntu pulls used
`/home/ubuntu/.ssh/gavrilo123456-github` with the explicit Git SSH command.
No tracked source was copied outside Git.

The Ubuntu code tests were not rerun after this documentation-only S: `src`,
`deploy`, `Makefile`, schemas, and test files are unchanged from tested code S
`d90155bd8a9391a14759b420c7a49b48f5a0d0a8`. The already completed all-host
tests and vet at that exact source SHA remain applicable; no executable gate
was omitted for changed code.

Evidence closeout E is committed separately as
`docs(LANES-B024-01): record verified source delivery`. Its primary/mirror and
three-host delivery follows the same `AGENTS.md` order. E itself contains no
self-referential hash; its final SHA is recorded in the next unit's preflight
or the post-commit handoff.

`LANES-B024-02` remains a separate baseline-maintenance unit. Existing BUG-024
repair and fresh acceptance evidence are retained inputs; they do not waive
the handbook's A08 package and approval checkpoint for any operation that
requires affected PRD maintenance. No such operation is performed here.
