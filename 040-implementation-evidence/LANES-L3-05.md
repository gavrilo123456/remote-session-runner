# LANES-L3-05 — root anchor state and stale-ledger recovery

**State:** BLOCKED — source S and its H/N source gates are delivered and
verified. Ordered predecessor L3-04 remains BLOCKED; this unit is not PASS and
has no acceptance or evidence closeout E. No helper was installed and no host
service or production state was changed.

## Pre-unit record

- Pre-unit Mac `dev` HEAD: `a9aa3f38f15f21c5d1dbb550bb12032869d025d2`.
- Source predecessor: L3-04 source S `90c17965eb647863d0744d1715e05c2c025c2289`.
- L3-04 evidence at pre-unit HEAD: `040-implementation-evidence/LANES-L3-04.md`, blob `7e9ead64b8f58a267df7eb617628e850e6251d5a`; it is BLOCKED, without PASS or E. Its ordered chain remains blocked by L3-03/L3-02/L3-01 and L2-09 native F; L2-09 depends on the unprovisioned L2-07 Linux and L2-08 Mac fixtures. No predecessor E SHA is asserted.
- The owner authorized successive independent source-only units. L3-05 uses the delivered L3-04 trust validators and isolated test identities; its source work does not need L3-04 native acceptance, the L2-07/08 fixtures, production credentials, publisher pause, a maintenance window, a mailbox transaction, or service changes.
- B021/B024 remain BLOCKED with their existing evidence and held work. BUG-022/023 remain deferred. The maintenance package remains its existing snapshot; it was not rebuilt.

## Pre-unit read inventory

Blob IDs are from the pre-unit HEAD above.

| Source | Blob |
| --- | --- |
| `AGENTS.md` | `3402877131d9fcea513f5bae756582e4127ee892` |
| Lane initial idea | `503e54457228da265666ae48ceb2be22a0c0b81f` |
| Lane detailed design | `f7404c2d5a0b35706f3a9e867991bffcb6171d93` |
| Lane phased plan | `082e73ac62a7815c3ed15778eb8162d81a43c347` |
| Lane execution handbook | `d04a659df5c5834725ea05b9b047f4ad296b21bc` |
| Lane execution ledger | `5529fa182dea96c20c6a0bd55d5f0217d667fabf` |
| L3-04 predecessor evidence | `7e9ead64b8f58a267df7eb617628e850e6251d5a` |
| L2-07 blocker evidence | `4b6555ec34039b13b2846d46891d6750850f0a36` |
| L2-08 blocker evidence | `871fea76b91ed41f0536b89886c4c6942980e929` |
| `src/internal/release/l3_trust.go` | `cd0a5ecc3b44e9bcd7db5aed9345fc7a9e2a74d5` |
| `src/internal/release/envelope.go` | `31a032fc8b7e726c681b2f2d295f556a9d655653` |
| `src/internal/release/lanes_l304_test.go` | `31a44a869b6fd9223864077db73d64abe150e927` |
| `src/internal/release/l3_payload.go` | `f3cb21f3b0e5e8012face0592bdaae2962a25413` |
| `src/internal/release/schemas/v1/l3_payloads.schema.json` | `f8a6c9d770e358ed2a341f07feb6b4b83c8f26b1` |
| `src/internal/lanetests/manifest_test.go` | `e3a206a41781a31be82df9812b1598c4ebbfac7f` |
| `040-implementation-evidence/LANES-test-coverage.json` | `2f32b19dee15b202b725f6b90f8ab36448a6bc8f` |
| `Makefile` | `39cad4746bc66d27cafd249a9d7d3885f411dccc` |
| `go.mod` | `3073f69b23c7f8c851bda3d4ac299a5f81108e34` |

## Source deliverable

`src/internal/release/security_anchor.go` adds a fixed-path production opener
for the Mac and Linux security-anchor directories and an initialize-once
bootstrap operation. It requires a root helper, an owned real `0700` directory,
an owned regular `0600` SQLite file, and only the database or its SQLite
rollback journal in that directory. Existing databases cannot be bootstrapped
over or imported. SQLite uses `journal_mode=DELETE` and `synchronous=EXTRA`;
each accepted state update uses a generation compare-and-swap, a digest-chained
commit row, a full-sync transaction, database-file sync, and parent-directory
sync. A corrupt, forked, lower, wrong-owner, wrong-mode, or symlinked state
fails closed.

The protected snapshot retains the authority and host-attestation key chains,
revocation ledger, highest accepted authority time, per-lane directive tuple
and its transition/candidate/configuration/record identities, active runtime,
and pending candidate. Verified directive high-water and accepted-runtime
changes are persisted atomically; pending candidate state is removed only when
the matching accepted runtime advances to active. Continuity restore validation
never imports anchor state and rejects a lower or conflicting trust/time/
directive/runtime high-water.

The helper time-probe path persists one challenge at a time with its random
nonce, wall `t0`, monotonic `CLOCK_MONOTONIC` `t0`, and challenge ID before
returning it. The existing L3-04 verifier owns `t1`, checks the signature,
nonce, elapsed time, skew, and rollback, and the anchor durably advances the
highest authority time. Challenges that were pending when the helper restarts
are invalidated because their in-process monotonic interval can no longer be
verified.

