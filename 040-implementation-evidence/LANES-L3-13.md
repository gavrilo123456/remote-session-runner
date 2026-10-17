# LANES-L3-13 — fixed Mac local and Ubuntu mTLS release-record API

**State:** BLOCKED in the ordered lane sequence. Source S2, all six H
assertions, the native N assertion, and the required source gates passed on
the Mac and all three clean synchronized Ubuntu `dev` checkouts. The ordered
L3-12 F acceptance remains NOT RUN, so this record does not claim L3-13 PASS,
an evidence-closeout E, a live listener, or installed acceptance.

## Pre-unit state and scope

- Pre-unit Mac `dev` HEAD was `4a50324b90ce4cd1e48899546749cc982b0ab464`, the
  L3-12 blocked-source evidence commit. The active request named L1-02, which
  was already delivered from L1-01 closeout `b25468dd09a63a9c85daee2b219dfc61f2adb492`
  and has PASS source evidence at `fbb866789a01f7dcc442164c51ccf255b95405db`.
  The recorded L1 sequence is complete through L1-13; L3-13 was the next
  unfinished ordered source unit.
- L3-12 predecessor evidence is
  `040-implementation-evidence/LANES-L3-12.md`, blob
  `a7b7667fdd93bbece6c1e558fd193fb4059334d2` at pre-unit HEAD. Its state is
  BLOCKED because required F is NOT RUN: protected issuer/installer services
  and identities have not been installed, and L3-11/L3-10 acceptance remains
  blocked. L3-13's isolated API source tests do not use those services; the
  ordered predecessor blocker remains unchanged.
- B021 and B024 evidence remains BLOCKED and unchanged from pre-unit HEAD:
  `LANES-B021-02.md` blob
  `3e9700485f48ad75ad42ba04334d5b3871a36c7a`, and `LANES-B024-02.md` blob
  `15211861260284d0923c9d64259bd7746700399e`. BUG-022/023 baseline repairs
  remain DEFERRED. The historical maintenance package was not rebuilt or used
  to freeze development.
- H/N fixtures use temporary test state, generated certificates and keys, and
  fake security-anchor operations. No production credentials or keys were
  read; no Runner request or ACK was published; no publisher, service,
  installed configuration, storage, or firewall was changed. The native N
  listener is an ephemeral loopback test server, not an installed listener.

## Pre-unit input revisions

The following repository inputs resolve at pre-unit HEAD
`4a50324b90ce4cd1e48899546749cc982b0ab464`:

| Input | Blob |
| --- | --- |
| `AGENTS.md` | `3402877131d9fcea513f5bae756582e4127ee892` |
| Original initial design | `308dfb82a25029a786f8d8341e6a89940a2eba0b` |
| Original detailed design | `587654e9b71948519cc6a9e8d0c4813405e42e29` |
| Original phased plan | `260bc76b32da215bfc8ad0f1adf05889aa6c244f` |
| Lane initial idea | `503e54457228da265666ae48ceb2e22a0c0b81f` |
| Lane detailed design | `f7404c2d5a0b35706f3a9e867991bffcb6171d93` |
| Lane phased plan | `082e73ac62a7815c3ed15778eb8162d81a43c347` |
| Lane execution handbook | `d04a659df5c5834725ea05b9b047f4ad296b21bc` |
| Execution ledger | `f08c4f0dca48b221ccce7ad195e9016ab596a210` |
| L3-12 predecessor evidence | `a7b7667fdd93bbece6c1e558fd193fb4059334d2` |
| `Makefile` / `go.mod` | `39cad4746bc66d27cafd249a9d7d3885f411dccc` / `3073f69b23c7f8c851bda3d4ac299a5f81108e34` |
| Lane coverage manifest / test | `1eff5bdc3b331934f5553bc1e0fc3e7f2624bd95` / `11c8698207cf9db8a06d3d2399c9fd131e6bd3d8` |
| Existing release security endpoint | `bf09eccd271bbcc20e37bd31a054dba4447623ea` |
| Darwin+cgo endpoint identity | `377b0ecae02d08b88e11f7312b5a6c4b3fbcf080` |
| Linux endpoint identity | `0b98b75da4ed98434c2f091a70a724763f0a830b` |
| Darwin non-cgo endpoint identity | `acd4bdda59c0527c2add502ec431b8c6b2a1fc60` |
| Authority signer / host attestation | `368657abfe81f7cddc0178f0a934a7f9348a6891` / `e97d17e626af0a30283c0b9f27bf9c77f3f93f93` |
| Verified release cache | `63e1fae96a6dae194f223cf6b4f34ed053f829af` |

