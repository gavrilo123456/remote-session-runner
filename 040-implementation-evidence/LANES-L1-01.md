# LANES-L1-01 — unit-test dispatcher and V3 namespace derivation

**State:** PASS — L1-01's H source contract and exact-S delivery are verified
on Mac and all three Ubuntu `dev` checkouts. This is source-unit acceptance
only; it does not close B021-02, claim a lane installation, or pass live
acceptance.

## Preflight and predecessor disposition

- Pre-unit Mac `dev` HEAD was the clean, synchronized
  `222bb2345e6f73a7f09ea50ce9b69880d3b5103c` on 2026-10-16. Its amended plan,
  handbook, ledger, and `AGENTS.md` were reloaded before this unit.
- The active baseline case remains [LANES-B021-02](LANES-B021-02.md), BLOCKED.
  Its exact retained failed request/recovery identity and evidence are unchanged.
  The earlier 30-minute window expired at 18:39:40 CEST; the latest owner
  instruction supplies no new window and does not attest a publisher pause.
  Its exact blocker evidence blob at pre-unit HEAD is
  `60b28c13e3ad38851d776ec054438a3ee8cb54bc`. This independent source unit
  does not close B021-02, refresh a candidate, publish a request/ACK, or run
  recovery or the timeout probe.
- [LANES-B024-02](LANES-B024-02.md) also remains BLOCKED on A08 producer pause
  evidence. BUG-022/023 baseline units remain DEFERRED and OPEN; none is
  started or used as an L1 predecessor.
- This H work is hermetic source code and temporary test fixtures. It does not
  require future lane volumes, generated identifiers, provisioning, a live
  service, Runner mailbox work, publisher pause, or the later L2 pressure
  results. It does not pass PREP-00, L0, readiness, B021-02, or any installed
  acceptance gate.

## Read inventory at pre-unit HEAD

The following planning inputs and repository history were read for this unit;
blob IDs are from pre-unit HEAD `222bb2345e6f73a7f09ea50ce9b69880d3b5103c`.

| Input | Blob |
| --- | --- |
| `AGENTS.md` | `3402877131d9fcea513f5bae756582e4127ee892` |
| Lane initial idea | `503e54457228da265666ae48ceb2be22a0c0b81f` |
| Lane detailed design | `f7404c2d5a0b35706f3a9e867991bffcb6171d93` |
| Lane phased plan | `082e73ac62a7815c3ed15778eb8162d81a43c347` |
| Lane execution handbook | `d04a659df5c5834725ea05b9b047f4ad296b21bc` |
| Original PoC initial design | `308dfb82a25029a786f8d8341e6a89940a2eba0b` |
| Original PoC detailed design | `587654e9b71948519cc6a9e8d0c4813405e42e29` |
| Original PoC phased plan | `260bc76b32da215bfc8ad0f1adf05889aa6c244f` |
| Lane execution ledger | `6043d026e519511c6bab31e5876d25a1ea57c616` |
| Blocked predecessor `LANES-B021-02.md` | `60b28c13e3ad38851d776ec054438a3ee8cb54bc` |
| `LANES-B021-01.md` | `5323fa6593d30dff56116e1d981c0352f7580654` |
| `LANES-B024-02.md` | `15211861260284d0923c9d64259bd7746700399e` |
| BUG-021 report | `0c68ed8d0052e77c35c6d32e4f99bec5f54693eb` |
| BUG-024 report | `59d95f49ee38f9decc75a21b7f9c1d67ffd960c9` |
| `src/internal/config/config.go` | `5108f701fa8e6c1b081ca0869a214c2c92eec49a` |
| `src/internal/config/v2.go` | `1e113dd8734ba3302673bf931b129722bfce28c3` |
| `src/internal/config/config_test.go` | `2d5983108660ab21f50e05a51d2aca0030ab0cc3` |
| `Makefile` | `b20ca0c435b536332bdd0217b3297fe3ae6e80c2` |

The unit also reviewed the current configuration package conventions and its
compatibility fixtures. No source file was edited on Ubuntu. New files in this
unit are the dispatcher, coverage manifest, namespace derivation/parser/schema,
and their hermetic tests.

## Implemented scope

- Added the exact-unit `make test-lanes UNIT=...` dispatcher. It loads the
  checked-in coverage manifest, checks the handbook's unit table and all ten
  design acceptance IDs, and runs only registered H tests for the selected
  unit. It fails for unknown units, an undeclared H tier, missing assertions,
  malformed test output, skipped assertions, or an assertion that did not run
  and pass.
- Added traceability for all 68 handbook units and all ten design acceptance
  items. Only L1-01 has H assertions in this unit; later units are not treated
  as implemented by the coverage rows.
- Added the closed V3 namespace schema and pure derivation for Mac and Linux
  lane roots, configuration/database paths and WAL/SHM sidecars, sockets,
  workspaces, temporary/backup/log/staging paths, lifecycle locks, executable
  paths, Mac registry root, launch-agent labels/plists, Linux units/entrypoint/
  journal identifiers, ports, endpoints, and accepted remote profiles. The
  parser accepts exactly one host and lane, rejects unknown YAML fields and
  caller-supplied namespace deviations, and rejects Dev/QAS local execution
  targets. The canonical V1/V2 loader and fixtures remain unchanged; this
  namespace subset is not presented as a complete V3 runtime configuration.

## Mac source H validation

