# LANES-L1-01 — unit-test dispatcher and V3 namespace derivation

**State:** IN PROGRESS — Mac source H gates pass. Source S delivery and the
required exact-revision Ubuntu validations remain outstanding; no phase PASS
or runtime installation is claimed.

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

## Source S delivery and remaining gates

Source S is the source/docs commit for this unit and will be recorded here by
the evidence-only closeout after its delivery. The required delivery order is
the `AGENTS.md` Mac `dev` commit → primary Gitea push → exact GitHub mirror
verification → clean fast-forward of `linux-host`, `sandbox-host`, and
`factory-sandbox` → exact-S validation. Those delivery and Ubuntu gates remain
**NOT RUN** at this source evidence revision. L1-01 remains IN PROGRESS until
all required checks and evidence-only closeout E are delivered and verified.

No installation, restart, configuration write, candidate build/freeze, live
acceptance, Runner request, ACK, teardown recovery, or publisher action was
performed. The existing held and consumed ACK states are unchanged.