Each trust-record application consumes exactly one unused helper time probe.
Key-set and revocation updates use the L3-04 signature, predecessor,
generation, overlap, cumulative-revocation, and expiry validators. If the
cached ledger is stale, only a contiguous `authority_key_set` or
`release_revocation_ledger` successor is accepted; candidates, host key sets,
directives, and other records remain closed until a fresh ledger is durable.
The directive/runtime persistence methods accept validated results from their
future helper call paths; this source unit does not add peer authentication or
install a runtime service.

During test development, an initial bootstrap fixture used a fixed timestamp
that had fallen outside its synthetic key interval; the fixture now derives its
base from the current UTC time with bounded synthetic validity. An early probe
sequence also exposed that a second accepted time proof could precede
consumption of the first; the anchor now permits only one outstanding
challenge and one unconsumed accepted proof. The final registered gates below
were rerun after both corrections.

Eight registered H assertions cover bootstrap permissions and persistence,
single-use time challenges, directive/pending/active high-water, interrupted
transactions, corruption/symlink/mode refusal, stale-ledger acceptance and
rejection, authority-key successor recovery, and rollback/forked continuity
snapshots. One registered N assertion exercises the native database and
directory fsync path and rejects a lower-ledger restore. All identities are
synthetic test keys and all storage is under isolated temporary directories.

## Mac pre-delivery source gates

Execution machine: `mac-control-plane` (Darwin arm64), Makefile-pinned Go
1.27.1, shared Go caches.

| Command | Result |
| --- | --- |
| `make test-lanes UNIT=LANES-L3-05` | PASS — 8 registered H assertions |
| `LANES_NATIVE_HOST_ID=mac-control-plane make test-lanes UNIT=LANES-L3-05 LANES_TIER=N` | PASS — 1 registered N assertion |
| `make test-lanes UNIT=LANES-L3-04` | PASS — 8 predecessor H assertions |
| `GOCACHE=/private/tmp/remote-session-runner-gocache GOMODCACHE=/private/tmp/remote-session-runner-gomodcache GOTOOLCHAIN=local '/Users/tomasz.walczuk/Library/Application Support/RemoteSessionRunner/toolchains/go1.27.1/bin/go' test -race ./src/internal/release -run '^TestLanesL305' -count=1` | PASS |
| `make check build` | PASS — full test suite, vet, smoke, and command builds |
| `python3 -m json.tool 040-implementation-evidence/LANES-test-coverage.json` and `git diff --check` | PASS |

The full suite emitted existing nonfatal duplicate `-lproc` linker warnings.

## Source delivery and host validation

Source revision S is
`e526070b424e37cc8aa8e4a31c9bfca60e564424`, committed on Mac `dev` as
`phase(LANES-L3-05): add durable security anchor state`. The explicit-key push
to primary Gitea succeeded. Direct `ls-remote` queries showed both
`origin/dev` and the automatic `github/dev` mirror at exact S. Each Ubuntu
checkout was clean on `dev` at pre-pull `a9aa3f38f15f21c5d1dbb550bb12032869d025d2`,
fast-forwarded from GitHub, and verified clean at exact S before validation.
No tracked source was edited or copied directly on Ubuntu.

| Machine | SSH alias / Mac identity | L3-05 H | L3-05 N | `make check build` | Focused race | Final checkout |
| --- | --- | --- | --- | --- | --- | --- |
| `mac-control-plane` | local Mac checkout | PASS (8) | PASS (1) | PASS | PASS | `e526070b424e37cc8aa8e4a31c9bfca60e564424` |
| `linux-host` | `remote-session-runner` / `/Users/tomasz.walczuk/.ssh/remote-session-runner` | PASS (8) | PASS (1) | PASS | PASS | `e526070b424e37cc8aa8e4a31c9bfca60e564424` |
| `sandbox-host` | `sandbox.env` / `/Users/tomasz.walczuk/.ssh/dev.slidestud.io` | PASS (8) | PASS (1) | PASS | PASS | `e526070b424e37cc8aa8e4a31c9bfca60e564424` |
| `factory-sandbox` | `factory-sandbox` / `/Users/tomasz.walczuk/.ssh/factory-sandbox` | PASS (8) | PASS (1) | PASS | NOT RUN — `CGO_ENABLED=0`, no `cc` | `e526070b424e37cc8aa8e4a31c9bfca60e564424` |

Each Ubuntu host first verified clean `dev` at the predecessor, then used:

```sh
git -c "core.sshCommand=ssh -i /home/ubuntu/.ssh/gavrilo123456-github -o IdentitiesOnly=yes -o BatchMode=yes -o StrictHostKeyChecking=yes -o UpdateHostKeys=no" pull --ff-only origin dev
make test-lanes UNIT=LANES-L3-05
LANES_NATIVE_HOST_ID=<canonical-host-id> make test-lanes UNIT=LANES-L3-05 LANES_TIER=N
make check build
```

Linux-host and sandbox-host also ran the focused `go test -race` command using
the pinned Go 1.27.1 binary. Factory-sandbox reported `CGO_ENABLED=0` and no
compiler, so that race assertion was NOT RUN. All three final checkouts were
clean on `dev` at exact S.

## Scope and acceptance state

This was source implementation and isolated validation only. There were no
production credentials, mailbox requests or ACKs, installed configuration,
service restarts, production storage changes, or production work changes.
L3-05 source S is available to the next independent source unit, but L3-04 and
this unit remain BLOCKED by the ordered predecessor acceptance chain. No
installed helper, live gate, unit PASS, or evidence closeout E is claimed.
