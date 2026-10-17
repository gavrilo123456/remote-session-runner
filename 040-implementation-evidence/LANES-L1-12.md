# LANES-L1-12 — Exclusive PRD group, maintenance and typed linked recovery

**State:** PASS. This unit implements and tests the durable PRD operation
group with isolated registry fixtures. It does not perform PRD maintenance or
claim installed/live acceptance.

## Predecessor and scope

- Ordered predecessor: `LANES-L1-11` evidence closeout E
  `258c2a795e1a3be228c61864893eb0b4dbd6e967`, clean on Mac `dev`, Gitea,
  GitHub, and all three Ubuntu `dev` checkouts.
- L1-12 implements exclusive PRD maintenance ownership, closure and quiescence
  gates, typed linked restore/forward-repair children, strictly increasing
  per-surface epochs, and fresh child-bound acceptance. The operation-group
  record is additive to the optional JSON registry fields; it is not a second
  runtime database or a production mutation interface.
- `LANES-B021-02` and `LANES-B024-02` remain **BLOCKED**, with evidence
  preserved at their pre-unit blobs below. Their maintenance, publisher pause,
  candidate refresh, and live acceptance are not prerequisites for this
  isolated source unit. BUG-022/023 baseline repair rows remain deferred/open.
- Tests use temporary registry roots and synthetic candidate/evidence
  identities. No production credential, mailbox publication, ACK, service,
  host setting, storage, firewall, publisher, or production Runner is used or
  changed.

## Pre-unit read inventory

Pre-unit HEAD was `258c2a795e1a3be228c61864893eb0b4dbd6e967` on clean Mac
`dev`; Gitea `origin/dev` and GitHub `dev` matched. All three Ubuntu checkouts
were clean on `dev` at the same SHA. The plan, design, handbook, ledger,
predecessor evidence, and current blocked-unit records were reloaded. The
registry snapshot validation/update path, existing Dev/QAS operation state
machine, PRD migration fence representation, and H-test manifest/dispatcher
were reviewed.

| Read input | Blob at pre-unit HEAD |
| --- | --- |
| `AGENTS.md` | `3402877131d9fcea513f5bae756582e4127ee892` |
| Lane initial idea | `503e54457228da265666ae48ceb2e22a0c0b81f` |
| Lane detailed design | `f7404c2d5a0b35706f3a9e867991bffcb6171d93` |
| Lane phased plan | `082e73ac62a7815c3ed15778eb8162d81a43c347` |
| Lane execution handbook | `d04a659df5c5834725ea05b9b047f4ad296b21bc` |
| Execution ledger | `3460a6d2a976e678c64b552dec0d3bfcc7659324` |
| L1-11 predecessor evidence | `5a37261e239f58e5e3d8138b58bd5f24341f0257` |
| B021-02 blocked evidence | `3e9700485f48ad75ad42ba04334d5b3871a36c7a` |
| B024-02 blocked evidence | `15211861260284d0923c9d64259bd7746700399e` |
| H coverage manifest | `899c1660c56096a6281c2fa4f858c566558f4738` |
| Registry persistence and snapshot validation | `b9c3ddcfa077d1babec3c9a388c6f6a2fa05d24b` |
| Dev/QAS operation graph and validation | `da6c45bc8149005e4be59d7201c5c98e40c95e2f` |
| PRD migration fence representation | `00b09e996712e8017a11efa2ae73e6f1102bb531` |
| H dispatcher manifest checks | `60cdf78c754a0eb0978271cb32a4ce961819d37d` |

## H proof plan

1. Verify one durable unarchived PRD group excludes another primary.
2. Reject staging/recovery before all four closed-surface proofs and complete
   zero-work quiescence; retain a failed maintenance parent in the group.
3. Accept only typed restore or explicitly authorized forward-repair children
   linked to that parent; reject a second active child and non-increasing
   per-surface epochs.
4. Require fresh exact child candidate-closed/open attestations and a new
   authority-verified zero-work `lane_acceptance` bound to that child before
   atomically archiving parent and child and releasing the group.

## Implementation

- Added an optional durable `prd_operation_groups` field to the existing
  owner-only registry snapshot. Group updates use the existing registry
  lock, generation compare-and-swap, fsync and snapshot validation. No SQLite
  runtime authority was added.
- The PRD group is exclusive against both other V3 groups and the existing V2
  migration fence. A maintenance parent records all four closed surfaces,
  all four quiescence proofs, the four work-class dispositions, retained
  deferred request/ACK references, and a zero-work postflight before staging.
- Maintenance and typed restore/forward-repair paths require increasing
  per-surface epochs, complete candidate-closed proofs, remote transport-ready
  proofs, fresh per-surface authorizations, remote-first open proofs, and a
  new transition-bound PRD acceptance. The parent and accepted child archive
  atomically. A pre-open failed child archives under the still-held parent;
  the next child must be typed and use higher epochs.
- Validation checks the persisted graph and evidence references structurally.
  Authority signatures and host attestations remain verified by their
  separate owners; H fixtures do not claim those external verifications.
- Five registered H assertions cover maintenance closure/quiescence, typed
  recovery, exclusivity and epoch progression, fresh child acceptance and
  atomic archive, altered persisted-state rejection, and normal maintenance
  acceptance/archive.

## Mac source validation

