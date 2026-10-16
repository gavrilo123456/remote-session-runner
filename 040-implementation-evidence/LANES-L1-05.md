# LANES-L1-05 — successful fresh and replacement Dev/QAS transition graph

**State:** PASS — the hermetic registry contract and exact-S source delivery
passed. This is a source-contract result; it does not certify an installed
lane, live service transition, mailbox, or admission path.

## Predecessor and scope

- Ordered predecessor: `LANES-L1-04` evidence closeout E
  `f423f495c33c3ef3873b5c66d6653b0659dba86f`. L1-01 through L1-04 are
  recorded PASS in the ledger.
- Source S: `e56520701bb6346261b1c07640ad2b6c9c40c4cc` on Mac `dev`.
- This unit implements the successful fresh and replacement transition paths
  in the existing owner-only JSON lane registry. It does not add another
  SQLite/runtime authority. Old snapshots omit the optional `operations`
  field and remain readable.
- Durable CAS operation records bind UUIDv7 IDs, Dev/QAS lane, candidate and
  exact incumbent root/configuration identities, candidate-set digest, source
  revision, artifact and manifest digests, surface evidence and transition
  history. At most one unarchived operation may exist per Dev or QAS lane.
- Replacement requires complete four-surface incumbent closure, then a
  separate complete four-surface work inventory and zero-work postflight.
  Failed closure/quiescence attempts and deferred request/ACK references stay
  in the record. Candidate staging requires the successful quiescence gate;
  the successor must be a higher-epoch exact-root successor. Candidate
  activation and incumbent supersession share one registry CAS.
- Reopen preparation requires all three remote surfaces healthy and closed.
  Fresh authorization covers all four surfaces. `target_opening` is durable
  before open attestations, all remote opens precede aggregate Mac ingress,
  and archiving requires an authority-verified acceptance bound to the
  transition, lane, source, candidate set, live suite, and zero-work postflight.
  The code stores verification evidence supplied by the caller; it does not
  implement signing or live acceptance.
- Failed-open recovery, abort, and registry reconstruction remain later
  contracts (`LANES-L1-06` and onward); this unit does not implement or claim
  those paths.

## Source files and gates

The source commit contains only:

- `src/internal/laneregistry/operations.go`
- `src/internal/laneregistry/operations_test.go`
- `src/internal/laneregistry/registry.go`
- `src/internal/lanetests/manifest_test.go`
- `040-implementation-evidence/LANES-test-coverage.json`

On Mac (`mac-control-plane`), the configured Go 1.27.1 toolchain ran:

| Command | Result |
| --- | --- |
| `make test-lanes UNIT=LANES-L1-05` | PASS — all 7 registered H assertions |
| `make test` | PASS — `go test ./...` |
| `make vet` | PASS |
| `make build` | PASS |
| `make smoke` | PASS |
| `go test -race ./src/internal/laneregistry ./src/internal/lanetests` | PASS |

All three Ubuntu checkouts ran the same `make test-lanes UNIT=LANES-L1-05`,
`make test`, `make vet`, `make build`, and `make smoke` commands at exact S;
all passed. Focused `go test -race ./src/internal/laneregistry
./src/internal/lanetests` also passed on `linux-host` and `sandbox-host`.
`factory-sandbox` race was not run because the configured toolchain reports
`CGO_ENABLED=0` and `cc` is unavailable; this is not counted as a PASS.

## Git delivery and synchronized validation

Mac `dev` committed S as `phase(LANES-L1-05): add Dev QAS transition graph`.
The explicit-identity push to primary Gitea `origin/dev` succeeded. Direct
`git ls-remote` checks reported the exact S on both primary Gitea
`origin/dev` and automatic mirror `github/dev`.

Before pulling, each Ubuntu checkout was clean on `dev` at
`f423f495c33c3ef3873b5c66d6653b0659dba86f`, with `origin` set to the GitHub
mirror. Each fast-forwarded `origin/dev` using the configured explicit GitHub
identity, then ran the source gates above. Each ended clean on `dev` at exact S:

| Machine | Platform | H/full/vet/build/smoke | Focused race | Final checkout |
| --- | --- | --- | --- | --- |
| `linux-host` | Linux amd64 | PASS | PASS | clean `dev`, exact S |
| `sandbox-host` | Linux arm64 | PASS | PASS | clean `dev`, exact S |
| `factory-sandbox` | Linux amd64 | PASS | NOT RUN — no C compiler with `CGO_ENABLED=0` | clean `dev`, exact S |

No tracked files were edited or copied directly on Ubuntu. The builds and tests
used the synchronized Git revision. H fixtures used synthetic identities and
digests; they read no production credentials and published no Runner request
or ACK.

## Independent blockers preserved

- `LANES-PREP-00` remains **BLOCKED** and `LANES-L0` remains **BLOCKED / NOT
  STARTED**.
- `LANES-B021-02` and `LANES-B024-02` remain **BLOCKED**; their existing
  evidence and retained work were not changed.
- BUG-022/023 baseline repair units remain **DEFERRED / NOT STARTED**.
- No publisher pause, maintenance window, service/configuration/storage/firewall
  change, production Runner operation, mailbox publication, ACK, or lane
  runtime acceptance was performed for L1-05.
