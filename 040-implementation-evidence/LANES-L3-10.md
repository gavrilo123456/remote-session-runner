# LANES-L3-10 — root signer, agent, approval and replay journal

**State:** BLOCKED — Source S and all H source gates pass on the Mac and three
Ubuntu checkouts. Required F acceptance is NOT RUN and remains blocked on the
separately approved disposable privileged Mac fixture. L3-09/L3-08 ordered
acceptance and evidence closeout are also blocked; no L3-10 PASS or E is
claimed.

## Pre-unit record and scope

- Pre-unit Mac `dev` HEAD was `43cb972617c974a6738db83cbe51f3e717212d5e`,
  clean before this unit. This is the delivered L3-09 evidence closeout C.
- L3-09 source S `359629128250ee01585259be2c816e86449e3a4c` and its H/N/full
  source gates are delivered on the three Ubuntu checkouts. Its acceptance and
  E remain blocked by L3-08's missing cache/backup F fixtures and E.
- The owner-authorized successive source continuation applies. B021/B024
  maintenance, publisher pause, production credentials, a maintenance window,
  service changes, and BUG-022/023 baseline work are not prerequisites here.
  B021/B024 remain BLOCKED and BUG-022/023 remain DEFERRED.
- All state, key and payload tests use temporary roots, synthetic signing
  identities, a fake interactive-authorizer response, and simulated Mac agent
  evidence. The real Authorization Services provider was compiled but never
  invoked. No production key, service, socket, mailbox, host configuration,
  storage, firewall or credential was read or changed.

## Read inputs at the predecessor

| Input | Blob at pre-unit HEAD |
| --- | --- |
| `AGENTS.md` | `3402877131d9fcea513f5bae756582e4127ee892` |
| Lane detailed design | `f7404c2d5a0b35706f3a9e867991bffcb6171d93` |
| Lane phased plan | `082e73ac62a7815c3ed15778eb8162d81a43c347` |
| Lane execution handbook | `d04a659df5c5834725ea05b9b047f4ad296b21bc` |
| Execution ledger | `4ab0a5e6cadf7d572d744b95bc7061a4e22f84c1` |
| Lane coverage manifest | `5b9647af81cfa457a79c003c26c3823ca5eb6ee4` |
| Existing Mac peer validation | `7f30126159f530512be1050454d8c19dd6957448` |
| Security endpoint contract | `bf09eccd271bbcc20e37bd31a054dba4447623ea` |
| Envelope roles and signer API | `31a032fc8b7e726c681b2f2d295f556a9d655653` |
| Closed payload schema validator | `20602b239115a11034bb4326862f27b37d44ec5b` |
| Existing full-sync root anchor patterns | `e41b0fb24e739ad0eecee7470d2c9b9cd5fbd698` |
| Authority/key/ledger trust validators | `e463407256c6ea710dd663a33d8021dea6a5e80b` |

## Source implementation

- Added a fixed Mac authority-state location under
  `/Library/Application Support/RemoteSessionRunner-release-authority-signer/state/`.
  The SQLite state database is accepted only as a root-owned `0600` regular
  file in a root-owned `0700` directory, configured with DELETE journaling and
  EXTRA synchronous durability, and checked against a contiguous commit chain.
  Bootstrap public-key identity, pending plans, exact mandates, consumed
  receipts/envelopes, accepted record IDs and transition context are preserved
  in that root-only state. It is outside lane roots, caches and continuity
  bundles. Private key bytes are not stored in the state database.
- The fixed key provider reads only the Mac authority signer's fixed root-owned
  `0600` Ed25519 key path. It verifies the loaded public key against the pinned
  state key; the signer copies and clears its transient private-key buffer.
- The agent stages only canonical, schema-validated authority record bodies.
  Mandates bind record type, exact body SHA-256, derived candidate/transition/
  predecessor context, current ledger generation and short expiry. A mandate
  cannot be created unless the injected interactive authorizer succeeds; its
  receipt and exact body digest are recorded before the opaque mandate ID is
  returned.
- The Mac production authorizer uses Authorization Services to request the
  interactive administrator right for the exact displayed record type, body
  digest, context digest and ledger generation. It returns only an opaque
  receipt; the right and credentials are not retained.
- The signer accepts no caller body. It validates the exact authenticated
  `runner-release-authority-agent` launchd/audit-token/code identity through
  the existing Mac Security.framework peer verifier, fetches the staged plan,
  revalidates schema/context/expiry/ledger/key, and signs only the closed
  authority record map. Host-attestation, unknown and arbitrary record types
  are denied. `authority_key_set` is signed under the named previous-authority
  role and must name the pinned previous key.