Commands ran in the authoritative Mac checkout with Makefile Go `go1.27.1`
and the shared `/private/tmp/remote-session-runner-gocache` and
`/private/tmp/remote-session-runner-gomodcache`:

- `make test-lanes UNIT=LANES-L1-12 LANES_TIER=H` — PASS, five registered
  assertions.
- `make test` — PASS across all packages. The first run caught the dispatcher
  regression check still expecting L1-12 to have no assertions; it now checks
  the still-unstarted L1-13 unit, and the full suite passed on rerun.
- `make vet`, `make build`, and `make smoke` — PASS.
- Focused race: `go test -race ./src/internal/laneregistry -run
  '^TestLanesL112' -count=1` — PASS; cgo and Mac compiler available.
- `gofmt -l`, `jq empty 040-implementation-evidence/LANES-test-coverage.json`,
  and `git diff --check` — PASS.

## Source S and synchronized validation

Source S is `6eb6b3a195045023449cc32df135ad960b162145` on Mac `dev`.

- Mac commit: `phase(LANES-L1-12): model exclusive PRD recovery graph`.
- The Mac pushed with
  `git -c core.sshCommand='ssh -i /Users/tomasz.walczuk/.ssh/local-gitea-key -o IdentitiesOnly=yes -o BatchMode=yes -o StrictHostKeyChecking=yes' push origin dev`.
  The push to primary Gitea `origin/dev` succeeded. Updating the local
  `refs/remotes/origin/dev` tracking ref was denied by the managed filesystem;
  `git -c core.sshCommand='ssh -i /Users/tomasz.walczuk/.ssh/local-gitea-key -o IdentitiesOnly=yes -o BatchMode=yes -o StrictHostKeyChecking=yes' ls-remote origin refs/heads/dev`
  verified Gitea at exact S.
- Direct `ls-remote github refs/heads/dev` with the configured GitHub identity
  via
  `git -c core.sshCommand='ssh -i /Users/tomasz.walczuk/.ssh/gavrilo123456-github -o IdentitiesOnly=yes -o BatchMode=yes -o StrictHostKeyChecking=yes' ls-remote github refs/heads/dev`
  verified the automatic mirror at exact S. The mirror was not pushed manually.
- Each Ubuntu checkout was clean on `dev` at predecessor E
  `258c2a795e1a3be228c61864893eb0b4dbd6e967` before pull. Each fast-forwarded
  `origin/dev` with `/home/ubuntu/.ssh/gavrilo123456-github`,
  `IdentitiesOnly=yes`, `BatchMode=yes`, and `StrictHostKeyChecking=yes`, using
  `git -c core.sshCommand='ssh -i /home/ubuntu/.ssh/gavrilo123456-github -o IdentitiesOnly=yes -o BatchMode=yes -o StrictHostKeyChecking=yes' pull --ff-only origin dev`.
  Each is clean on `dev` at exact S after validation.

| Host | SSH alias and explicit login identity | H | Full `make test` | `make vet` | `make build` | `make smoke` | Focused race |
| --- | --- | --- | --- | --- | --- | --- | --- |
| Mac `mac-control-plane` | Authoritative Mac checkout | PASS | PASS | PASS | PASS | PASS | PASS; cgo and `/usr/bin/cc` available |
| `linux-host` | `remote-session-runner`; `/Users/tomasz.walczuk/.ssh/remote-session-runner` | PASS | PASS | PASS | PASS | PASS | PASS |
| `sandbox-host` | `sandbox.env`; `/Users/tomasz.walczuk/.ssh/dev.slidestud.io` | PASS | PASS | PASS | PASS | PASS | PASS |
| `factory-sandbox` | `factory-sandbox`; `/Users/tomasz.walczuk/.ssh/factory-sandbox` | PASS | PASS | PASS | PASS | PASS | NOT RUN; `CGO_ENABLED=0`, no `cc` |

On all three Ubuntu hosts the exact commands were `make test-lanes
UNIT=LANES-L1-12 LANES_TIER=H`, `make test`, `make vet`, `make build`, and
`make smoke`. Linux and sandbox also ran
`/home/ubuntu/.local/share/remote-session-runner/toolchains/go1.27.1/bin/go test -race ./src/internal/laneregistry -run '^TestLanesL112' -count=1`.
The Mac ran the same unit/full/vet/build/smoke gates and focused race at exact
S using the Makefile's Go 1.27.1 toolchain and shared Go caches. The formatting,
coverage JSON, and committed-diff checks passed on Mac. Validation ran from
`/home/ubuntu/projects/remote-session-runner` at exact S on Ubuntu. Factory
race was unavailable because that host has `CGO_ENABLED=0` and no C compiler.

The first full Mac suite run exposed a stale H-dispatch test expectation; it
was corrected to expect unstarted `LANES-L1-13` and the full suite then passed.
The earlier temporary-fixture setup errors were also corrected before S.

## Evidence closeout and acceptance state

This evidence and the ledger update are the evidence-only closeout E. The
source S revision and all four checkout validations are synchronized. L1-12
passes its source unit. No production maintenance, service installation, or
live lane acceptance is claimed. `LANES-B021-02` and `LANES-B024-02` remain
BLOCKED and their preserved evidence is unchanged; BUG-022/023 remain
DEFERRED.