All commands ran on `mac-control-plane` (Darwin arm64), at the uncommitted
L1-01 source tree based on pre-unit HEAD `222bb2345e6f73a7f09ea50ce9b69880d3b5103c`,
using Go 1.27.1 and the Makefile's shared caches. No Runner route was used;
there was no mailbox publication or host/service operation.

| Command | Result |
| --- | --- |
| `make test-lanes UNIT=LANES-L1-01` | PASS; all 5 registered H assertions ran and passed |
| `make test` (restricted shell attempt) | Initial attempt failed because the existing Darwin host-process test could not execute `/bin/ps` (`operation not permitted`) |
| `make test` (authorized Mac shell) | PASS; full `./...` suite, including `runnerlocald`, passed |
| `make vet` | PASS |
| `make build` | PASS |
| `make smoke` | PASS |
| `go test -race ./src/internal/config ./src/internal/lanetests -run '^TestLanesL101' -count=1` | PASS for both packages |
| `gofmt` on changed Go files; `git diff --check` | PASS |

The authorized-shell rerun was necessary only because the restricted shell
blocked `/bin/ps` in unrelated existing test
`TestBUG020MacShellExitWithInheritedControlWriterSettlesOnce`. The failure
was not suppressed; the identical full suite passed on the authorized Mac
shell. Darwin linker warnings about duplicate `-lproc` were non-fatal.

## Source S delivery and H validation

Source S is `79221a9714561ebb97cc219de66150fc51191e32`, committed on Mac
`dev` as `phase(LANES-L1-01): add test dispatcher and V3 namespace derivation`.
It was pushed to primary Gitea using the explicit
`/Users/tomasz.walczuk/.ssh/local-gitea-key`; `origin/dev` resolved to S. The
push updated Gitea successfully but the restricted shell could not update the
Mac's local `refs/remotes/origin/dev`. An explicit-key fetch refreshed that
tracking ref, leaving Mac `dev` clean and equal to `origin/dev` at S.

The automatic GitHub mirror was checked with
`/Users/tomasz.walczuk/.ssh/gavrilo123456-github`; `github/dev` resolved to the
exact same S SHA before any Ubuntu pull.

All three Ubuntu hosts were checked on `dev`, clean, with the expected GitHub
mirror `origin` and pre-pull HEAD `222bb2345e6f73a7f09ea50ce9b69880d3b5103c`.
Each used its configured explicit Mac SSH identity with
`IdentitiesOnly=yes`, `BatchMode=yes`, `StrictHostKeyChecking=yes`, and
`UpdateHostKeys=no`; each then used the explicit Ubuntu GitHub identity
`/home/ubuntu/.ssh/gavrilo123456-github` for `git pull --ff-only origin dev`.
Every post-pull and final validation check found branch `dev`, clean worktree,
expected origin, and HEAD exactly S.

Runner was not used for the H tests because the prior Runner-routed exact-pair
test remains blocked on retained lost teardown and its guarded recovery has
not occurred. The owner-authorized SSH fallback ran tests only in the clean
synchronized source checkout; it did not edit tracked source on Ubuntu or
touch installed services, mailbox files, requests, or ACKs.

| Machine | Validation at exact S | Result |
| --- | --- | --- |
| Mac `mac-control-plane` (Darwin arm64, Go 1.27.1) | `make test-lanes UNIT=LANES-L1-01`; full `make test`; `make vet`; `make build`; `make smoke`; `go test -race ./src/internal/config ./src/internal/lanetests -run '^TestLanesL101' -count=1`; `gofmt`, JSON parse, `git diff --check` | PASS. The initial restricted-shell full suite hit `/bin/ps: operation not permitted` in unrelated `TestBUG020MacShellExitWithInheritedControlWriterSettlesOnce`; the same full suite passed from the authorized Mac shell |
| `linux-host` (Linux amd64, Go 1.27.1) | `make test-lanes UNIT=LANES-L1-01`; `make test`; `make vet`; `make build`; `make smoke`; `GOTOOLCHAIN=local /home/ubuntu/.local/share/remote-session-runner/toolchains/go1.27.1/bin/go test -race ./src/internal/config ./src/internal/lanetests -run '^TestLanesL101' -count=1` | PASS; final HEAD S and worktree clean |
| `sandbox-host` (Linux arm64, Go 1.27.1) | Same H, full-suite, vet, build, smoke, and focused race commands as `linux-host` | PASS; final HEAD S and worktree clean. One combined SSH command session did not return after package output stopped; a read-only process check showed no active Go test/build process. The local SSH client was closed, then each required command was rerun separately and returned PASS |
| `factory-sandbox` (Linux amd64, Go 1.27.1) | `make test-lanes UNIT=LANES-L1-01`; `make test`; `make vet`; `make build`; `make smoke`; `go env CGO_ENABLED`; `command -v cc` | H/full/vet/build/smoke PASS; final HEAD S and worktree clean. `CGO_ENABLED=0` and `cc` is absent, so the focused race test is NOT RUN (unavailable), not PASS |

No L1-01-specific native, privileged, installation, or live acceptance gate is
specified beyond H. Those later proof tiers and all lane acceptance checks
remain pending in their ordered units. This evidence-only closeout E records
the completed H source unit; its own Git delivery remains the final required
delivery check before advancing to another unit.

No installation, restart, configuration write, candidate build/freeze, live
acceptance, Runner request, ACK, teardown recovery, or publisher action was
performed. The existing held and consumed ACK states are unchanged.
