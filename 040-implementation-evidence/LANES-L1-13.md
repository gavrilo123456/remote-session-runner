# LANES-L1-13 — Post-commit repair and complete contract integration

**State:** IN PROGRESS. This is hermetic source implementation and testing;
no installed or live lane acceptance is claimed.

## Predecessor and scope

- Ordered predecessor: `LANES-L1-12` evidence closeout E
  `9b27d084afea60ac5bc2311b820f65ed133d7efd`, clean on Mac `dev`, Gitea,
  GitHub, and all three Ubuntu `dev` checkouts.
- L1-13 closes the remaining PRD operation contract: post-commit repair closure,
  supported terminal reconciliation, retained deferred evidence, zero-work
  handoff, archive/group release, sequential failed-child replacement, and
  V2 abort fence release only after the exact mailbox marker is removed and
  absence is durably recorded. It also runs the complete L1 H coverage.
- `LANES-B021-02` and `LANES-B024-02` remain **BLOCKED** at evidence blobs
  `3e9700485f48ad75ad42ba04334d5b3871a36c7a` and
  `15211861260284d0923c9d64259bd7746700399e`. This source unit has no
  dependency on their maintenance closeout. BUG-022/023 remain deferred.
- Fixtures are temporary and synthetic. No production credential, mailbox
  publication, ACK, service, host setting, storage, firewall, publisher, or
  production Runner is used or changed.

## Pre-unit inventory

Pre-unit HEAD was `9b27d084afea60ac5bc2311b820f65ed133d7efd` on clean Mac
`dev`; direct Gitea and GitHub `dev` refs matched. The three Ubuntu checkouts
were clean on `dev` at the same SHA. The design's PRD recovery and post-commit
repair contract, the full L1 plan, handbook and ledger, L1-12 evidence, current
B021/B024 blocker evidence, PRD operation graph, V2 migration-fence controller,
and coverage dispatcher were reviewed.

| Read input | Blob at pre-unit HEAD |
| --- | --- |
| `AGENTS.md` | `3402877131d9fcea513f5bae756582e4127ee892` |
| Lane initial idea | `503e54457228da265666ae48ceb2e22a0c0b81f` |
| Lane detailed design | `f7404c2d5a0b35706f3a9e867991bffcb6171d93` |
| Lane phased plan | `082e73ac62a7815c3ed15778eb8162d81a43c347` |
| Lane execution handbook | `d04a659df5c5834725ea05b9b047f4ad296b21bc` |
| Execution ledger | `c26cebf8cb46856f1db0d8a2ec4ee02c1f6e6a85` |
| L1-12 predecessor evidence | `98d2008c4f0fc912413a805b3e6b3ea058573c88` |
| B021-02 blocked evidence | `3e9700485f48ad75ad42ba04334d5b3871a36c7a` |
| B024-02 blocked evidence | `15211861260284d0923c9d64259bd7746700399e` |
| BUG-022 record | `d194da008eb8f843cf97d0e496a696a8257935b3` |
| BUG-023 record | `a603119d409a6b30f217a930e88e9b7c7ac667a4` |
| H coverage manifest | `55cb5a248fcfff3ebc583a9d74ed00edadeddc21` |
| PRD operation graph | `da8506eefb0007d339d88429fbc0415160cf5ed0` |
| V2 migration fence | `b44f57651ec881f40e403f346ca8fa6689e3bba0` |
| L1-12 PRD operation tests | `09e50468521c54d5f9b0f27ea0eac58cccc1580a` |

## H proof plan

1. Drive committed primary and recovery-child operations into a post-commit
   fault and verify that the exact failure state/evidence and continuity plus
   prior-acceptance lineage are retained under the exclusive group.
2. Reject incomplete or stale all-host repair closure; do not claim global
   closure before every current-candidate endpoint is closed. Reject repair
   quiescence until closure completes, and reject handoff for unsupported or
   uncertain dispositions, missing deferred request/ACK evidence, remaining
   work, or absent zero-work postflight.
3. Permit a sealed handoff and archive only after the closure and zero-work
   proof set is complete. Atomically archive a recovery child and its failed
   parent at handoff, release the group, and reject any replacement child
   after this terminal handoff.
4. Verify a failed pre-commit child can be archived before a strictly
   higher-epoch sequential replacement, while a second concurrent child stays
   excluded and altered/reparented registry history is rejected.
5. Exercise V2 abort recovery at each release boundary. Retained pairs remain
   intact; V3 operation-group allocation remains blocked while the exact
   aborted marker exists; only exact marker removal plus an exclusive-lock
   absence receipt releases the durable fence and permits a later V3 group.
6. Run the complete L1 H suite and each unit's registered coverage in addition
   to the required package, vet, build, smoke and race gates.

## Source implementation

