# LANES-L3-15 — signed admission and full L1 transition integration

**State:** IN PROGRESS. The registered H and N source gates and the post-S
source validations pass. The unit remains blocked in the ordered sequence by
the L3-14 physical F gate; this record does not claim L3-14 or L3-15 PASS,
live acceptance, or a completed lane transition.

## Pre-unit revision, predecessor, and scope

- Pre-unit Mac `dev` HEAD was clean at `01b13afba5911af6d06bee467d1f8819e220066d`.
- The ordered predecessor is L3-14. Its latest evidence at pre-unit HEAD is
  `LANES-L3-14.md`, blob `0dae129af6ac63a7c7fb863cd50194801fecf1bd`, recording
  source S `8369163e346bb2c90c0c6fcca16b521b621afcaa` and its source checks as
  complete while physical F remains NOT RUN. L3-14 is still IN PROGRESS and
  BLOCKED; no predecessor PASS/E is inferred. L3-15's hermetic source work
  uses those delivered source APIs and is independent of the missing physical
  fixtures. This follows the owner's incremental-readiness authorization.
- The exact open L3-14 F dependencies remain the L2-07 approved Linux data and
  backup devices with measured capacity/performance, compatible Linux cgroup
  hierarchy/controller proof (including factory controller propagation), and
  the L2-08 approved disposable Mac APFS lane/cache/backup fixtures. Evidence
  remains in `LANES-L2-07.md` (blob
  `4b6555ec34039b13b2846d46891d6750850f0a36`) and `LANES-L2-08.md` (blob
  `871fea76b91ed41f0536b89886c4c6942980e929`).
- `LANES-B021-02` and `LANES-B024-02` remain BLOCKED with existing evidence
  unchanged. BUG-022/023 baseline repair remains deferred. No maintenance
  operation was started and no maintenance package was rebuilt. No production
  credentials, mailbox requests/ACKs, services, installations, host
  configuration, storage, firewall, boot state, or retained work/evidence were
  changed. Test credentials and SQLite state are isolated generated fixtures.
- Source revision S is
  `c9bd10dad99ad9f233a17ecbb1f2db0d5ef391a3`, parent
  `01b13afba5911af6d06bee467d1f8819e220066d`. Gitea `origin/dev` accepted S;
  the automatic GitHub mirror resolved to the exact same SHA. All three clean
  Ubuntu `dev` checkouts were fast-forwarded to S before validation.

## Inputs reviewed at pre-unit HEAD

The lane idea/design/plan/handbook, original design and phased plan, repository
instructions, ledger, L3-14 predecessor and blocker evidence, L3-15 coverage
manifest, and implementation/test files were reviewed before source changes.
The relevant Git blobs at the pre-unit revision were:

| Input | Blob |
| --- | --- |
| `AGENTS.md` | `3402877131d9fcea513f5bae756582e4127ee892` |
| Original initial design | `308dfb82a25029a786f8d8341e6a89940a2eba0b` |
| Original detailed design | `587654e9b71948519cc6a9e8d0c4813405e42e29` |
| Original phased plan | `260bc76b32da215bfc8ad0f1adf05889aa6c244f` |
| Lane initial idea | `503e54457228da265666ae48ceb2e22a0c0b81f` |
| Lane detailed design | `f7404c2d5a0b35706f3a9e867991bffcb6171d93` |
| Lane phased plan | `082e73ac62a7815c3ed15778eb8162d81a43c347` |
| Execution handbook | `d04a659df5c5834725ea05b9b047f4ad296b21bc` |
| Execution ledger | `feab54a66ce5022d51a39c577e3e3150f0040427` |
| L3-14 predecessor evidence | `0dae129af6ac63a7c7fb863cd50194801fecf1bd` |
| L2-07 / L2-08 blocker evidence | `4b6555ec34039b13b2846d46891d6750850f0a36` / `871fea76b91ed41f0536b89886c4c6942980e929` |
| B021-02 / B024-02 evidence | `3e9700485f48ad75ad42ba04334d5b3871a36c7a` / `15211861260284d0923c9d64259bd7746700399e` |
| Coverage manifest / dispatcher manifest test | `d4050fa4927da12d70f2d0ee890277cea2f67a9f` / `4f5830279795e363b13f288829ac548d2cc9478c` |
| Security anchor / L3 payload | `5498d8abbd9dbd1b1c9ad12ffae5d6112458a149` / `495c2f8abdc3f0a7f0403b2f37160aa27a54d0b4` |
| L3-05 / L3-11 release regressions | `0a57a8e164cd185d193415aab64eefc93d198ba8` / `584df6299800cf6ddc86adeb3845a8f25b4cc715` |

Execution machines were `mac-control-plane`, `linux-host`, `sandbox-host`,
and `factory-sandbox`. Ubuntu validation used only the synchronized Git
checkouts. No live L3 operation or physical F probe was attempted.

## Source contract implemented

- The security anchor now applies signed ingress/admission directives only
  after checking authority signature, host binding, the current fresh ledger,
  local/trusted time validity, phase/epoch monotonicity, exact predecessor and
  lease state, and candidate pending state. Exact replay is idempotent; stale,
  forked, skipped, wrong-host, tampered, revoked, or expired directives fail
  closed.
- Candidate generation zero remains closed. Later phase transitions are
  contiguous. A closure binds the exact open directive and prior candidate /
  runtime, clears an unaccepted pending candidate, and requires a higher epoch
  before replacement. The anchor retains directive identity and signer/ledger
  metadata needed to validate later acceptance.
