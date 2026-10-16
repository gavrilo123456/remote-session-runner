# LANES-L1-06 — failed-open recovery, exact-incumbent recovery and abort

**State:** PASS — the hermetic recovery contract and exact-S source delivery
passed. This is a source-contract result; it does not certify installed lane
services, live transition acceptance, or B021/B024 maintenance.

## Predecessor and scope

- Ordered predecessor: `LANES-L1-05` evidence closeout E
  `d8cbfc70eb1c6f32e294f1059dc4ea0e7f8c4ae4`. L1-01 through L1-05 are
  recorded PASS in the ledger.
- Source S: `ec2db7dbe8b9a45d5af486f2587a2954cc343f4b` on Mac `dev`.
- This unit adds durable failed-open evidence, failed-closed abort and exact
  incumbent recovery transitions to the owner-only JSON lane registry. It
  keeps failure, closure and quiescence evidence attached to the operation;
  it does not replay requests or mutate mailboxes.
- A failure before target opening requires terminal closed and zero-work
  proofs before abort and archive. A failure after opening records all known
  directives and high-water tuples, then requires target closure and complete
  quiescence before abort, recovery, or archive.
- Recovery is allowed only for the exact previously active incumbent and
  creates a new, higher-epoch prepared successor. The closed failed target is
  never reactivated. The recovery candidate remains prepared through reopen
  and becomes active only in the same registry CAS that records fresh lane
  acceptance; the old incumbent remains superseded. Recovery failure follows
  closure/quiescence and archive rules, retaining the evidence.
- Validation rejects unsafe or inconsistent evidence, stale epochs, missing
  high-water facts, mismatched identity/digests and invalid transition links.
  Legacy snapshots without the optional operations field remain readable.

## Source files and registered regressions

Source S contains only:

- `040-implementation-evidence/LANES-test-coverage.json`
- `src/internal/laneregistry/operations.go`
- `src/internal/laneregistry/operations_test.go`
- `src/internal/lanetests/manifest_test.go`

The six registered H assertions are:

1. `TestLanesL106FreshFailureNeedsClosedProofBeforeAbortAndArchive`
2. `TestLanesL106ReplacementMayAbortOnlyBeforeIncumbentClosureWithClosedProof`
3. `TestLanesL106PostOpenFailureQuiescesBeforeExactIncumbentRecovery`
4. `TestLanesL106PreOpenRecoveryFailureSealsRecoveryAbort`
5. `TestLanesL106RecoveryOpenFailureClosesAndQuiescesBeforeArchive`
6. `TestLanesL106RejectsUnsafeFailureAndClosureEvidence`

The recovery regression verifies cleanup before recovery, exact-incumbent
identity, fresh authorization and epoch, no pointer restoration, retention of
failure/high-water/deferred-ACK evidence, and atomic promotion only after fresh
acceptance. Other cases cover pre-open failure, replacement abort boundary,
recovery failure and rejection of incomplete or inconsistent evidence.

## Mac source gates

Commands ran on `mac-control-plane` (Darwin arm64) with Go 1.27.1 and the
Makefile's shared `GOCACHE=/private/tmp/remote-session-runner-gocache` and
`GOMODCACHE=/private/tmp/remote-session-runner-gomodcache`:

| Command | Result |
| --- | --- |
| `make test-lanes UNIT=LANES-L1-06 LANES_TIER=H` | PASS — all 6 registered H assertions |
| `make test` | PASS — complete `go test ./...` suite |
| `make vet` | PASS |
| `make build` | PASS |
| `make smoke` | PASS |
| `go test -race ./src/internal/laneregistry -run 'TestLanesL106' -count=1` | PASS |
| `gofmt`, coverage JSON validation, `git diff --check` | PASS |

The final H gate and full suite were rerun after the last source adjustment.

## Source delivery and synchronized Ubuntu validation

Source S was committed on Mac `dev` as
`phase(LANES-L1-06): implement failed-open recovery graph`. The explicit-
identity push to primary Gitea `origin/dev` succeeded; direct `git ls-remote`
checks of primary Gitea and automatic mirror `github/dev` both returned exact
S. Mac `dev` was clean at S before the evidence closeout.

Each Ubuntu checkout was clean on `dev` at the exact S before and after
validation. Each checkout ran the same H, full test, vet, build, and smoke
commands listed above at S; every gate passed. The focused race test also
passed on `linux-host` and `sandbox-host`. `factory-sandbox` race could not run:
its configured Go 1.27.1 reports `CGO_ENABLED=0`, and no `cc` executable is
installed. This is recorded as unavailable, not as a PASS.

| Machine | Platform | H / full / vet / build / smoke | Focused race | Final checkout |
| --- | --- | --- | --- | --- |
| `linux-host` | Linux amd64 | PASS | PASS | clean `dev`, exact S |
| `sandbox-host` | Linux arm64 | PASS | PASS | clean `dev`, exact S |
| `factory-sandbox` | Linux amd64 | PASS | NOT RUN — `CGO_ENABLED=0`, no `cc` | clean `dev`, exact S |

SSH fallback was used because these are checkout-local Go gates, not mailbox
work. Mac used the configured explicit identities for `remote-session-runner`
(`~/.ssh/remote-session-runner`), `sandbox.env` (`~/.ssh/dev.slidestud.io`),
and `factory-sandbox` (`~/.ssh/factory-sandbox`), with batch mode, strict
verified host keys, and `UpdateHostKeys=no`. Each Ubuntu Git pull used
`/home/ubuntu/.ssh/gavrilo123456-github`, `IdentitiesOnly=yes`, batch mode,
and strict host-key checking. No tracked source was edited or copied on
Ubuntu. No Runner request or ACK was published.

## Independent blockers preserved

- `LANES-B021-02` remains **BLOCKED**. Its evidence file is unchanged at blob
  `3e9700485f48ad75ad42ba04334d5b3871a36c7a` from the L1-05 E predecessor
  through source S. Maintenance, exact-pair recovery, publisher pause, and
  installed acceptance remain open.
- `LANES-B024-02` remains **BLOCKED**. Its evidence file is unchanged at blob
  `15211861260284d0923c9d64259bd7746700399e` from the L1-05 E predecessor
  through source S.
- `LANES-PREP-00` remains **BLOCKED** and `LANES-L0` remains **BLOCKED / NOT
  STARTED**. BUG-022/023 baseline units remain deferred.
- No publisher pause, maintenance window, service/configuration/storage/
  firewall change, production Runner operation, retained-work mutation, or
  live lane acceptance was performed for L1-06.