Implemented a guarded post-commit repair state path for both primary and
accepted recovery-child operations. The registry preserves the failed state
and digest, continuity bundle, and prior accepted candidate/acceptance lineage;
requires fresh all-host closure epochs above candidate and anchor high-water;
requires closure before complete zero-work quiescence; and seals the next-QAS
handoff before archiving. A recovery-child handoff archives its
`restore_required` parent in the same registry update and releases the
exclusive group only after the handoff is complete. Additional children are
rejected after terminal archive.

V2 abort now remains an exclusive PRD migration fence after the aborted marker
is removed until an exclusive-lock receipt confirms both committed and pending
marker names are absent and the mailbox-root directory has been synced. The
controller can reconcile a crash between marker removal and registry receipt.
New V3 PRD operation groups stay blocked while any V2 fence lacks that receipt.
Deferred request/ACK pairs remain retained and are not replayed or deleted.

Added four registered L1-13 H assertions for primary repair, recovery-child
repair and parent archive, altered-history rejection, and V2 marker-removal
receipt gating. Updated the coverage dispatcher and the related L1-09/L1-12
fixtures. This unit adds no installer or live service changes. The PRD
operation registry remains an evidence/state authority only; authority
signatures and host attestations are not claimed to be verified by these
structural fixtures.

### Mac source gates before Source S

Commands ran on `mac-control-plane` (Darwin arm64), with Go 1.27.1 via the
Makefile and its shared `/private/tmp/remote-session-runner-gocache` and
`/private/tmp/remote-session-runner-gomodcache`.

The H gates ran serially with `make test-lanes UNIT=<unit> LANES_TIER=H` for
each unit from `LANES-L1-01` through `LANES-L1-13`. L1-03 N ran separately with
`make test-lanes UNIT=LANES-L1-03 LANES_TIER=N`.

| Command | Result |
| --- | --- |
| `make test-lanes UNIT=LANES-L1-01..13 LANES_TIER=H` (serial, one invocation per unit) | PASS; every registered L1 H assertion passed, including four L1-13 assertions |
| `make test-lanes UNIT=LANES-L1-03 LANES_TIER=N` | PASS; all six registered N assertions passed |
| `make test` | PASS; complete `./...` suite |
| `make vet` | PASS |
| `make build` | PASS |
| `make smoke` | PASS |
| Focused race: `GOCACHE=/private/tmp/remote-session-runner-gocache GOMODCACHE=/private/tmp/remote-session-runner-gomodcache GOTOOLCHAIN=local '/Users/tomasz.walczuk/Library/Application Support/RemoteSessionRunner/toolchains/go1.27.1/bin/go' test -race ./src/internal/laneregistry ./src/internal/lanemigration ./src/internal/mailboxfence -run '^TestLanesL113' -count=1` | PASS using the Makefile-shared caches |
| `gofmt`, `jq empty 040-implementation-evidence/LANES-test-coverage.json`, `git diff --check` | PASS |

The initial ad-hoc race invocation used Go's restricted default cache and
stopped before compiling tests with `operation not permitted`; it was rerun
with the Makefile-shared cache paths and passed. This was an execution-context
failure, not a suppressed test failure. The first PATH lookup for `gofmt` also
failed because the toolchain is not on PATH; formatting then used the pinned
Go 1.27.1 toolchain's `gofmt` binary and passed.

## Source S and synchronized validation

Source S delivery and exact-S Ubuntu gates are pending.

Immediately before source delivery, all three Ubuntu checkouts were checked
over SSH with `IdentitiesOnly=yes`, `BatchMode=yes`,
`StrictHostKeyChecking=yes`, and `UpdateHostKeys=no`. The host keys were
already present in the Mac `known_hosts`; host-key mutation was disabled for
the check. Each checkout was clean on `dev` at predecessor E
`9b27d084afea60ac5bc2311b820f65ed133d7efd`.

| Host | Mac SSH alias and identity | Pre-S checkout |
| --- | --- | --- |
| `linux-host` | `remote-session-runner`; `/Users/tomasz.walczuk/.ssh/remote-session-runner` | `dev`, clean, `9b27d084afea60ac5bc2311b820f65ed133d7efd` |
| `sandbox-host` | `sandbox.env`; `/Users/tomasz.walczuk/.ssh/dev.slidestud.io` | `dev`, clean, `9b27d084afea60ac5bc2311b820f65ed133d7efd` |
| `factory-sandbox` | `factory-sandbox`; `/Users/tomasz.walczuk/.ssh/factory-sandbox` | `dev`, clean, `9b27d084afea60ac5bc2311b820f65ed133d7efd` |

Pre-pull verification command on each host was:
`ssh -i <identity> -o IdentitiesOnly=yes -o BatchMode=yes -o StrictHostKeyChecking=yes -o UpdateHostKeys=no <alias> 'hostname; cd /home/ubuntu/projects/remote-session-runner && git status --short --branch && git rev-parse HEAD'`.

## Evidence closeout and acceptance state

Pending source S and evidence-only closeout E. No production or live lane
acceptance is claimed. B021/B024 remain blocked and BUG-022/023 remain deferred.
