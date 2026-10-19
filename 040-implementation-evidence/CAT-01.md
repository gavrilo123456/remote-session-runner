# CAT-01 — sealed release identity and local catalog publication

**Status: source implemented locally; delivery and native-host validation pending.**

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

Fixture coverage includes exact four-host identity, common source/toolchain,
schema declarations, wrong platform, missing/foreign components, unsafe
labels, traversal, exact idempotent import, tampering, unexpected entries,
symlinked roots, interrupted staging, concurrent conflicting publication, and
hard-linked executable rejection and sealed file/directory modes. The suite
printed non-fatal Darwin linker warnings about a duplicate `-lproc`; all tests,
vet and smoke gates completed successfully. Native Ubuntu-host validation is
not yet recorded in this initial source entry.

## Live state and omitted acceptance

No catalog publish, selector switch, service operation, database operation or
host filesystem cleanup was run in CAT-01. Dev and QAS installed acceptance are
**NOT RUN** here and belong to CAT-04/05. PRD migration is **NOT RUN** and
remains gated by CAT-06 approval.
Mailbox request/ACK acceptance is **NOT RUN** under the explicit no-mailbox
instruction. Existing publisher pauses were neither changed nor inferred from
mailbox directory counts.

## Delivery

The CAT-01 source commit, Gitea primary push, exact GitHub mirror SHA, and
fast-forwarded Ubuntu checkout revisions will be added in the source/closeout
delivery record before CAT-01 is declared delivered.