- Approval consumption, the signed result and accepted record ID are committed
  atomically. A retry by the authenticated agent returns the identical retained
  envelope and does not sign again. Missing commit-chain entries, wrong peer,
  wrong key, stale ledger, expired approval, approval denial or malformed/open
  body fail closed without consuming a valid pending approval.

This source slice supplies the journal, approval and signer control logic. It
does not install or launch a production daemon, open a signer socket, create
production authority keys, or perform a release transition.

## Mac source gates

At pre-source-S worktree `43cb972617c974a6738db83cbe51f3e717212d5e` plus the
uncommitted L3-10 changes:

| Command | Result |
| --- | --- |
| `make test-lanes UNIT=LANES-L3-10 LANES_TIER=H` | PASS — 6 registered assertions |
| `make check build` | PASS — all Go tests, vet and command builds |
| `go test -race ./src/internal/release -run '^TestLanesL310' -count=1` | PASS |
| pinned `gofmt`, coverage JSON parse, `git diff --check` | PASS |

The first test run exposed a fixture-clock mismatch: approval expiry used the
wall clock while signer verification used the fixed test clock. The fixture now
injects one deterministic clock into both paths; the full registered H run and
the focused race run pass after that correction. No production failure was
observed or changed.

## Required F gate and ordered blockers

L3-10 F is NOT RUN. The handbook requires a separately approved disposable
privileged fixture. Exact remaining F evidence is a temporary root-owned
authority state/key tree and test launchd job/socket, real audit-token and code
identity denial cases, actual interactive Authorization Services approval and
denial, process restart/replay proof, and a host-management replacement drill
showing that the original anchor, consumed approvals, accepted IDs and
high-water state survive unchanged. The fixture must contain no production key,
service, path or release record. A concrete fixture manifest/approval is not
available yet. This is a later privileged fixture dependency, not a reason to
block the independent source/H work or modify a production service.

The ordered L3-09/L3-08 acceptance and evidence-closeout chain remains blocked
by L3-08's absent physical cache/backup F fixtures. This unit therefore has no
PASS, installed acceptance, root signer deployment, live mandate, or E claim.

## Source S delivery and host validation

Source S is `41c582f2463b7f49651f4855e29c0a1638393037`
(`phase(LANES-L3-10): add bounded authority signing state`). Mac `dev` was
clean at S. Gitea `origin/dev` and the automatic GitHub mirror `github/dev`
both resolved to exactly S. Each required Ubuntu checkout was clean on `dev`
at exactly S before validation and remained at S after validation:

| Required machine | SSH profile / Makefile host | Final branch and HEAD | H / full source gates | Focused race |
| --- | --- | --- | --- | --- |
| `linux-host` | `remote-session-runner` / `oracle-yuta-konopka-ubuntu-micro-02` | clean `dev`, S | `LANES_NATIVE_HOST_ID=linux-host make test-lanes UNIT=LANES-L3-10 LANES_TIER=H`: PASS (6 assertions); `make check build`: PASS | PASS with pinned Go 1.27.1 and `/usr/bin/cc` |
| `sandbox-host` | `sandbox.env` / `oracle-gustaw-janecki-ubuntu-flex-02` | clean `dev`, S | `LANES_NATIVE_HOST_ID=sandbox-host make test-lanes UNIT=LANES-L3-10 LANES_TIER=H`: PASS (6 assertions); `make check build`: PASS | PASS with pinned Go 1.27.1 and `/usr/bin/cc` |
| `factory-sandbox` | `factory-sandbox` / `oracle-waclaw-dziwulski-ubuntu-micro-01` | clean `dev`, S | `LANES_NATIVE_HOST_ID=factory-sandbox make test-lanes UNIT=LANES-L3-10 LANES_TIER=H`: PASS (6 assertions); `make check build`: PASS | NOT RUN: pinned Go reports `CGO_ENABLED=0`; no `cc` is installed |

The focused command on the two race-capable Ubuntu hosts was
`GOTOOLCHAIN=local /home/ubuntu/.local/share/remote-session-runner/toolchains/go1.27.1/bin/go test -race ./src/internal/release -run '^TestLanesL310' -count=1`.
The factory limitation is recorded as NOT RUN, not PASS. All Ubuntu commands
ran over the configured explicit SSH identity with strict host-key checking;
the Linux Mac-side SSH client emitted a known_hosts update-permission warning,
but strict verification and the command succeeded.

No production service, key, socket, mailbox, host configuration, storage,
firewall, or credential was read or changed. The required F gate remains
unavailable and blocks L3-10 PASS/E; the verified S/H work can support the
owner-authorized continuation of source-only units that do not depend on F.
