# LANES-L3-03 — promotion, admission, capacity, and PKI payloads

**State:** BLOCKED — Source S `120589cfa6277cc6937ca6910c5e55828d456bc9`
and all required L3-03 source gates pass on Mac and the three synchronized
Ubuntu hosts. L3-02 is still blocked by L3-01/L2-09 native F, so L3-03 has no
ordered acceptance or evidence closeout E.

## Predecessors and scope

- Pre-unit Mac HEAD: `2e7f1a16b342a07273273019cc23dc88fc7b4aca`, clean `dev`.
- L3-02 source predecessor: `d82d171cc545cfdbf4e6f611b3f3b66faec074d8`;
  its evidence follow-up is the pre-unit HEAD above. L3-02 remains BLOCKED by
  L3-01/L2-09 native F, and has no ordered acceptance or evidence closeout E.
- L3-01 source is `f87dfa1a5f8f10fe9664f1c6e87c7a7517496bed`; L2-09 F remains
  blocked by the L2-07 Linux device/controller prerequisites and L2-08
  disposable APFS fixtures.
- The owner's incremental-readiness instruction allowed this successive
  hermetic source unit to proceed from delivered L3-02 source. No live lane,
  installation, production credential, capacity, ingress, or service gate is
  represented as passed.
- B021/B024 remain **BLOCKED**; BUG-022/023 remain deferred. No mailbox
  publication, publisher control, service, storage, firewall, credential, or
  production Runner change occurred. The external maintenance package remains
  a snapshot and was not rebuilt or used to freeze development.

Inputs read from pre-unit `2e7f1a16b342a07273273019cc23dc88fc7b4aca`:

| Input | Blob |
| --- | --- |
| `AGENTS.md` | `3402877131d9fcea513f5bae756582e4127ee892` |
| Lane detailed design | `f7404c2d5a0b35706f3a9e867991bffcb6171d93` |
| Lane phased plan | `082e73ac62a7815c3ed15778eb8162d81a43c347` |
| Lane execution handbook | `d04a659df5c5834725ea05b9b047f4ad296b21bc` |
| Lane execution ledger | `2fec6b7ad975ab3a2022235ceb8993482e8b2f43` |
| L3-02 unit evidence | `fe9ded8462053fa40a6d69ecc7d0a99e3fae1245` |
| L3-01 unit evidence | `eb473935b7cf14b1e56417eed5911ddb71d1da70` |
| L2-09 blocker evidence | `529bde06b2c494844c9e2d832992abe779049f92` |
| L2-07 blocker evidence | `4b6555ec34039b13b2846d46891d6750850f0a36` |
| L2-08 blocker evidence | `871fea76b91ed41f0536b89886c4c6942980e929` |
| Lane test coverage contract | `d3bb967e57d4e5d21cdc9b4ca2ea1347b03bb26f` |

## Implemented source contract

- Added closed V1 schemas for promotion authorizations and attestations, lane
  acceptance, ingress admission directives and attestations, capacity
  measurement attestations and policies, and PKI certificate requests. All
  eight record types are registered in the embedded schema compiler.
- Promotion validation enforces canonical host component sets, the Dev versus
  QAS/PRD predecessor rule, nonempty validity, exact candidate artifact
  binding, and matching lane-runtime capacity-policy host/lane binding.
  Promotion attestations bind every authorization identity; lane acceptance
  requires one accepted, same-transition result for each of the four hosts.
- Ingress validation binds the complete directive identity and phase to its
  host attestation. The surface inventory is exact and sorted: Mac
  `local_api`, `local_intent`, `mailbox_ack_importer`,
  `mailbox_request_importer`; Ubuntu `direct_https`, `queued_ssh_bridge`.
  Any non-applied surface yields `applied=false`; missing or cross-host
  surfaces are invalid.
- Capacity measurement validation recomputes the deterministic digest over the
  full typed inventory. Capacity policy validation binds the verified
  measurement record ID, host and digest, checks its validity interval, and
  applies the existing Linux cgroup/storage or Mac APFS/quota/headroom
  validator to isolated measurement fixtures.
