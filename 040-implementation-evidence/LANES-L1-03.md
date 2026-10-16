# LANES-L1-03 — descriptor-based root/storage handoff validation

**State:** PASS — source-unit H/N tests, full source gates, and exact-S
delivery completed on Mac and all three Ubuntu `dev` checkouts. This does not
provision or accept live lane roots and does not close B021/B024.

## Predecessor and scope

- Source predecessor and synchronized evidence closeout: L1-02 E
  `d9ba3555c6a19c1b707571af9016b809b3fb11bb`. L1-02 used L1-01 E
  `b25468dd09a63a9c85daee2b219dfc61f2adb492` as its source predecessor.
- `LANES-B021-02` remains **BLOCKED**. Its evidence blob is
  `3e9700485f48ad75ad42ba04334d5b3871a36c7a` at both the predecessor and
  final L1-03 source S. `LANES-B024-02` remains **BLOCKED** with evidence blob
  `15211861260284d0923c9d64259bd7746700399e` at both revisions.
- BUG-022/023 baseline repair units remain deferred. No maintenance package
  rebuild, publisher pause, mailbox publication, ACK, credential access,
  service change, storage change, or firewall change was performed.
- The external B021 maintenance package remains a historical snapshot,
  unchanged at SHA-256
  `8bb177560880c1d3ec95ae7fe6dd0ac7cf1deb7e07abf2c18a048e3531d40360`.
- Tests used isolated temporary directory trees and synthetic handoff data.
  No real external root, provisioned volume, production identity, or retained
  mailbox content was read or modified.

## Implemented contract

- Added `src/internal/laneroot`: a declarative handoff validator and a
  read-only descriptor/no-follow inspection for existing roots. It requires
  exactly the fixed `inbox`, `outbox`, `events`, `acks`, and `diagnostics`
  children, owner-only `0700` root/children, safe owner-controlled ancestors,
  and the declared `initialized_empty` or `retained` layout.
- Descriptor identities are checked against the supplied handoff for the
  root, each required child, and every top-level retained ancillary entry.
  Ancillary entries are type/owner/mode checked without traversing their
  contents. The validator rechecks the top-level inventory and resolves the
  path again after inspection; missing, replaced, linked, unsafe, nested, or
  aliased roots fail closed.
- Mac Dev/QAS handoffs require matching expected and freshly supplied APFS
  volume facts (UUID, filesystem, mountpoint, device, and positive quota),
  bound to the opened root's device identity. The native fixture suite tests
  identity mismatches; it does not claim a live APFS volume observation.
- Added `LANES_TIER=H|N` support to the test dispatcher and Makefile. H checks
  the closed handoff and collision rules; N exercises actual OS descriptors
  and temporary filesystem fixtures without privileged operations.

## Failures found and corrected

- An early declarative root-set fixture reused the same synthetic root inode
  for two disjoint roots, so the validator correctly rejected it. The fixture
  was corrected to model distinct root identities before source S.
- Initial Ubuntu N runs at S `247d21b403876dff1effbf9302615db6a27a1960`
  failed on valid fixtures. Diagnostic revisions `f6ecacf0bbf558dfc09d4c28707ba51f2d30e270`
  and `53af075e45d4180e1073dd9355c7c4de227903f0` showed Go's `t.TempDir()`
  created an intermediate `001` directory with mode `0775` under the host's
  umask. The validator correctly rejected that group-writable ancestor. The
  fixture now sets its `t.TempDir()` base to `0700`; S `1ae1253e2737d11beda1246a96c8c8f5263d3c53`
  passed H and N on all hosts.
- The first direct Mac race invocation tried the restricted default Go cache
  and was denied. It was rerun with the Makefile's shared
  `GOCACHE=/private/tmp/remote-session-runner-gocache` and
  `GOMODCACHE=/private/tmp/remote-session-runner-gomodcache` and passed.

## Mac source gates

Commands ran on `mac-control-plane` (Darwin arm64), Go 1.27.1:

| Command | Result |
| --- | --- |
| `make test-lanes UNIT=LANES-L1-03` | PASS; all 6 registered H assertions |
| `make test-lanes UNIT=LANES-L1-03 LANES_TIER=N` | PASS; all 6 registered N assertions |
| `make test` | PASS; complete `./...` suite |
| `make vet` | PASS |
| `make build` | PASS |
| `make smoke` | PASS |
| `go test -race ./src/internal/laneroot ./src/internal/lanetests ./src/cmd/test-lanes` with shared Go caches | PASS |
| `gofmt -d`, `python3 -m json.tool 040-implementation-evidence/LANES-test-coverage.json`, `git diff --check` | PASS |

## Source S and Ubuntu validation

Final source S is
`1ae1253e2737d11beda1246a96c8c8f5263d3c53` on Mac `dev`.
Gitea `origin/dev` and automatic GitHub mirror `github/dev` both resolved to
that exact SHA. Each Ubuntu checkout was clean on `dev` before pulling, then
fast-forwarded from its GitHub `origin/dev` using
`/home/ubuntu/.ssh/gavrilo123456-github`. No tracked file was edited or copied
on Ubuntu.

At exact S each host ran, in order, `make test-lanes UNIT=LANES-L1-03`,
`make test-lanes UNIT=LANES-L1-03 LANES_TIER=N`, `make test`, `make vet`,
`make build`, and `make smoke`. Each command passed on all three hosts. The
focused race command
`GOTOOLCHAIN=local /home/ubuntu/.local/share/remote-session-runner/toolchains/go1.27.1/bin/go test -race ./src/internal/laneroot ./src/internal/lanetests ./src/cmd/test-lanes`
passed on linux-host and sandbox-host. Factory-sandbox did not run race because
`CGO_ENABLED=0` and `cc` is unavailable.

| Host label | SSH alias / identity | Platform | H/N and full source gates | Focused race | Final checkout |
| --- | --- | --- | --- | --- | --- |
| `linux-host` | `remote-session-runner` / `~/.ssh/remote-session-runner` | Linux amd64, Go 1.27.1 | PASS | PASS | clean `dev`, exact S |
| `sandbox-host` | `sandbox.env` / `~/.ssh/dev.slidestud.io` | Linux arm64, Go 1.27.1 | PASS | PASS | clean `dev`, exact S |
| `factory-sandbox` | `factory-sandbox` / `~/.ssh/factory-sandbox` | Linux amd64, Go 1.27.1 | PASS | NOT RUN — `CGO_ENABLED=0`, no `cc` | clean `dev`, exact S |

SSH used explicit identities, `IdentitiesOnly=yes`, batch mode, strict verified
host keys, and `UpdateHostKeys=no`. The Gitea push succeeded; the sandbox
denied the optional local `refs/remotes/origin/dev` update, so primary and
mirror delivery were verified with direct `git ls-remote` calls.

Source/evidence delivery remains two steps: this S was synchronized and
validated first; the evidence/ledger E closeout is delivered separately under
the same Gitea → exact GitHub mirror → three-host fast-forward workflow. No
installation or runtime acceptance is implied.
