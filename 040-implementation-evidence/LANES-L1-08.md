# LANES-L1-08 — native/workspace request and ACK publisher fencing

**State:** IN PROGRESS — Mac source gates pass; source delivery and
synchronized-host validation are pending.

## Predecessor and pre-unit read inventory

- Ordered predecessor: `LANES-L1-07` evidence closeout E
  `d9c981cecb8ec146e718646e5754cbafc356157b`.
- Pre-unit HEAD: `d9c981cecb8ec146e718646e5754cbafc356157b`; Mac `dev` was
  clean before this unit began.
- `AGENTS.md`, the lane design, phased plan, execution handbook, ledger,
  L1-07 evidence, B021-02/B024-02 blocker evidence, mailbox publisher/fence
  implementation, coverage manifest, and dispatch tests were read at this
  HEAD. Their blob revisions are:

| Read input | Blob |
| --- | --- |
| `AGENTS.md` | `3402877131d9fcea513f5bae756582e4127ee892` |
| Lane detailed design | `f7404c2d5a0b35706f3a9e867991bffcb6171d93` |
| Lane phased plan | `082e73ac62a7815c3ed15778eb8162d81a43c347` |
| Lane execution handbook | `d04a659df5c5834725ea05b9b047f4ad296b21bc` |
| Lane execution ledger | `4f5d9beca7ee64ba5822529bfc7e5e3dc3ec7b63` |
| L1-07 predecessor evidence | `1f6dd8acc871fb17e984f5bd8a010792834d0d95` |
| B021-02 blocker evidence | `3e9700485f48ad75ad42ba04334d5b3871a36c7a` |
| B024-02 blocker evidence | `15211861260284d0923c9d64259bd7746700399e` |
| `src/internal/mailboxfence/fence.go` | `c31b0bc09d1ca9d2235668067e7813af39f32776` |
| `src/internal/mailboxfence/fence_test.go` | `30f51b7115bc3b5b0a0c5a8e9d42e90e6b3cad8f` |
| `src/internal/mailboxclient/client.go` | `5e66959cc9afa2af930fe7e3f66601e224e34040` |
| `src/internal/mailboxclient/client_test.go` | `938a80413ddcd57cfb8e757d2c17272a970b2ebb` |
| `src/internal/lanetests/manifest.go` | `b8b94c45bafb48811b0119bd71fa4186e7ffd18e` |
| `src/internal/lanetests/manifest_test.go` | `ed5ba2c52d9344efa12508cb53d844327ea7165d` |
| `040-implementation-evidence/LANES-test-coverage.json` | `853773ab67158a9536a11e26b0da925790b854ea` |

The source review confirmed the L1-08 requirement: a cooperating native or
workspace writer holds the shared lock from the migration-fence check through
durable JSON, marker-last ready file, and parent-directory fsync. A transition
uses the same lock exclusively while capturing request and ACK ingress. Raw
writers do not participate in this advisory lock and remain a stop condition
unless their owner proves a tested pause/fence control. L1-09 owns the
reversible PRD fence record and descriptor/digest deferred ledger.

## Scope

Implement the reusable source contract for request and ACK publication,
exclusive paired-ingress snapshots, and fail-closed migration-marker checks.
Add hermetic regressions for lock lifetime, snapshot consistency, incomplete
or raw-writer residue, crash boundaries, and publisher rejection after a
migration fence marker is present. All tests use temporary mailbox fixtures
and synthetic IDs. This does not pause a live publisher, change an installed
service, publish a Runner request/ACK, or inspect production mailbox roots.

## Implementation and registered regressions

- Native request and ACK publication now share the same root-relative
  descriptor path. The shared lock is acquired before the migration-marker
  check and remains held through owner-only JSON creation, empty ready marker
  creation, and ingress-directory fsync. Directory and pair-member opens use
  `openat` with no-follow and exclusive-create flags, so a path replacement
  cannot redirect the write outside the locked mailbox root.
- A present regular owner-only `.runner-mailbox-migration-fence` marker blocks
  shared publishers. Symlinked or unsafe markers fail closed. The migration
  transition can hold the same lock exclusively without changing service,
  directory permissions, or marker contents. Retirement fencing refuses to
  cross an active migration marker.
