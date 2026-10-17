# LANES-L3-04 — authority and host trust chains, revocation, and time

**State:** BLOCKED — source S and required source gates are delivered and
verified; the ordered L3-03 chain remains BLOCKED. No unit PASS, live
acceptance, installation, or evidence closeout E is claimed.

## Pre-unit record

- Pre-unit Mac `dev` HEAD: `21954765f3d01778f47113921ac2c8985920cf5e`.
- Source predecessor: L3-03 source S `120589cfa6277cc6937ca6910c5e55828d456bc9`.
- L3-03 evidence file at pre-unit HEAD: `040-implementation-evidence/LANES-L3-03.md`, blob `1b1e84b142ed1c27dbb17b48894de9e3d28506a0`.
- L3-03 is BLOCKED, not PASS. Its acceptance chain is blocked by L3-02/L3-01 and the L2-09 native gate. L2-09 is blocked by the missing L2-07 Linux controller/device prerequisites and L2-08 disposable Mac APFS fixtures. See [L3-03](LANES-L3-03.md), [L2-09](LANES-L2-09.md), [L2-07](LANES-L2-07.md), [L2-08](LANES-L2-08.md), [L3-01](LANES-L3-01.md), and [L3-02](LANES-L3-02.md). No predecessor PASS evidence-closeout SHA is asserted.
- This source work is independently testable with synthetic Ed25519 identities and isolated payloads. It needs the delivered envelope/schema primitives, but not the blocked native fixtures, production credentials, publisher pause, installed service, production state, or maintenance transaction. The owner's incremental-readiness authorization therefore permits this source-only continuation while preserving the predecessor blocker.
- The maintenance package remains a snapshot. No candidate was rebuilt. B021/B024 remain BLOCKED with their existing evidence and retained work; BUG-022/023 baseline repairs remain DEFERRED.

## Pre-unit read inventory

Document sources read for the unit contract, workflow, sequencing, and
blockers (blob IDs are from the pre-unit HEAD):

| Source | Blob |
| --- | --- |
| `AGENTS.md` | `3402877131d9fcea513f5bae756582e4127ee892` |
| Lane initial idea | `503e54457228da265666ae48ceb2be22a0c0b81f` |
| Lane detailed design | `f7404c2d5a0b35706f3a9e867991bffcb6171d93` |
| Lane phased plan | `082e73ac62a7815c3ed15778eb8162d81a43c347` |
| Lane execution handbook | `d04a659df5c5834725ea05b9b047f4ad296b21bc` |
| Lane execution ledger | `83ee26607b6f2094316b4a2e6cde8f3178fbf7ee` |
| Original PoC initial design | `308dfb82a25029a786f8d8341e6a89940a2eba0b` |
| Original PoC detailed design | `587654e9b71948519cc6a9e8d0c4813405e42e29` |
| Original PoC phased plan | `260bc76b32da215bfc8ad0f1adf05889aa6c244f` |
| L3-03 predecessor evidence | `1b1e84b142ed1c27dbb17b48894de9e3d28506a0` |
| L3-02 blocker evidence | `fe9ded8462053fa40a6d69ecc7d0a99e3fae1245` |
| L3-01 blocker evidence | `eb473935b7cf14b1e56417eed5911ddb71d1da70` |
| L2-09 blocker evidence | `529bde06b2c494844c9e2d832992abe779049f92` |
| L2-07 blocker evidence | `4b6555ec34039b13b2846d46891d6750850f0a36` |
| L2-08 blocker evidence | `871fea76b91ed41f0536b89886c4c6942980e929` |

Implementation review covered `src/internal/release/envelope.go`,
`payload_schema.go`, `l3_payload.go`, existing L3-02/L3-03 tests and V1
schemas, `src/cmd/test-lanes`, the coverage manifest, and the Makefile. Planned
proof is H only, on the Mac and all three Ubuntu source checkouts after S is
delivered; there are no production approvals or external dependencies for this
unit.

## Source deliverable

The source change adds closed V1 schemas for authority key sets, host
attestation key sets, and cumulative release revocation ledgers. Validators
require a contiguous generation and exact predecessor, exact prior authority
signer where applicable, host/bootstrap key mapping, canonical Ed25519 key
encoding, key overlap, and successor proof of possession over the specified
domain-separated JCS payload. Validated rotations return both the retired
predecessor state and successor state so a caller can retain the old key only
through its signed retirement time.

The revocation validator requires a currently trusted signer, exact contiguous
predecessor, sorted cumulative record/key sets, no un-revocation or signer
self-revocation, a maximum 900-second ledger lifetime, and a five-minute
freshness check before mutation. A signed successor can revoke another
currently trusted key.

The time-probe helper owns a fresh 32-byte nonce, challenge ID, wall timestamps,
and monotonic round-trip measurement; callers provide no nonce or timing
values. It verifies the canonical nonce-bound authority signature, consumes a
challenge once, applies the two-second RTT and five-second uncertainty bounds,
retains the highest accepted authority time, and rejects local clock rollback
greater than one second. Authorization and ingress-directive payload checks
now cap the signed interval at 3,600 seconds; `ValidateReleaseTimeInterval`
also requires the interval to be active at use time. Durable challenge/high
water persistence and native helper enforcement remain later L3 units.

Eight registered H assertions cover schema closure, authority and host
rotation chains, bootstrap mapping, proof/signature failures, forks/skips,
revocation monotonicity/freshness, nonce/signature/replay/delay/skew/rollback,
and the exact authorization lifetime boundary. Tests generate disposable
Ed25519 identities only.

