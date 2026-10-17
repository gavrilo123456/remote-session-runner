# LANES-L3-02 — build, candidate, and host-management schemas

**State:** BLOCKED — Source S `d82d171cc545cfdbf4e6f611b3f3b66faec074d8` and
all required L3-02 source gates pass on Mac and the three synchronized Ubuntu
hosts. L3-01 remains blocked by L2-09 native F, so L3-02 has no ordered
acceptance or evidence closeout E.

## Predecessors and scope

- Pre-unit Mac HEAD: `aa68d359ec0bd59bf7ae5d7d4f902acbd7f1eb3e`, clean `dev`.
- L3-01 source predecessor: `f87dfa1a5f8f10fe9664f1c6e87c7a7517496bed`.
  Its current evidence follow-up is at pre-unit `aa68d35`, blob
  `eb473935b7cf14b1e56417eed5911ddb71d1da70`. L3-01 remains BLOCKED because
  L2-09 native F and ordered acceptance are outstanding.
- The owner's incremental-readiness decision allowed this independent H-tier
  source work to proceed from delivered L3-01 source while preserving the
  blocker. No L2 fixture or operation was represented as passed.
- B021/B024 remain **BLOCKED**. BUG-022/023 remain deferred. No mailbox
  publication, publisher control, service, storage, firewall, credential, or
  production Runner change occurred. The external maintenance package remains
  a snapshot and was not rebuilt or used to freeze development.

Inputs read from pre-unit `aa68d359ec0bd59bf7ae5d7d4f902acbd7f1eb3e`:

| Input | Blob |
| --- | --- |
| `AGENTS.md` | `3402877131d9fcea513f5bae756582e4127ee892` |
| Lane detailed design | `f7404c2d5a0b35706f3a9e867991bffcb6171d93` |
| Lane phased plan | `082e73ac62a7815c3ed15778eb8162d81a43c347` |
| Lane execution handbook | `d04a659df5c5834725ea05b9b047f4ad296b21bc` |
| Lane execution ledger | `393407613e43ac396f140f252d4dd67d71302023` |
| L3-01 unit evidence | `eb473935b7cf14b1e56417eed5911ddb71d1da70` |
| L2-09 blocker evidence | `529bde06b2c494844c9e2d832992abe779049f92` |
| L2-07 blocker evidence | `4b6555ec34039b13b2846d46891d6750850f0a36` |
| L2-08 blocker evidence | `871fea76b91ed41f0536b89886c4c6942980e929` |
| Lane test coverage contract | `ac98577ae09d3f6376a1da0577db4fcef358bc32` |

## Implemented source contract

- Added closed, embedded V1 payload schemas for `host_build_attestation`,
  `candidate_manifest`, and `host_management_manifest`; unknown fields, bad
  digests, malformed timestamps, unsupported tuples, and invalid IP SANs fail.
- The lane-runtime matrix has exactly 16 `(host_id, component)` tuples; the
  host-management matrix has exactly 20. Validators enforce sorted canonical
  row order, no missing/extra/duplicate rows, and one GOOS/GOARCH tuple per host.
- Build attestations bind equal 40-character source/Gitea/GitHub commit IDs,
  track, host/component/platform, toolchain/input/artifact digests, V3 schema
  digest, sorted evidence digests, and a UTC `Z` timestamp.
- Candidate and management manifests bind every row to the referenced
  host-build envelope record ID. Validators recompute each canonical envelope
  ID and compare its tuple, source revision, and shared template/schema digest.
  Callers must verify envelope signature, signer role, and host identity against
  active trust before passing records; L3-02 does not assert signature trust.
- Host-management transport inventory binds CA and trust assignments to each
  host, leaf URI/IP identity to its issuing trusted CA, and every active peer
  certificate/SPKI pair to an inventoried leaf.
- Eight registered H assertions cover closed schemas, both full matrices,
  source/toolchain/input/artifact/schema binding, platform uniformity, and
  transport inventory relationships.

## Mac source gates

Execution machine: `mac-control-plane` (Darwin arm64), pinned Go 1.27.1 at
`/Users/tomasz.walczuk/Library/Application Support/RemoteSessionRunner/toolchains/go1.27.1/bin`,
with the Makefile-shared Go caches.

| Command | Result |
| --- | --- |
| `make test-lanes UNIT=LANES-L3-02` | PASS — eight registered H assertions |
| `make check build` | PASS — full `./...` tests, vet, smoke, and command builds |
| `go test -race ./src/internal/release -run '^TestLanesL302' -count=1` with shared caches | PASS |
| `git diff --check` | PASS |

An initial race invocation used the restricted default Go cache and could not
open a cache file. The focused race test passed when rerun with
`GOCACHE=/private/tmp/remote-session-runner-gocache` and
`GOMODCACHE=/private/tmp/remote-session-runner-gomodcache`. The full tests
printed existing nonfatal duplicate `-lproc` linker warnings.

## Source S and synchronized host validation

Source S is `d82d171cc545cfdbf4e6f611b3f3b66faec074d8`, committed on Mac `dev`
as `phase(LANES-L3-02): add closed build and manifest schemas`. Gitea primary
`origin/dev` and automatic GitHub mirror `github/dev` both resolved to S. The
Gitea push succeeded; a local remote-tracking-ref lock warning did not affect
either remote ref. Mac `dev` remained clean at S.

Each Ubuntu host was clean on `dev` at predecessor
`aa68d359ec0bd59bf7ae5d7d4f902acbd7f1eb3e`, with GitHub mirror as `origin`.
Each used the configured explicit Mac SSH identity and strict verified host
key checking, then fast-forwarded with `/home/ubuntu/.ssh/gavrilo123456-github`
to exact S. At S each ran the focused H gate and `make check build`; all exited
zero and all worktrees remained clean. No tracked files were edited or copied
directly on Ubuntu.

| Machine | SSH profile | Focused H | `make check build` | Final checkout |
| --- | --- | --- | --- | --- |
| `mac-control-plane` | local Mac checkout | PASS (8) | PASS | clean `dev` at S |
| `linux-host` (`oracle-yuta-konopka-ubuntu-micro-02`) | `remote-session-runner`; `/Users/tomasz.walczuk/.ssh/remote-session-runner` | PASS (8) | PASS | clean `dev` at S |
| `sandbox-host` (`oracle-gustaw-janecki-ubuntu-flex-02`) | `sandbox.env`; `/Users/tomasz.walczuk/.ssh/dev.slidestud.io` | PASS (8) | PASS | clean `dev` at S |
| `factory-sandbox` (`oracle-waclaw-dziwulski-ubuntu-micro-01`) | `factory-sandbox`; `/Users/tomasz.walczuk/.ssh/factory-sandbox` | PASS (8) | PASS | clean `dev` at S |

## Acceptance blocker

L3-01 source is present and tested, but its ordered unit acceptance remains
blocked by L2-09 native F; L2-09 still needs L2-07 Linux device/controller
prerequisites and L2-08 disposable APFS fixtures. Therefore L3-02 is recorded
as BLOCKED despite its H/source delivery. No L3-02 PASS or evidence closeout E
is claimed. Subsequent independent source work may proceed under the existing
incremental-readiness decision while preserving this blocker chain.
