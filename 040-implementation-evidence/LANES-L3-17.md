# LANES-L3-17 — constrained bootstrap packages and abandon/retry protocol

**State:** IN PROGRESS. Source S and its registered H assertions, full source
gates, and applicable race gates passed. Required physical F is NOT RUN because
the ordered L3-15/L3-14 acceptance chain is blocked by L2-07 Linux fixture and
L2-08 Mac APFS fixture prerequisites. This evidence does not claim a bootstrap
cohort, installed management state, or live host acceptance.

## Pre-unit revision, predecessor, and scope

- Mac `dev` was clean at pre-unit HEAD and predecessor evidence-closeout E
  `a7019903b4851b8f4d5b8beb2794bfdd69640fd0`. Its parent Source S was
  `ef3e96a10eab52c0eb4bda691b8c19990d502b20`; the L3-16 evidence blob at the
  pre-unit HEAD is `aed6e070d03a25ab24ace3271b2d3da078738b0c`.
- The ordered physical predecessor chain remains blocked: L3-15 F is NOT RUN
  behind L3-14 F, which requires the approved Linux controller/device
  prerequisites and disposable Mac APFS fixtures described in L3-14,
  L2-07, and L2-08 evidence. Their pre-unit blobs are
  `0dae129af6ac63a7c7fb863cd50194801fecf1bd`,
  `4b6555ec34039b13b2846d46891d6750850f0a36`, and
  `871fea76b91ed41f0536b89886c4c6942980e929` respectively. No physical
  capacity, bootstrap, or service result is inferred from this source work.
- The owner's incremental-readiness authorization permits this independent
  hermetic source unit to proceed while those later F inputs remain absent.
  L3-17 F remains unstarted until its ordered predecessors and approved
  physical fixtures pass.
- `LANES-B021-02` and `LANES-B024-02` remain BLOCKED and their evidence is
  preserved (`LANES-B021-02.md` blob
  `3e9700485f48ad75ad42ba04334d5b3871a36c7a`; `LANES-B024-02.md` blob
  `15211861260284d0923c9d64259bd7746700399e`). BUG-022/023 remain deferred.
  The historical B021 maintenance package was not rebuilt. No mailbox request
  or ACK was published; no service, publisher, credential, installation,
  storage, firewall, boot setting, retained work, or production Runner state
  was changed. Tests use generated state and test identities only.

## Inputs reviewed at pre-unit HEAD

| Input | Blob |
| --- | --- |
| `AGENTS.md` | `3402877131d9fcea513f5bae756582e4127ee892` |
| Lane initial idea | `503e54457228da265666ae48ceb2be22a0c0b81f` |
| Lane detailed design | `f7404c2d5a0b35706f3a9e867991bffcb6171d93` |
| Phased implementation plan | `082e73ac62a7815c3ed15778eb8162d81a43c347` |
| Execution handbook | `d04a659df5c5834725ea05b9b047f4ad296b21bc` |
| Execution ledger | `7f08a56b6e6cddaf6f3c53a3ed5781bf4cebf42e` |
| Test coverage manifest | `b6d6a76969a7e8c95858cf0f66d48b94ca45d2fd` |
| L3-16 predecessor evidence | `aed6e070d03a25ab24ace3271b2d3da078738b0c` |
| L3-14 / L2-07 / L2-08 blocker evidence | `0dae129af6ac63a7c7fb863cd50194801fecf1bd` / `4b6555ec34039b13b2846d46891d6750850f0a36` / `871fea76b91ed41f0536b89886c4c6942980e929` |
| B021-02 / B024-02 retained evidence | `3e9700485f48ad75ad42ba04334d5b3871a36c7a` / `15211861260284d0923c9d64259bd7746700399e` |

Validation ran on `mac-control-plane`, `linux-host`, `sandbox-host`, and
`factory-sandbox` from Git-synchronized checkouts. No privileged or live F
operation was attempted.

## Source contract implemented

- Adds a closed public bootstrap cohort contract that pins one reviewed source
  revision across Gitea, GitHub, and every candidate artifact. It fixes the
  four-host serial order, host-specific management roots, PKI plans, management
  component matrix, platform mapping, ownership/mode, and toolchain/input/
  artifact digests. The contract carries public key fingerprints only; it
  cannot contain source, artifacts, commands, certificates, credentials, or
  private keys.
- Adds a serial cohort state machine requiring fresh L0 and health evidence at
  each host transition, exact per-host issuance identity, and explicit
  per-host exception disable before moving forward. A partial attempt cannot
  be resumed in place: all hosts must be disabled, the cohort abandoned, and a
  fresh generation and fresh issuance identities used for retry.