## Implemented source contract

- Adds the fixed three-host Ubuntu endpoint matrix and the Mac-only local Unix
  socket constructor. Ubuntu requests use TLS 1.3 mutual authentication and
  the fixed private `:9443` bind, public-host header, active host/authority
  URI SANs, literal server IP, certificate and SPKI fingerprints. The Mac
  profile exposes no TLS listener and accepts only the authority-agent peer
  evidence produced by Darwin audit-token, launchd-label, and code-identity
  checks. Non-Darwin and Darwin non-cgo builds fail closed for that Mac-only
  peer inspection path.
- Implements only the closed routes from design §7: outbox index/read,
  addressed record cache, single-use time-probe begin/accept, fixed no-body
  capacity attestation, and opaque-mandate PKI attestation. It rejects
  unknown methods/routes, queries, escaped or traversal paths, request bodies
  on fixed operations, arbitrary JSON or keys, and generic file/source/
  artifact/shell/SQLite operations.
- Validates canonical envelopes, URL/body record-ID agreement, allowed record
  types, payload schemas, host scope, and active peer fingerprints. The
  `ReleaseTransportOperations` contract requires the fixed security-anchor
  adapter to own complete signature/chain/ledger/time/host validation and
  durable atomic cache writes; the API passes exact envelope bytes and accepts
  only a same-ID `cached` or `already_present` result. H tests use an isolated
  backend fixture to verify the retry/collision contract. No production
  adapter, anchor service, or cache is wired or installed by this unit.
- Registers six H assertions and one native N assertion in
  `LANES-test-coverage.json` and the dispatcher manifest. H coverage checks all
  three Ubuntu endpoint identities, fixed listener modes, active pins,
  cross-host outbox scope, mismatched record IDs, retained-byte collisions,
  closed routes, and Mac peer denial. N performs a native TLS 1.3 mTLS
  handshake and rejects removed active peer pins.

## Source revisions and correction history

- Initial source S1 was
  `d5c392934e4a0b2c6f6776a0d8df8a0d350330fb`
  (`phase(LANES-L3-13): add fixed release-record transport`). Mac H/N and
  full source gates passed. The first Ubuntu H/N/full-suite run exposed the
  missing non-Darwin definition for `inspectAuthorityAgentPeer` at
  `release_transport_api.go:247`. The Linux build correctly failed; S1 is not
  treated as a validated source revision.
- Corrected source S2 is
  `551e9b550ae630fdaed7b6d987df7cf4d76d596c`
  (`fix(LANES-L3-13): provide fail-closed platform stub`). It adds
  `security_endpoint_no_authority_agent.go` for `!darwin || !cgo` and a
  regression that requires Linux to reject Mac authority-agent inspection.
  This correction preserves the Darwin+cgo implementation.
- The first Ubuntu pull wrapper had a remote-shell quoting error before Git
  ran; the corrected wrapper used a single quoted `core.sshCommand`. The S1
  primary push succeeded; Git's local tracking-ref update was denied by the
  sandbox and was corrected with an authorized explicit fetch. Neither event
  changed host files. S2 was then delivered by the prescribed Git workflow.

## Required gate results at S2

All four machines used Go 1.27.1. Mac used the shared Makefile caches
`GOCACHE=/private/tmp/remote-session-runner-gocache` and
`GOMODCACHE=/private/tmp/remote-session-runner-gomodcache`; Ubuntu reused each
account's normal Go caches.

