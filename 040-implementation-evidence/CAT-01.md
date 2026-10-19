# CAT-01 — sealed release identity and local catalog publication

**Status: CAT-01 source, fixture and delivery gates complete.**

## Scope

Implement the immutable four-host release contract, local-host publication,
and exact read-only identity inspection. This unit does not alter a lane
selector, service definition, installed binary, database, mailbox, or publisher
state. No release has been published on a production host.

The current assignment preserves completed SIMPLE work and historical
LANES/Pnnn evidence. CAT-06 PRD migration still requires a separate concrete
package and owner approval. Mailbox publication and ACKs remain prohibited.

## Baseline

The Mac source checkout was clean on `dev` at predecessor
`c2cac5d78dbf48663a83f6cdae9e72050bf90661`. Each Ubuntu `dev` checkout was
also clean at that exact SHA:

| Machine | Checkout | Initial revision | State |
| --- | --- | --- | --- |
| Mac | `/Users/tomasz.walczuk/projects/remote-session-runner` | `c2cac5d78dbf48663a83f6cdae9e72050bf90661` | clean |
| `linux-host` | `/home/ubuntu/projects/remote-session-runner` | same | clean |
| `sandbox-host` | `/home/ubuntu/projects/remote-session-runner` | same | clean |
| `factory-sandbox` | `/home/ubuntu/projects/remote-session-runner` | same | clean |

## Implementation

- `CatalogReleaseManifest` binds one safe immutable release ID, common source
  SHA/toolchain/database compatibility, and exactly four canonical host
  platform/bundle/component-manifest digests.
- Manifest generation accepts validated per-host bundle metadata so the
  sealing step does not require collecting foreign-host executables. Each
  publication host independently revalidates its complete local bundle.
- Publication stages a private release directory and atomically renames it to
  the immutable label. Exact repeats are idempotent; conflicting bytes, bad
  host/platform/source metadata, traversal, and symlink paths fail closed.
- Sealed release directories are owner read/execute only (`0500`), metadata is
  owner readable (`0400`), and executable components are owner read/execute
  only (`0500`). Interrupted `.catalog-stage-*` directories are not selectable.
- `runner-lane catalog-manifest`, `catalog-publish`, and `catalog-inspect`
  expose manifest sealing, host-local publication, and validated identity
  inspection under `~/projects-runtime/runner-releases/`. Publish/inspect
  require the actual host platform and expected existing account.
- Existing SIMPLE bundle/staging permissions and current lane installation
  behavior are unchanged.

## Source checks

| Machine | Command | Result |
| --- | --- | --- |
| Mac | Explicit Go 1.27.1 `go test ./src/internal/laneinstall ./src/cmd/runner-lane` using the shared repository caches | PASS |
| Mac | Explicit Go 1.27.1 `gofmt` on changed Go files | PASS |
| Mac | `make check GO='<explicit Go 1.27.1 binary>'` with the shared repository caches | PASS: all tests, `go vet ./...`, and command smoke checks |
| Mac | `git diff --check` | PASS after source and documentation edits |
| `linux-host` | Go 1.27.1 `go test ./src/internal/laneinstall ./src/cmd/runner-lane -run 'TestCatalog|TestSimpleBundle' -count=1` | PASS |
| `sandbox-host` | Same Go test command with task-owned `GOTMPDIR` under `/home/ubuntu/.cache` | PASS; root retained 996 MB free afterward |
| `factory-sandbox` | Same Go test command with task-owned `GOTMPDIR` under `/home/ubuntu/.cache` | PASS after the default `/tmp` attempt failed to link with `disk quota exceeded`; `/tmp` was a 476 MB tmpfs with 118 MB free. Root retained 37 GB free. The task-owned temp directory was removed by its shell trap; shared Go cache was preserved. |

Fixture coverage includes exact four-host identity, common source/toolchain,
schema declarations, wrong platform, missing/foreign components, unsafe
labels, traversal, exact idempotent import, tampering, unexpected entries,
symlinked roots, interrupted staging, concurrent conflicting publication, and
hard-linked executable rejection and sealed file/directory modes. The suite
printed non-fatal Darwin linker warnings about a duplicate `-lproc`; all tests,
vet and smoke gates completed successfully. Ubuntu checks are source/fixture
results only and do not qualify installed Dev/QAS or PRD.

## Live state and omitted acceptance

No catalog publish, selector switch, service operation, database operation or
host filesystem cleanup was run in CAT-01. Dev and QAS installed acceptance are
**NOT RUN** here and belong to CAT-04/05. PRD migration is **NOT RUN** and
remains gated by CAT-06 approval.
Mailbox request/ACK acceptance is **NOT RUN** under the explicit no-mailbox
instruction. Existing publisher pauses were neither changed nor inferred from
mailbox directory counts.

## Delivery

Source commit `b5fa02085887db796e133b86fecb6dc82ab29139` was pushed to Gitea
`origin/dev`; GitHub `dev` resolved to that exact SHA. The three clean Ubuntu
`dev` checkouts fast-forwarded from `c2cac5d78dbf48663a83f6cdae9e72050bf90661`
to the source SHA before their tests. This file is the evidence-only CAT-01
closeout (`E`); its delivery commit is identified in the final work report.
