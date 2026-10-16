# LANES-L1-04 — durable mailbox-root registry

**State:** PASS — seven registered H assertions, full Mac and Ubuntu source
gates, and exact-S delivery completed. This is source-level registry behavior;
it does not install or activate a lane service.

## Predecessor and scope

- Source predecessor and synchronized evidence closeout: L1-03 E
  `2ff96c0dc83eb11e70d11c5b9968a55b05e95749`.
- Final source S: `1c6d72e7b8f05a8879d1a716e79d00d3bfa946a7`.
- `LANES-B021-02` remains **BLOCKED** and `LANES-B024-02` remains
  **BLOCKED**. Their evidence blobs are unchanged from the predecessor to S:
  `LANES-B021-02.md` `3e9700485f48ad75ad42ba04334d5b3871a36c7a`, and
  `LANES-B024-02.md` `15211861260284d0923c9d64259bd7746700399e`.
- BUG-022/023 baseline repair units remain deferred. The B021 maintenance
  snapshot remains historical at SHA-256
  `8bb177560880c1d3ec95ae7fe6dd0ac7cf1deb7e07abf2c18a048e3531d40360`.
- All filesystem tests used isolated `t.TempDir()` fixtures and synthetic
  root/volume identities. No production mailbox, retained work, credential,
  service, storage, firewall, or publisher state was read or changed.

## Implemented contract

- Added `src/internal/laneregistry`, a Mac-wide coordination mirror that stores
  canonical JSON and a separate owner-only lock file. The directory is `0700`,
  files are `0600`, and registry and lock paths are traversed with
  descriptor-relative `O_NOFOLLOW` opens. Reads reject missing, linked,
  noncanonical, unknown-field, duplicate-key, malformed, oversized, wrong-owner,
  and unsafe-mode state; `Open` never creates or repairs state. Explicit
  first-time `Initialize` is documented as a separately authorized genesis,
  never a recovery path.
- Snapshot updates use an advisory lock, generation compare-and-swap, a synced
  temporary file, atomic rename, and directory `fsync`. Root records retain the
  normalized path and descriptor identity, lane/inbox, configuration digest,
  epoch, declared ancillary inventory, and (for Mac Dev/QAS) APFS UUID,
  mountpoint, device, and quota facts.
- The closed state set is `legacy_prd_reserved`, `prepared`, `active`,
  `superseded`, and `retired`. Every state remains in collision checks. A
  prepared retry is idempotent only for its exact existing record. A
  same-lane successor names its predecessor and uses a higher epoch; activation
  requires a matching service-reconciliation identity and atomically retains
  the predecessor as `superseded`. Rollback is represented as a new higher
  epoch, never by reactivating an old row.
- Legacy PRD reservation and reviewed refresh append retained history without
  reading or mutating V2 files. The registry contains no request bodies,
  credentials, executable authority, mailbox content, or SQLite database.
- This source unit provides the registry mirror and structural guards. It does
  not itself establish root-owned signing authority, infer valid old bytes to
  be a rollback, reconcile installed services, or certify a live lane. Those
  checks remain with the later authority/integration and operational gates.

## Mac gates

Ran on `mac-control-plane` (Darwin arm64), Go 1.27.1, using the Makefile's
shared `/private/tmp/remote-session-runner-gocache` and
`/private/tmp/remote-session-runner-gomodcache`:

| Command | Result |
| --- | --- |
| `make test-lanes UNIT=LANES-L1-04` | PASS; all 7 registered H assertions |
| `make test` | PASS; complete `./...` suite |
| `make vet` | PASS |
| `make build` | PASS |
| `make smoke` | PASS |
| `go test -race ./src/internal/laneregistry ./src/internal/lanetests ./src/cmd/test-lanes` | PASS |
| `gofmt -d`, `python3 -m json.tool 040-implementation-evidence/LANES-test-coverage.json`, `git diff --check` | PASS |

An initial direct `go test` invocation omitted the prescribed shared caches and
was denied while writing the default Go caches. The same package check passed
with the Makefile cache paths; all final gates above used the configured
toolchain and caches.

## Source delivery and Ubuntu validation

Final source S is on Mac `dev`, primary Gitea `origin/dev`, and automatic
GitHub mirror `github/dev` at the exact SHA
`1c6d72e7b8f05a8879d1a716e79d00d3bfa946a7`. Each Ubuntu checkout was clean on
`dev` at `fed3944f31a3093a298d5e8b05cd90cdbe442595`, then fast-forwarded from
GitHub to S using `/home/ubuntu/.ssh/gavrilo123456-github`. Each final checkout
remained clean on `dev` at the exact S after validation. No tracked source was
edited or copied on Ubuntu.

Each host ran `make test-lanes UNIT=LANES-L1-04`, `make test`, `make vet`,
`make build`, and `make smoke` at S; every command passed. The focused race
command above also passed on linux-host and sandbox-host. Factory-sandbox did
not run race: its configured Go reports `CGO_ENABLED=0` and `cc` is unavailable.

| Host | SSH alias / Mac identity | Platform and toolchain | H and full source gates | Focused race | Final checkout |
| --- | --- | --- | --- | --- | --- |
| `linux-host` | `remote-session-runner` / `~/.ssh/remote-session-runner` | Linux amd64, Go 1.27.1 | PASS | PASS | clean `dev`, exact S |
| `sandbox-host` | `sandbox.env` / `~/.ssh/dev.slidestud.io` | Linux arm64, Go 1.27.1 | PASS | PASS | clean `dev`, exact S |
| `factory-sandbox` | `factory-sandbox` / `~/.ssh/factory-sandbox` | Linux amd64, Go 1.27.1 | PASS | NOT RUN — `CGO_ENABLED=0`, no `cc` | clean `dev`, exact S |

SSH used explicit identities, `IdentitiesOnly=yes`, batch mode, strict verified
host keys, and `UpdateHostKeys=no`. Gitea accepted the push. The sandbox
blocked only the optional local `refs/remotes/origin/dev` update, so the
primary and mirror refs were verified directly with `git ls-remote`.

The source S was synchronized and validated before this evidence/ledger E
closeout. No service installation, mailbox publication, ACK, publisher pause,
or runtime acceptance is implied.