| Machine | L3-13 H / N | `make check build` | `make smoke` | Focused race | Final checkout |
| --- | --- | --- | --- | --- | --- |
| `mac-control-plane` — Darwin arm64, Go 1.27.1 | PASS — six H, one native N | PASS — full `./...`, vet, command builds | PASS | PASS — `go test -race ./src/internal/release -run TestLanesL313 -count=1` | `dev`, clean, S2 |
| `linux-host` — `129.151.232.40`, Linux amd64, cgo 1, SSH identity `/Users/tomasz.walczuk/.ssh/remote-session-runner` | PASS — six H, one native N | PASS | PASS | PASS | `dev`, clean, S2 |
| `sandbox-host` — `132.226.205.205`, Linux arm64, cgo 1, SSH identity `/Users/tomasz.walczuk/.ssh/dev.slidestud.io` | PASS — six H, one native N | PASS | PASS | PASS | `dev`, clean, S2 |
| `factory-sandbox` — `145.241.255.66`, Linux amd64, cgo 0, SSH identity `/Users/tomasz.walczuk/.ssh/factory-sandbox` | PASS — six H, one native N | PASS | PASS | NOT RUN — toolchain reports `CGO_ENABLED=0` | `dev`, clean, S2 |

The per-host commands were `make test-lanes UNIT=LANES-L3-13
LANES_TIER=H`, `make test-lanes UNIT=LANES-L3-13 LANES_TIER=N`,
`make check build`, and `make smoke`. Where cgo was enabled, each host also
ran `GOTOOLCHAIN=local /home/ubuntu/.local/share/remote-session-runner/toolchains/go1.27.1/bin/go test -race ./src/internal/release -run TestLanesL313 -count=1`.
Mac additionally ran the focused race command above and a Linux/amd64/cgo=0
`go test -c` cross-compile of `./src/internal/release`; both passed.

## Git delivery

- Mac `dev` S2: `551e9b550ae630fdaed7b6d987df7cf4d76d596c`; worktree clean.
- Primary Gitea `origin/dev`: exact S2 verified with explicit
  `/Users/tomasz.walczuk/.ssh/local-gitea-key`.
- Automatic GitHub mirror `github/dev`: exact S2 verified with explicit
  `/Users/tomasz.walczuk/.ssh/gavrilo123456-github`.
- Each Ubuntu checkout was clean on `dev` at S1 before the S2 pull and then
  fast-forwarded with `--ff-only origin dev` using
  `/home/ubuntu/.ssh/gavrilo123456-github`. The final HEAD on `linux-host`,
  `sandbox-host`, and `factory-sandbox` equals S2, and every checkout is clean.
  No versioned file was edited or copied outside Git.

The delivery commands were the prescribed explicit-identity commands:

```sh
# Mac: push primary Gitea
git -c core.sshCommand='ssh -i /Users/tomasz.walczuk/.ssh/local-gitea-key -o IdentitiesOnly=yes -o BatchMode=yes -o StrictHostKeyChecking=yes' push origin dev

# Mac: verify primary and automatic mirror
git -c core.sshCommand='ssh -i /Users/tomasz.walczuk/.ssh/local-gitea-key -o IdentitiesOnly=yes -o BatchMode=yes -o StrictHostKeyChecking=yes' ls-remote origin refs/heads/dev
git -c core.sshCommand='ssh -i /Users/tomasz.walczuk/.ssh/gavrilo123456-github -o IdentitiesOnly=yes -o BatchMode=yes -o StrictHostKeyChecking=yes' ls-remote github refs/heads/dev

# Each Ubuntu host, after confirming a clean dev checkout
git -c core.sshCommand='ssh -i /home/ubuntu/.ssh/gavrilo123456-github -o IdentitiesOnly=yes -o BatchMode=yes -o StrictHostKeyChecking=yes' pull --ff-only origin dev
```

## Remaining ordered blocker

L3-13 remains BLOCKED in sequence solely because predecessor L3-12 F and its
earlier L3-11/L3-10 acceptance chain remain unmet. No release-transport,
security-anchor, issuer, or installer service was started or installed. No
ordered PASS or evidence-closeout E is claimed. The owner-authorized
source-only continuation can proceed to the next independent ordered unit
while citing this blocker; any L3-14 live F that depends on L2-07/L2-08
capacity fixtures remains blocked until those actual prerequisites pass.
