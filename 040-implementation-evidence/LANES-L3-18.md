# LANES-L3-18 — complete platform/no-bypass rehearsal and live package preparation

**State:** IN PROGRESS. Source S, the two registered H assertions, every
L3-01–18 H gate on Mac and all three Ubuntu platforms, and `make check build`
passed on all four hosts. The required physical F rehearsal is NOT RUN because
the ordered L3-17/L3-15/L3-14 acceptance chain remains blocked by L2-07 Linux
fixture and L2-08 Mac APFS fixture prerequisites. This evidence does not claim
a fresh L0, a materialized live operation package, privileged bootstrap, or
host-management installation.

## Pre-unit revision, predecessor, and scope

- Mac `dev` was clean at pre-unit HEAD and predecessor evidence-closeout E
  `7af4bd9dcdf95d024f4eb3bba196752a5a44c6a9`. Its parent Source S was
  `e70f612fc764d2a409b9d6c0d09ea7c16f376583`; the L3-17 evidence blob at the
  pre-unit HEAD is `61d7c2cef6af6eedaaae7693877d2cbd22a25f92`.
- The immediate ordered F predecessor, L3-17, remains IN PROGRESS with F NOT
  RUN at Source S `e70f612fc764d2a409b9d6c0d09ea7c16f376583`. Its exact
  blocker evidence is `040-implementation-evidence/LANES-L3-17.md` (pre-unit
  blob `61d7c2cef6af6eedaaae7693877d2cbd22a25f92`). L3-17 F is blocked by
  L3-15/L3-14 physical acceptance; L3-14 requires the approved Linux
  controller/device prerequisites and disposable Mac APFS fixtures described
  in `LANES-L3-14.md`, `LANES-L2-07.md`, and `LANES-L2-08.md`. Their blobs at
  the pre-unit revision are `0dae129af6ac63a7c7fb863cd50194801fecf1bd`,
  `4b6555ec34039b13b2846d46891d6750850f0a36`, and
  `871fea76b91ed41f0536b89886c4c6942980e929`. L2-07 and L2-08 remain
  BLOCKED; no native control or capacity result is inferred here.
- The owner's incremental-readiness authorization permits this independent
  source rehearsal work while those physical predecessors remain blocked.
  L3-18 F remains unstarted; later-generated pins, CSR receipts, measurements,
  mandates, and grants are not fabricated or pulled forward from L3-19/20.
- `LANES-B021-02` and `LANES-B024-02` remain BLOCKED with retained evidence;
  BUG-022/023 remain deferred. Their maintenance package was not rebuilt. No
  Runner request or ACK was published; no service, publisher, credential,
  installation, storage, firewall, boot setting, retained work, or production
  Runner state was changed.

## Inputs reviewed at pre-unit HEAD

| Input | Blob |
| --- | --- |
| `AGENTS.md` | `3402877131d9fcea513f5bae756582e4127ee892` |
| Lane initial idea | `503e54457228da265666ae48ceb2be22a0c0b81f` |
| Lane detailed design | `f7404c2d5a0b35706f3a9e867991bffcb6171d93` |
| Phased implementation plan | `082e73ac62a7815c3ed15778eb8162d81a43c347` |
| Execution handbook | `d04a659df5c5834725ea05b9b047f4ad296b21bc` |
| Execution ledger | `7e83e7a5ffc7ee787ca5eac4a335e70d3745eb83` |
| Test coverage manifest | `aa04f11a04f12c4379d92e2ab5044cfa430ae3b6` |
| L3-17 predecessor evidence | `61d7c2cef6af6eedaaae7693877d2cbd22a25f92` |
| L3-14 / L2-07 / L2-08 blocker evidence | `0dae129af6ac63a7c7fb863cd50194801fecf1bd` / `4b6555ec34039b13b2846d46891d6750850f0a36` / `871fea76b91ed41f0536b89886c4c6942980e929` |

## Source rehearsal checks implemented

- Registers the previously unimplemented L3-18 H tier with two assertions. The
  first requires every prior L3-01 through L3-17 unit to retain its required
  H tier and a nonempty, explicitly registered assertion list; it also checks
  that L3-18 retains its declared H+F tiers and both assertions.
- The second reads the existing approval checkpoint table and enforces the
  fixed A01–A06 order. This guards the required storage/fixtures, networking,
  bootstrap cohort, bootstrap PKI, authority records, and renewal-grant review
  sequence without generating future keys, pins, receipts, or signed bodies.
- All prior L3 H assertion sets were run individually on all four source
  platforms, in order, followed by L3-18 H. This checks the registered
  no-bypass/source assertions across Mac, Linux AMD64, Linux ARM64, and factory
  AMD64 before the full source gates.
