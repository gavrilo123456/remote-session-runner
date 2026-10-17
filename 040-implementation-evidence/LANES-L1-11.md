# LANES-L1-11 — Mac/local-intent/importer/ACK admission and safe status

**State:** IN PROGRESS. This is hermetic source implementation and testing;
no installed or live lane acceptance is claimed.

## Predecessor and scope

- Ordered predecessor: `LANES-L1-10` evidence closeout E
  `7b48a042f31ef42e934eb330b13fdf806499ea97`, clean on Mac `dev`, Gitea,
  GitHub, and all three Ubuntu `dev` checkouts.
- L1-11 covers the remaining Mac local-intent and mailbox request/ACK
  admission paths, restart-closed behavior, and safe V3 health/status
  attestation. It must preserve the existing V1 mailbox response schema and
  V2 health output.
- `LANES-B021-02` and `LANES-B024-02` remain **BLOCKED**. Their current
  evidence blobs are respectively `3e9700485f48ad75ad42ba04334d5b3871a36c7a`
  and `15211861260284d0923c9d64259bd7746700399e`. Their maintenance, exact
  pair recovery, publisher pause and runtime acceptance are unrelated
  prerequisites and are not attempted here. BUG-022/023 remain deferred/open.
- Fixtures use temporary SQLite/mailbox roots and synthetic V3 digests. No
  production credential, mailbox publication, ACK, service, host setting,
  storage, firewall, publisher, or production Runner is used or changed.

## Pre-unit read inventory

Pre-unit HEAD was `7b48a042f31ef42e934eb330b13fdf806499ea97` on clean Mac
`dev`. The direct primary and mirror `dev` refs matched that SHA; all three
Ubuntu checkouts were clean on `dev` at the same SHA. Planning and predecessor
inputs were reloaded. The existing Mac local API, mailbox request processor,
ACK importer/store transaction, lane admission persistence, health handlers,
V1 response schema, and L1-10 test conventions were reviewed.

| Read input | Blob at pre-unit HEAD |
| --- | --- |
| `AGENTS.md` | `3402877131d9fcea513f5bae756582e4127ee892` |
| Lane initial idea | `503e54457228da265666ae48ceb2be22a0c0b81f` |
| Lane detailed design | `f7404c2d5a0b35706f3a9e867991bffcb6171d93` |
| Lane phased plan | `082e73ac62a7815c3ed15778eb8162d81a43c347` |
| Lane execution handbook | `d04a659df5c5834725ea05b9b047f4ad296b21bc` |
| Original V2 detailed design | `5659973dddaf886c9e872b24c2702dc8ae64d34e` |
| Original detailed design | `587654e9b71948519cc6a9e8d0c4813405e42e29` |
| Original phased plan | `260bc76b32da215bfc8ad0f1adf05889aa6c244f` |
| Execution ledger | `9ac8651473f69cc7d76bfbe0cdc1cd9ae3e8887f` |
| L1-10 predecessor evidence | `e78257a1d9eceb3b78a7b54b9d53e0a9957b3a06` |
| B021-02 blocker evidence | `3e9700485f48ad75ad42ba04334d5b3871a36c7a` |
| B024-02 blocker evidence | `15211861260284d0923c9d64259bd7746700399e` |
| H coverage manifest | `a4d1f8e24fc8abcff931f0ca26c1ad75d88f3383` |
| Test dispatcher tests | `1b0d4989e48afce4fc59ea201816b60af768fba9` |
| Store local intents | `bcabe4508ea94854abc7fa5d4d89af90d0912db7` |
| Store mailbox exchanges | `19764bcd1f4ac1a9362b863ed2b6f07893089bfc` |
| Store mailbox ACK | `f757c00d69b29dabce7ffaeb13f3c8026f44932e` |
| Store lane admission | `7ea327aac6e74da56b4a4b6d7c3ae9109978bc0a` |
| Mailbox ACK importer | `ac88154e16c3051b1307508a300ad2399b6c6ce1` |
| Mailbox session processor | `00ce6a5e080376961b907513e9011dca020de9e2` |
| V1 mailbox response schema | `be4f83e89fa63a7aab240582dc54bff1ac83448d` |
| Mac local API | `21479cf7d32814768374282d9cd502d5b7db3c90` |
| Mac mailbox adapter | `0a543fcf591d1d74f50e1bbad6003e0f738ab58e` |
| Health/status implementation | `9c37f54558b1d800890729e0ee41d340777685ec` |
| Health tests | `2f2e7d1c693782f1117b695dc4daa83ccb6fd6cb` |
| Build revision source | `6db5032483b28e28131a68f794255e66146528b2` |
| Mac private service health wiring | `b375d9644af6831d12817b71cbfe77bc21d4f7a0` |
| Linux private service health wiring | `891fa548395ab1d880a16c75073f4df6c1d302a0` |
| Linux direct service health wiring | `2dd88488c860e9906b07d280fe03a16ad225f1f5` |