- Persists cohort lifecycle and issuance state in the root authority journal.
  Restart/replay restores the in-progress or abandoned state and high-water
  marks. Retry requires a strictly newer generation and new request/receipt
  identities; the original failed result is retained and no generation or
  issuance can be reused.
- Four registered H assertions cover exact contract/pins/platform/path
  mapping, serial fresh-L0/health transitions and exception disable, whole
  cohort abandonment with fresh retry, and persisted authority issuance plus
  restart/replay/high-water behavior. Negative cases reject unsafe or
  inconsistent host mappings, stale or missing evidence, reordered hosts,
  partial disable, and reused identities. All tests use fixtures and test keys.

## Source revision and Mac gates

Source S is `e70f612fc764d2a409b9d6c0d09ea7c16f376583`, parent
`a7019903b4851b8f4d5b8beb2794bfdd69640fd0`.

| Machine | Command | Result |
| --- | --- | --- |
| `mac-control-plane` | `make test-lanes UNIT=LANES-L3-17 LANES_TIER=H` | PASS — four registered assertions |
| `mac-control-plane` | `make check build` | PASS — full Go suite, vet, smoke, command builds |
| `mac-control-plane` | `go test -race ./src/internal/laneinstall ./src/internal/release -run 'TestLanesL31(2|7)' -count=1` | PASS |
| `mac-control-plane` | Coverage JSON parse and `git diff --check` | PASS |

The Mac used the Makefile-pinned Go 1.27.1 toolchain and shared
`GOCACHE=/private/tmp/remote-session-runner-gocache` and
`GOMODCACHE=/private/tmp/remote-session-runner-gomodcache`.

## Git delivery and post-S validation

| Gate | Command or check | Result |
| --- | --- | --- |
| Mac primary | Explicit-key `git push origin dev` using `/Users/tomasz.walczuk/.ssh/local-gitea-key` | PASS — Gitea `origin/dev` is S |
| Mac mirror | Explicit-key `git ls-remote github refs/heads/dev` using `/Users/tomasz.walczuk/.ssh/gavrilo123456-github` | PASS — exact SHA S |
| `linux-host` | Clean `dev` at predecessor; GitHub `origin/dev` `--ff-only` pull; H, full source gates, focused race | PASS at S |
| `sandbox-host` | Clean `dev` at predecessor; GitHub `origin/dev` `--ff-only` pull; H, full source gates, focused race | PASS at S after the temp-space diagnosis below |
| `factory-sandbox` | Clean `dev` at predecessor; GitHub `origin/dev` `--ff-only` pull; H and full source gates | PASS at S; focused race NOT RUN because no C compiler is available (`CGO_ENABLED=1`) |

All three Ubuntu checkouts were clean on `dev` at predecessor
`a7019903b4851b8f4d5b8beb2794bfdd69640fd0`, then fast-forwarded from their
GitHub mirror `origin/dev` to S and left clean at S. SSH used configured
explicit identities and strict host-key checking; no tracked file was edited
or copied outside Git. All hosts used Go 1.27.1 from the Makefile-pinned
toolchain.

On `sandbox-host`, the first combined gate reached the build but failed because
the root filesystem had only 91 MB free; no cache, retained file, or other
user-owned temporary data was removed. A link succeeded when Go build scratch
was redirected to `/dev/shm`, but exporting `GOTMPDIR=/dev/shm` also caused the
`laneroot` fixture tests to reject temp-root ancestry. This was diagnosed by
comparing the focused package with and without that override: it passed
uncached with the normal temp environment and failed with the override. Once
the Go process had exited and root free space was 3.0 GB, the exact clean S
checkout passed `make check build` with `TMPDIR`, `GOTMPDIR`, and `GOFLAGS`
unset. The full suite, including `laneroot` and `localapi`, vet, smoke, and
build all passed. No source change was needed.

## Ordered F gate and current disposition

L3-17 physical F remains NOT RUN. L3-15 F remains NOT RUN behind L3-14 F;
L3-14 F requires the retained L2-07 approved Linux capacity/controller
fixtures and L2-08 disposable Mac APFS fixtures. No real bootstrap pins,
receipts, measurements, authority records, or grants were generated. No
credentials, publisher pause, maintenance window, host service, production
storage, or installed management artifacts were required or changed.

After this evidence-only closeout, continue with the next ordered independent
source unit. Do not rebuild the historical B021 maintenance snapshot; rebuild
and freeze it only when publisher pause and a valid maintenance window are
available, against the then-selected synchronized revision, as the owner
directed.