## Mac pre-delivery source gates

Execution machine: `mac-control-plane` (Darwin arm64), Makefile-pinned Go
1.27.1, shared Go caches.

| Command | Result |
| --- | --- |
| `make test-lanes UNIT=LANES-L3-04` | PASS — 8 registered H assertions |
| `make test-lanes UNIT=LANES-L3-03` | PASS — 9 predecessor regressions, including the one-hour interval bound |
| `make test-lanes UNIT=LANES-L3-02` | PASS — 8 predecessor regressions |
| `make check build` | PASS — full `./...` tests, vet, smoke, and command builds |
| `GOCACHE=/private/tmp/remote-session-runner-gocache GOMODCACHE=/private/tmp/remote-session-runner-gomodcache GOTOOLCHAIN=local '/Users/tomasz.walczuk/Library/Application Support/RemoteSessionRunner/toolchains/go1.27.1/bin/go' test -race ./src/internal/release -run '^TestLanesL304' -count=1` | PASS |
| `git diff --check` | PASS |
| `python3 -m json.tool 040-implementation-evidence/LANES-test-coverage.json` | PASS |

The full test run emitted existing nonfatal duplicate `-lproc` linker
warnings. No tracked Ubuntu file or installed service was changed.

## Delivery and acceptance status

Source revision S is `90c17965eb647863d0744d1715e05c2c025c2289`, committed on
Mac `dev` as `phase(LANES-L3-04): add release trust and time validators`. Its
explicit-key push to Gitea `origin/dev` succeeded. Git printed the known local
remote-tracking lock warning after the successful push; direct remote queries
confirmed Gitea and GitHub `refs/heads/dev` both resolve to exact S. Ubuntu
checkouts were clean on `dev` at predecessor `21954765f3d01778f47113921ac2c8985920cf5e`,
fast-forwarded through GitHub with `--ff-only`, and verified clean at exact S
before tests. No source was edited or copied directly on Ubuntu.

Delivery commands on the Mac:

```sh
git -c core.sshCommand='ssh -i /Users/tomasz.walczuk/.ssh/local-gitea-key -o IdentitiesOnly=yes -o BatchMode=yes -o StrictHostKeyChecking=yes' push origin dev
git -c core.sshCommand='ssh -i /Users/tomasz.walczuk/.ssh/local-gitea-key -o IdentitiesOnly=yes -o BatchMode=yes -o StrictHostKeyChecking=yes' ls-remote origin refs/heads/dev
git -c core.sshCommand='ssh -i /Users/tomasz.walczuk/.ssh/gavrilo123456-github -o IdentitiesOnly=yes -o BatchMode=yes -o StrictHostKeyChecking=yes' ls-remote github refs/heads/dev
```

Both remote refs returned exact S. Each Ubuntu host was checked clean on `dev`
before this pull command, then verified at S before tests:

```sh
git -c 'core.sshCommand=ssh -i /home/ubuntu/.ssh/gavrilo123456-github -o IdentitiesOnly=yes -o BatchMode=yes -o StrictHostKeyChecking=yes' pull --ff-only origin dev
```

| Machine | SSH profile | Final `HEAD` | L3-04 H | `make check build` | Focused race |
| --- | --- | --- | --- | --- | --- |
| `mac-control-plane` | Mac checkout | `90c17965eb647863d0744d1715e05c2c025c2289` | PASS (8) | PASS | PASS |
| `linux-host` (`oracle-yuta-konopka-ubuntu-micro-02`) | `remote-session-runner`; `/Users/tomasz.walczuk/.ssh/remote-session-runner` | `90c17965eb647863d0744d1715e05c2c025c2289` | PASS (8) | PASS | PASS |
| `sandbox-host` (`oracle-gustaw-janecki-ubuntu-flex-02`) | `sandbox.env`; `/Users/tomasz.walczuk/.ssh/dev.slidestud.io` | `90c17965eb647863d0744d1715e05c2c025c2289` | PASS (8) | PASS | PASS |
| `factory-sandbox` (`oracle-waclaw-dziwulski-ubuntu-micro-01`) | `factory-sandbox`; `/Users/tomasz.walczuk/.ssh/factory-sandbox` | `90c17965eb647863d0744d1715e05c2c025c2289` | PASS (8) | PASS | NOT RUN: `CGO_ENABLED=0`, no `cc` |

Each Ubuntu host ran `make test-lanes UNIT=LANES-L3-04` and `make check
build` at S. Linux-host and sandbox-host additionally ran
`GOTOOLCHAIN=local /home/ubuntu/.local/share/remote-session-runner/toolchains/go1.27.1/bin/go test -race ./src/internal/release -run '^TestLanesL304' -count=1`
with PASS. Mac ran that focused race command using the pinned Mac toolchain and
the shared Go caches. Mac also reran L3-03 (9 H assertions) and L3-02 (8 H
assertions) as named regressions; both passed.

The full test runs emitted existing nonfatal duplicate `-lproc` linker
warnings. The source/evidence delivery did not restart services, publish
Runner requests or ACKs, create production credentials, or modify production
state. The historical maintenance package remains a snapshot; it was not
rebuilt or frozen by this source unit.

L3-04 remains BLOCKED by L3-03/L3-02/L3-01/L2-09 ordered acceptance and the
L2-07/L2-08 native infrastructure fixtures. No installed or live acceptance
is inferred. B021/B024 remain BLOCKED and BUG-022/023 remain DEFERRED.