- Active-runtime recording now requires an authority-signed acceptance
  statement bound to the exact current open directive and candidate. It checks
  the acceptance interval against directive, local, and trusted time, rejects
  reuse of an earlier acceptance for a higher epoch, and permits an exact
  idempotent retry. Production callers cannot advance the anchor using
  caller-constructed directive fields.
- Seven H assertions cover signed admission/acceptance, rejected tampering,
  wrong-host/expired/stale-ledger/identity-fork/skipped-phase cases, candidate
  closure and replacement behavior, crash-before-commit retry, and the existing
  registry closure/quiescence/zero-work/post-commit-handoff guards. One native
  N assertion proves SQLite directive high-water survives restart. All use
  generated test identities and temporary state; none are live operational
  acceptance.

## Source tests and Mac checks

| Machine | Command | Result |
| --- | --- | --- |
| `mac-control-plane` | `make test-lanes UNIT=LANES-L3-15 LANES_TIER=H` | PASS — seven registered assertions |
| `mac-control-plane` | `make test-lanes UNIT=LANES-L3-15 LANES_TIER=N` | PASS — one registered assertion |
| `mac-control-plane` | `make check build` | PASS — full Go suite, vet, and command builds |
| `mac-control-plane` | `make smoke` | PASS |
| `mac-control-plane` | Focused `go test -race` for `release` and `laneregistry` | PASS |
| `mac-control-plane` | `git diff --check` and JSON manifest parse | PASS before S commit |

Focused Mac race command, using the Makefile-pinned Go 1.27.1 binary and the
shared `/private/tmp/remote-session-runner-gocache` and
`/private/tmp/remote-session-runner-gomodcache`:

```sh
GOCACHE=/private/tmp/remote-session-runner-gocache GOMODCACHE=/private/tmp/remote-session-runner-gomodcache GOTOOLCHAIN=local '/Users/tomasz.walczuk/Library/Application Support/RemoteSessionRunner/toolchains/go1.27.1/bin/go' test -race ./src/internal/release ./src/internal/laneregistry -run 'TestLanesL315|TestLanesL105ReplacementClosesThenQuiescesBeforeHigherEpochStaging|TestLanesL112PRDMaintenanceRequiresFullClosureAndZeroWork|TestLanesL113PRDPostCommitRepairRequiresFreshClosureQuiescenceAndHandoff' -count=1
```

The first full `make check build` run exposed a stale L3-11 test fixture that
assumed a nonzero initial lease. The fixture was corrected to preserve the
intended invalid-cap condition, and the full suite passed on the final S. The
first focused race invocation used bare `go`, which is not on this Mac shell's
PATH; it did not launch the tests. The command was rerun with the explicit
Makefile-pinned toolchain above and passed. These were corrected test/command
issues; no failure was waived.

## Gitea, mirror, and Ubuntu post-S validation

The Mac checkout was `dev`, clean at S after the source commit. The exact
primary push command was:

```sh
git -c core.sshCommand='ssh -i /Users/tomasz.walczuk/.ssh/local-gitea-key -o IdentitiesOnly=yes -o BatchMode=yes -o StrictHostKeyChecking=yes' push origin dev
```

The exact mirror lookup command was:

```sh
git -c core.sshCommand='ssh -i /Users/tomasz.walczuk/.ssh/gavrilo123456-github -o IdentitiesOnly=yes -o BatchMode=yes -o StrictHostKeyChecking=yes' ls-remote github refs/heads/dev
```

Both Gitea `origin/dev` and GitHub `dev` resolved to
`c9bd10dad99ad9f233a17ecbb1f2db0d5ef391a3`.

| Machine | Pre-pull state and route | Post-pull gates at S | Result |
| --- | --- | --- | --- |
| `linux-host` (`129.151.232.40`) | Clean `dev` at `01b13af`; SSH identity `/Users/tomasz.walczuk/.ssh/remote-session-runner`; IP used because its SSH alias did not resolve; strict pinned host-key checking | H, N, `make check build`, `make smoke`, focused race | PASS; clean `dev` at S |
| `sandbox-host` (`132.226.205.205`) | Clean `dev` at `01b13af`; SSH identity `/Users/tomasz.walczuk/.ssh/dev.slidestud.io`; IP used because its SSH alias did not resolve; strict pinned host-key checking | H, N, `make check build`, `make smoke`, focused race | PASS; clean `dev` at S |
| `factory-sandbox` (`145.241.255.66`) | Clean `dev` at `01b13af`; SSH identity `/Users/tomasz.walczuk/.ssh/factory-sandbox`; configured alias; strict host-key checking | H, N, `make check build`, `make smoke`; focused race applicability check | PASS; clean `dev` at S. Race NOT RUN (`cc` absent, `CGO_ENABLED=0`) |

All Ubuntu pulls used
`git -c core.sshCommand='ssh -i /home/ubuntu/.ssh/gavrilo123456-github -o IdentitiesOnly=yes -o BatchMode=yes -o StrictHostKeyChecking=yes' pull --ff-only origin dev`.
The H/N commands and full source/build gates were run from
`/home/ubuntu/projects/remote-session-runner` using its pinned Go 1.27.1
toolchain and normal Go caches. Linux-host and sandbox-host had a compiler and
`CGO_ENABLED=1`, so the focused race suite passed on both. No tracked file was
edited or copied on Ubuntu.

The latest evidence for L3-14 remains blocked; the owner's incremental
readiness decision allows the next hermetic source unit to proceed after this
evidence-only delivery. Physical capacity, PKI/service installation, lane
candidate deployment, and live acceptance remain outstanding at their planned
gates. Preserve the L3-14 blocker evidence, B021/B024 evidence, deferred
BUG-022/023 scope, and the existing maintenance snapshot.
