# LANES-L3-01 — canonical V1 release-record envelope

**State:** BLOCKED — Source S, all four-platform H gates, and full source gates
pass. L2-09 remains BLOCKED at native F, so this source-only exception does
not pass the ordered predecessor or claim L3-01 acceptance/evidence closeout E.

## Predecessor and independence

- Pre-unit Mac HEAD: `d700a96064027594772d9deeaeaa9ded8f6f706a`, clean `dev`.
- Ordered L2-09 predecessor evidence is
  [`LANES-L2-09.md`](LANES-L2-09.md), blob
  `529bde06b2c494844c9e2d832992abe779049f92`, in commit
  `d700a96064027594772d9deeaeaa9ded8f6f706a`. It is BLOCKED: source and H
  pass, but native F and evidence acceptance are not run.
- L2-07's blocker evidence is [`LANES-L2-07.md`](LANES-L2-07.md), blob
  `4b6555ec34039b13b2846d46891d6750850f0a36`; L2-08's is
  [`LANES-L2-08.md`](LANES-L2-08.md), blob
  `871fea76b91ed41f0536b89886c4c6942980e929`.
- L3-01 is source-only and independent of the missing native devices, cgroup
  setup, and APFS volumes: it implements deterministic JSON/signature
  mechanics from the fixed design using synthetic payloads and test keys. It
  neither starts a service nor accesses production credentials, storage,
  mailboxes, publishers, or Runner state. No operational approval is needed.
- B021/B024 remain BLOCKED with their evidence and held ACKs preserved.
  BUG-022/023 remain deferred. The historical B021 maintenance snapshot was
  not rebuilt.

## Pre-unit inputs reviewed

| Input | Blob at pre-unit HEAD |
| --- | --- |
| `AGENTS.md` | `3402877131d9fcea513f5bae756582e4127ee892` |
| Initial idea | `503e54457228da265666ae48ceb2be22a0c0b81f` |
| Detailed design, including §7 closed-record protocol | `f7404c2d5a0b35706f3a9e867991bffcb6171d93` |
| Phased implementation plan, including L3-01 requirements | `082e73ac62a7815c3ed15778eb8162d81a43c347` |
| Execution handbook | `d04a659df5c5834725ea05b9b047f4ad296b21bc` |
| Execution ledger | `b5825187137d731d831284e104ae55e912952a0c` |
| L2-07 blocker evidence | `4b6555ec34039b13b2846d46891d6750850f0a36` |
| L2-08 blocker evidence | `871fea76b91ed41f0536b89886c4c6942980e929` |
| L2-09 blocker evidence | `529bde06b2c494844c9e2d832992abe779049f92` |
| Lane test coverage manifest | `699559b88d04d6998ef5b76c337c96d2c72c0c83` |
| `Makefile` | `39cad4746bc66d27cafd249a9d7d3885f411dccc` |
| `go.mod` | `3073f69b23c7f8c851bda3d4ac299a5f81108e34` |
| `go.sum` | `3804f7cbeed6bb4756ee221fb5aed82426184041` |
| `src/internal/domain/canonical.go` | `2588059a733a3d7a569b9909dd1a4a61c6d4a671` |
| `src/internal/domain/canonical_test.go` | `e53bc2d413bc8aeda8d85b9f3b2dd2ffe32e9806` |
| `src/internal/laneinstall/pki.go` | `a448a69580d7648260669fdce222679c9c2dd558` |
| `src/internal/lanetests/manifest.go` | `f5cc9bec4d6046257e5fa7853bb72dc9a278fe1b` |
| `src/internal/lanetests/manifest_test.go` | `e1ce387e14007c1ea17640b85665338d66fdc783` |

The design fixes the exact V1 envelope fields, first-five-field signed body,
domain separator, 16 record types, JCS wire encoding, and signer-role table.
L3-02/L3-03 own the typed payload schemas, so this unit requires a JSON object
payload and defers payload schema validation.

## Source work

Added `src/internal/release` with:

- RFC 8785 canonical JSON encoding: UTF-16 property ordering, ECMAScript
  binary64 number formatting, exact JSON escaping, duplicate-name rejection,
  invalid UTF-8 and isolated surrogate rejection, and negative-zero refusal.
- The closed V1 envelope creator/verifier with the exact signed body and
  `remote-session-runner/release-record/<type>/v1` NUL-separated ID/signature
  input. Verification rejects noncanonical bytes, open envelopes, unknown
  record types, role mismatches, foreign host keys, incorrect predecessor-key
  identity, digest mismatch, and invalid Ed25519/base64 signatures.