- `SnapshotIngressPairs` requires an exclusive lock and records both request
  and ACK pairs with root/directory/file device and inode identities, relative
  descriptor-derived paths, and SHA-256 digests. Incomplete pairs, unknown
  artifacts, symlinked members, non-owner files, and replaced names stop the
  snapshot. `RequireEmpty` refuses to pass while any complete pair remains.
- Raw workspace writers do not use the advisory lock. A regression writes a
  raw pair while holding the exclusive lock and proves the empty-ingress gate
  detects it. The actual L6 operator must still stop every uncontrolled raw
  writer and verify its tested pause/fence control before crossing the live
  fence; this hermetic test does not prove a production publisher pause.

The eight registered H assertions are:

1. `TestLanesL108RequestAndAckClientRefuseMigrationFence`
2. `TestLanesL108PublisherLockSpansJSONReadyAndDirectorySync`
3. `TestLanesL108InterruptedPublicationRetainsEvidenceAndCannotReplay`
4. `TestLanesL108ExclusiveSnapshotBindsCompleteRequestAndAckPairs`
5. `TestLanesL108ExclusiveSnapshotRejectsIncompleteOrUnsafePairs`
6. `TestLanesL108RawWriterBypassesAdvisoryLockAndEmptySnapshotStops`
7. `TestLanesL108MigrationFenceMarkerBlocksSharedPublishers`
8. `TestLanesL108UnsafeMigrationFenceMarkerFailsClosed`

The interruption tests stop after JSON/before ready and after ready/before
directory fsync. They verify the original bytes remain, an incomplete pair
fails the snapshot, a complete but uncertain pair fails the empty gate, and a
same-ID retry cannot replace or replay retained work.

Source changes in this unit are limited to:

- `040-implementation-evidence/LANES-L1-08.md` and
  `040-implementation-evidence/LANES-000-execution-ledger.md`
- `040-implementation-evidence/LANES-test-coverage.json`
- `src/internal/mailboxclient/client.go` and
  `src/internal/mailboxclient/lanes_l108_test.go`
- `src/internal/mailboxfence/fence.go`, `snapshot.go`, and
  `lanes_l108_test.go`
- `src/internal/lanetests/manifest_test.go`

## Mac source gates

Commands ran on `mac-control-plane` (Darwin arm64), Go 1.27.1, using the
Makefile's shared `GOCACHE=/private/tmp/remote-session-runner-gocache` and
`GOMODCACHE=/private/tmp/remote-session-runner-gomodcache`:

| Command | Result |
| --- | --- |
| `make test-lanes UNIT=LANES-L1-08 LANES_TIER=H` | PASS — all 8 registered H assertions |
| `make test` | PASS — complete `go test ./...` suite |
| `make vet` | PASS |
| `make build` | PASS |
| `make smoke` | PASS |
| focused `go test -race` for `mailboxfence`, `mailboxclient`, and lane manifest assertions | PASS |
| configured `gofmt`, coverage JSON parse, `git diff --check` | PASS |

Development history: an initial direct `go test` invocation bypassed the
Makefile cache configuration and failed to open the default Go build cache;
the same run also exposed a symlink-fixture path collision. After switching to
the configured Make gates, the first L1-08 H run found an error-shadowing bug
in the injected crash boundary. The fixture collision and shadowing bug were
fixed; the final H, full suite, vet, build, smoke, and race gates all passed.
These were test harness/development invocation failures, not accepted product
failures. The final lock-race assertion initially expected a raw context
deadline; the lock API deliberately maps a timed-out fence wait to `ErrBusy`,
so the test was corrected to assert that public outcome and rerun successfully.

## Source and host gates

Source S, Gitea/GitHub verification, all three clean Ubuntu fast-forwards, and
host gates are pending. Record them here before the evidence-closeout E.

## Independent blockers preserved

- `LANES-B021-02` remains **BLOCKED**; evidence and retained work are
  preserved.
- `LANES-B024-02` remains **BLOCKED**; evidence and retained work are
  preserved.
- `LANES-PREP-00` remains **BLOCKED**; `LANES-L0` remains **BLOCKED / NOT
  STARTED**. BUG-022/023 baseline repair units remain deferred.
- No publisher state, service, runtime configuration, storage, firewall rule,
  production credential, mailbox file, Runner request, or ACK was changed for
  L1-08.