- These are structural and hermetic rehearsal checks. They do not produce the
  fresh unsigned L0 inventory or materialize live operation packages, and they
  do not stand in for the ordered F or later approved bootstrap.

## Source revision and Mac gates

Source S is `29cbdf7c981d02a5a5fbc231027f9ceb6c583b98`, parent
`7af4bd9dcdf95d024f4eb3bba196752a5a44c6a9`.

| Machine | Command | Result |
| --- | --- | --- |
| `mac-control-plane` | For each exact ID L3-01 through L3-18, `make test-lanes UNIT=<exact ID> LANES_TIER=H` | PASS — all registered prior L3 assertions and both L3-18 assertions |
| `mac-control-plane` | `make check build` | PASS — full Go suite, vet, smoke, command builds |
| `mac-control-plane` | `go test -race ./src/internal/lanetests -run 'TestLanesL318' -count=1` | PASS |
| `mac-control-plane` | Coverage JSON parse and `git diff --check` | PASS |

The Mac used the Makefile-pinned Go 1.27.1 toolchain and shared
`GOCACHE=/private/tmp/remote-session-runner-gocache` and
`GOMODCACHE=/private/tmp/remote-session-runner-gomodcache`.

## Git delivery and post-S validation

| Gate | Command or check | Result |
| --- | --- | --- |
| Mac primary | Explicit-key `git push origin dev` using `/Users/tomasz.walczuk/.ssh/local-gitea-key` | PASS — Gitea `origin/dev` is S |
| Mac mirror | Explicit-key `git ls-remote github refs/heads/dev` using `/Users/tomasz.walczuk/.ssh/gavrilo123456-github` | PASS — exact SHA S |
| `linux-host` | Clean `dev` at predecessor; GitHub `origin/dev` `--ff-only` pull; all 18 H gates, full source gates, focused race | PASS at S |
| `sandbox-host` | Clean `dev` at predecessor; GitHub `origin/dev` `--ff-only` pull; all 18 H gates, full source gates, focused race | PASS at S |
| `factory-sandbox` | Clean `dev` at predecessor; GitHub `origin/dev` `--ff-only` pull; all 18 H gates and full source gates | PASS at S; focused race NOT RUN because `cc` is unavailable |

All three Ubuntu checkouts were clean on `dev` at predecessor
`7af4bd9dcdf95d024f4eb3bba196752a5a44c6a9`, then fast-forwarded from GitHub
`origin/dev` to S and left clean at S. The first test-loop wrapper stopped on
Linux and factory because those hosts lack `rg`; no assertion failed. The
complete ordered H loops were rerun using `grep` and all assertions passed.
All hosts used explicit SSH identities, strict host-key checking, and the
Makefile-pinned Go 1.27.1 toolchain. No tracked file was edited or copied
outside Git. Gitea accepted the source push, but the local push process could
not lock `.git/refs/remotes/origin/dev`; direct `ls-remote` checks verified the
primary and mirror at S, and an explicit-key fetch then reconciled the local
tracking ref. No force push or manual mirror push was used.

| Machine | Full gate | Rehearsal and race result |
| --- | --- | --- |
| `linux-host` | `make check build` PASS | L3-01–18 H PASS; focused `go test -race ./src/internal/lanetests -run 'TestLanesL318' -count=1` PASS |
| `sandbox-host` | `make check build` PASS | L3-01–18 H PASS; focused `go test -race ./src/internal/lanetests -run 'TestLanesL318' -count=1` PASS |
| `factory-sandbox` | `make check build` PASS | L3-01–18 H PASS; focused race NOT RUN (`cc` unavailable) |

The first Mac focused-race invocation used the Go binary directly without the
Makefile's shared cache variables and was denied access to the default Go
cache. Repeating it with the configured shared Mac `GOCACHE` and `GOMODCACHE`
passed. No source change was needed for either wrapper/environment issue.

## Ordered F gate and current disposition

L3-18 physical F remains NOT RUN. Its fresh unsigned L0 and concrete live
operation package rehearsal await the ordered L3-17/L3-15/L3-14 physical
predecessors, including the missing L2-07 Linux fixtures and L2-08 Mac APFS
fixtures. A03/A04 public pins, CSR receipts, keys, signed measurements,
authority records, and grants remain just-in-time checkpoint values; this unit
does not fabricate them. No bootstrap, host-management policy, lane root,
mailbox, or lane service was created or changed.

L3-19 is a continuous approved bootstrap transaction and cannot start before
the L3-18 F package and its exact A03/A04 approvals pass. Continue source
implementation only when the next ordered unit has an independent source tier;
preserve B021/B024 evidence and the historical maintenance snapshot.