- Seven registered H assertions. Golden records cover all 16 V1 record types;
  expected IDs and signatures were independently generated with Python
  `cryptography` Ed25519 using fixed test seeds. Other cases cover RFC JCS
  canonicalization and all 22 finite binary64 samples from its Appendix B,
  including notation boundaries and round-to-even behavior. Negative zero is
  refused per the RFC 8785 verified erratum recommendation. The suite also
  covers unknown roles/types, canonical-form and duplicate/unknown-field
  rejection, signer-role/host binding, successor-key predecessor binding, and
  tampered IDs/signatures.

The synthetic signing seeds exist only in tests. No new module dependency was
added. No concurrency primitive or mutable runtime state was introduced, so a
race-specific test is not applicable.

## Source gates on all four platforms

Execution machine: `mac-control-plane`, Makefile-pinned Go 1.27.1 with the
shared Mac `GOCACHE` and `GOMODCACHE`.

| Command | Result |
| --- | --- |
| `make test-lanes UNIT=LANES-L3-01` | PASS; seven registered H assertions |
| `make check build` | PASS; full Go test suite, `go vet ./...`, command smoke checks, and `go build ./src/cmd/...` |
| `git diff --check` | PASS |
| Race-specific gate | Not applicable; implementation has no concurrent mutable state |

The first focused run exposed a test-key role setup mismatch for the authority
successor vector. The trusted-key check was corrected to permit the current
trusted authority key only when it is the payload's exact `previous_key_id`;
the focused H gate and full source gates then passed. No test failure was
suppressed. The full Mac build emitted only non-fatal duplicate `-lproc` linker
warnings for existing Runner test binaries.

## Source S delivery and synchronized validation

Source S is `f87dfa1a5f8f10fe9664f1c6e87c7a7517496bed`, committed on Mac `dev`
as `phase(LANES-L3-01): add canonical release record envelopes`. The Gitea
primary push succeeded. Direct reads of Gitea `origin/dev` and automatic
GitHub mirror `github/dev` both resolved to exact S. Git could not update the
Mac's local `origin/dev` tracking ref because it could not create
`.git/refs/remotes/origin/dev.lock`; this did not affect the push, and both
remote refs were verified directly.

Before pull, all three Ubuntu checkouts were clean on `dev` at
`d700a96064027594772d9deeaeaa9ded8f6f706a`. Each fast-forwarded from the
verified GitHub mirror using the configured explicit pull identity and strict
host-key checking. Each ran the focused H gate and `make check build` at exact
S, exited zero, and remained clean on `dev` at S. No source was edited or
copied directly on Ubuntu.

| Machine | Before pull | Source S after validation | Result |
| --- | --- | --- | --- |
| `mac-control-plane` | `d700a96064027594772d9deeaeaa9ded8f6f706a` | `f87dfa1a5f8f10fe9664f1c6e87c7a7517496bed` | H PASS; `make check build` PASS; clean `dev` |
| `linux-host` | `d700a96064027594772d9deeaeaa9ded8f6f706a` | `f87dfa1a5f8f10fe9664f1c6e87c7a7517496bed` | fast-forward; H PASS; `make check build` PASS; clean `dev` |
| `sandbox-host` | `d700a96064027594772d9deeaeaa9ded8f6f706a` | `f87dfa1a5f8f10fe9664f1c6e87c7a7517496bed` | fast-forward; H PASS; `make check build` PASS; clean `dev` |
| `factory-sandbox` | `d700a96064027594772d9deeaeaa9ded8f6f706a` | `f87dfa1a5f8f10fe9664f1c6e87c7a7517496bed` | fast-forward; H PASS; `make check build` PASS; clean `dev` |

Mac SSH printed a warning when it could not rewrite `known_hosts` under the
workspace write restriction. `StrictHostKeyChecking=yes` succeeded for each
host command; no host-key file was changed.

## Acceptance blocker and next source work

L3-01 remains BLOCKED from acceptance because its ordered L2 predecessor
L2-09 has no native F or evidence closeout E. The exact missing prerequisites
remain the L2-07 Linux devices/controller requirements and L2-08 disposable
Mac APFS fixtures documented above. L3-01 source/H results do not satisfy
those gates and do not infer any install, live behavior, or lane acceptance.

Under the owner's incremental-readiness authorization, an independent L3-02
hermetic schema source unit may use this delivered, four-platform-tested S as
its source predecessor while recording L3-01's blocked acceptance and the
unchanged L2-09 blocker. That source work cannot claim an L3-01 PASS or permit
any dependent live gate; L2 acceptance still requires its missing native F and
evidence closeout E.