## Implementation and validation plan

1. Gate new Mac local intents, newly imported mutating mailbox receipts, and
   imported ACKs inside their existing SQLite `BEGIN IMMEDIATE` transactions.
   Preserve exact known receipt replay and read-only mailbox operations; leave
   internal drain/reconciliation paths available. A closed gate after restart
   remains closed until exact runtime revalidation.
2. Map local API admission closure to a safe locked response. Make mailbox
   operation adapters treat it as retryable so the request pair remains
   available. ACK importer errors must retain both ACK files until durable ACK
   acceptance succeeds.
3. Add versioned V3 health/status routes that return only validated release
   lane, embedded source revision, SHA-256 artifact/configuration digests,
   schema version, readiness, and bounded admission status. Keep `/health/*`
   V2 JSON unchanged and do not modify the V1 mailbox schema.
4. Add registered H regressions for local/API and mailbox request admission,
   ACK retention/consumption, restart-closed behavior, safe attestation
   validation, and V1/V2 compatibility.

Required gates after source S: exact-unit H, full `make test`, `make vet`,
`make build`, `make smoke`, formatting/schema checks, and focused race tests
where the host has cgo and a C compiler. Run them on Mac and all three clean,
synchronized Ubuntu `dev` checkouts at exact S. Then deliver evidence-only E
through primary Gitea, exact GitHub mirror verification, and clean fast-forward
pulls to all three Ubuntu checkouts.

## Pre-S Mac source results

Execution machine: Mac control plane, authoritative checkout on `dev`, at the
pre-unit base plus the uncommitted L1-11 tree. Go `go1.27.1`; shared Go caches
from `Makefile`.

- `make test-lanes UNIT=LANES-L1-11 LANES_TIER=H` — PASS, 7 registered
  assertions. Early attempts exposed fixture mistakes (missing local API
  idempotency header, internal Go error JSON field casing, and a non-owner-only
  ACK fixture root); each was corrected and the full H gate rerun successfully.
- `make test` — PASS. The first managed-shell attempt failed the existing
  `TestBUG020MacShellExitWithInheritedControlWriterSettlesOnce` because its
  test-owned `/bin/ps` call was denied (`operation not permitted`). The isolated
  `runnerlocald` package and the full suite both passed with normal Mac process
  permissions. This failure was unrelated to L1-11.
- `make vet` — PASS.
- `make build` — PASS.
- `make smoke` — PASS.
- `GOCACHE=/private/tmp/remote-session-runner-gocache GOMODCACHE=/private/tmp/remote-session-runner-gomodcache GOTOOLCHAIN=local go test -race ./src/internal/store ./src/internal/mailbox ./src/internal/localapi ./src/internal/opshealth -run '^TestLanesL111' -count=1` — PASS on Mac (`CGO_ENABLED=1`, `/usr/bin/cc` available).
- `gofmt -l` over changed Go files, `jq empty 040-implementation-evidence/LANES-test-coverage.json`, and `git diff --check` — PASS.

## Source S and post-delivery gates

Source S commit, Gitea push, exact GitHub mirror verification, and all three
clean Ubuntu fast-forward pulls: NOT RUN. Required H/full/vet/build/smoke/race
validation on synchronized Ubuntu `dev` checkouts: NOT RUN. Evidence closeout E
and runtime installation/live acceptance: NOT RUN. L1-11 remains IN PROGRESS;
no unit PASS is claimed until S delivery, remaining source gates, and E delivery
are complete.

## Rollback and production impact

There is no installed V3 lane state to roll back. A source correction will use
a successor commit through the required Git workflow; no shared history will
be reset or force-pushed. This unit performs no production or service change.