- PKI request validation uses the fixed host plan for role, URI, and IP;
  checks mutually exclusive management/bootstrap context forms; verifies the
  CSR signature, Ed25519 public-key fingerprint, allowed subject/SAN set, and
  bounded certificate end time. Tests create disposable test keys and CSR
  material only. Envelope signature and active signer trust remain the caller's
  responsibility.
- Nine registered H assertions cover every schema family, cross-record
  identity rules, exact matrices/surfaces, capacity digest and scope binding,
  Linux and Mac storage-policy constraints, and both test PKI context forms.

## Mac source gates

Execution machine: `mac-control-plane` (Darwin arm64), Go 1.27.1 from the
Makefile-pinned toolchain, using the shared Go caches.

| Command | Result |
| --- | --- |
| `make test-lanes UNIT=LANES-L3-03` | PASS — nine registered H assertions |
| `make test-lanes UNIT=LANES-L3-02` | PASS — eight predecessor H assertions |
| `make check build` | PASS — full `./...` tests, vet, and command builds |
| `GOCACHE=/private/tmp/remote-session-runner-gocache GOMODCACHE=/private/tmp/remote-session-runner-gomodcache GOTOOLCHAIN=local '/Users/tomasz.walczuk/Library/Application Support/RemoteSessionRunner/toolchains/go1.27.1/bin/go' test -race ./src/internal/release -run '^TestLanesL303' -count=1` | PASS |
| `git diff --check` | PASS |

The full tests printed existing nonfatal duplicate `-lproc` linker warnings.

## Source S and synchronized host validation

Source S is `120589cfa6277cc6937ca6910c5e55828d456bc9`, committed on Mac `dev`
as `phase(LANES-L3-03): add release payload validators`. The explicit-key push
to Gitea `origin/dev` succeeded, and direct `ls-remote` checks showed both
Gitea `origin/dev` and automatic GitHub mirror `github/dev` at exact S. Git
printed a local remote-tracking-ref lock warning after the successful push;
the independently queried remote refs confirmed the commit. Mac `dev` was
clean at S before this evidence follow-up.

Each Ubuntu checkout was clean on `dev` at predecessor
`2e7f1a16b342a07273273019cc23dc88fc7b4aca`; each fast-forwarded from the GitHub
mirror using `/home/ubuntu/.ssh/gavrilo123456-github`, then verified clean at
exact S before validation. Every host ran the focused H gate and
`make check build` at S. All commands exited zero; worktrees remained clean.
No tracked files were edited or copied directly on Ubuntu.

| Machine | SSH profile | Focused H | `make check build` | Final checkout |
| --- | --- | --- | --- | --- |
| `mac-control-plane` | local Mac checkout | PASS (9) | PASS | clean `dev` at S before this evidence follow-up |
| `linux-host` (`oracle-yuta-konopka-ubuntu-micro-02`) | `remote-session-runner`; `/Users/tomasz.walczuk/.ssh/remote-session-runner` | PASS (9) | PASS | clean `dev` at S |
| `sandbox-host` (`oracle-gustaw-janecki-ubuntu-flex-02`) | `sandbox.env`; `/Users/tomasz.walczuk/.ssh/dev.slidestud.io` | PASS (9) | PASS | clean `dev` at S |
| `factory-sandbox` (`oracle-waclaw-dziwulski-ubuntu-micro-01`) | `factory-sandbox`; `/Users/tomasz.walczuk/.ssh/factory-sandbox` | PASS (9) | PASS | clean `dev` at S |

## Acceptance blocker

L3-02 source and H gates are present, but L3-02 remains blocked by the ordered
L3-01/L2-09 native F dependency. L2-09 still needs L2-07 Linux device and
controller prerequisites and L2-08 disposable Mac APFS fixtures. L3-03 is
therefore recorded as BLOCKED despite its H/source delivery. No L3-03 PASS,
live acceptance, installation, or evidence closeout E is claimed. Under the
owner's incremental-readiness instruction, later independent source work may
continue in plan order while retaining this blocker chain.
