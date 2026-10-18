# PRD in-place upgrade — implementation and fresh baseline

**Status:** PRD maintenance is authorized, but not yet started. Guarded
installation, migration, backup, and rollback tooling is implemented in the Mac
source checkout and passed the full Mac source gate. Git delivery and the three
Ubuntu source gates are pending. No PRD service, binary, database, configuration,
route, mailbox file, request, or ACK has been changed. Dev/QAS services remain
on their previously accepted artifacts.

## Scope and controls

This transaction covers only the existing unsuffixed PRD Runner on the Mac and
`linux-host`, `sandbox-host`, and `factory-sandbox`. The service candidate is the
per-host bundle already accepted in Dev and QAS at source revision
`f117853a9f712e734eab787e648383f5468e719c`; the bundle hashes and component
hashes are recorded in [the original diagnostic candidate evidence](PRD-diagnostic-upgrade-2026-10-18.md#per-host-candidate-identity).
No Dev/QAS service refresh is included. Before installation, each actual bundle
must be matched against those recorded hashes.

The owner has explicitly authorized interruption, cancellation, and supported
abandonment of unfinished PRD work, including forced termination when needed.
No successful completion or complete output will be fabricated. Runner mailbox
requests and ACKs will not be published for this maintenance. All existing
mailbox contents and held ACKs remain preserved, and external publishers remain
paused through postflight.

## Implemented PRD maintenance path

- `runner-lane` retains its existing Dev/QAS-only plan and installer. A separate
  fixed-host PRD plan admits only the Mac and three existing Ubuntu profiles.
- `prd-preflight` uses the schema-compatible, read-only database opener and
  reports service state, schema, configuration and current binary hashes, plus
  active sessions, command slots, reservations, queued/running commands,
  resumable jobs, and pending recovery finalizations.
- `prd-backup` writes a private SQLite online snapshot and copies the existing
  PRD binaries, config, secrets, and service definition. It records hashes,
  modes, schema, bundle identity, and both live and snapshot database paths.
- `prd-install` requires the unsuffixed service stopped, unchanged backup inputs,
  no new work identities, and no active or resumable work. Terminal lost work
  may retain slots/reservations so the newly installed guarded recovery utility
  can disposition it. Installation atomically replaces only fixed bundle
  components in the existing binary directory; it does not migrate or restart.
- `prd-migrate` requires a stopped service and a zero execution, queue,
  reservation, and recovery-finalization inventory before and after the
  forward-only migration. The existing guarded recovery CLI may itself apply
  the migration before cleanup.
- `prd-rollback` requires a stopped service and refuses if session, command,
  job, or local-intent identities were accepted after the backup. It restores
  the exact pre-migration database and old binaries, preserves unrelated files,
  and leaves the service stopped. It must never overwrite subsequently
  accepted work.
- Fixed PRD status/stop/start commands address only the unsuffixed launchd
  labels or `runnerd.service`. The operational order and CLI boundary are in
  [`deploy/PRD_UPGRADE.md`](../deploy/PRD_UPGRADE.md).
- The mixed guarded recovery source supports the exact retained Mac set of two
  lost pairs plus the completed exit-2 teardown pair, and the sandbox exit-1
  teardown pair. It preserves original results/events, does not replay commands,
  and requires complete runtime cleanup proof and finalization retry handling.

## Mac source validation

The Mac Go 1.27.1 toolchain and shared Go caches were used. `make check build`
passed with exit status 0. It ran `go test ./...`, `go vet ./...`, command
help/version smoke, and `go build ./src/cmd/...`. The focused source packages
`store`, `execution`, `runnerd`, `runnerlocald`, `laneinstall`, and
`cmd/runner-lane` passed. The complete `runnerlocald` package required the
authorized Mac shell because its existing process-ownership regression invokes
`/bin/ps`, which the restricted shell denies; that full package passed in the
authorized shell. `git diff --check` passed.

These are source gates. Installed Dev/QAS acceptance, host-specific builds, PRD
backups, recovery, migration, installation, and PRD postflight are not yet
claimed as PASS.

## Fresh read-only production baseline

Collected 2026-10-18 UTC from the Mac shell and explicit SSH identities with
verified host keys. No mailbox command was used.

| Host | Installed unsuffixed PRD build/service | Configuration/artifact facts | Fresh database/work result |
|---|---|---|---|
| Mac | `runner-locald` build `77910358a149d6e8fcdf4694498ebdf6c1f07f58`; launchd labels `com.remote-session-runner.local` PID 48708 and `com.remote-session-runner.locald` PID 48627 are running. | `mac.yaml` SHA-256 `65840e66837dedfa17f5c32568d191a0bbfd6098bcc108d7ba4066f44fb95711`; installed `runner-local`, `runner-locald`, and `runner` hashes are `b632233613858ad2541dfd7d18915eb8924df3abe082926b6ece0a1df6b66455`, `e7f15eea593c402fb2b1e7605aafc9d558e3289222d54abade56b8bacb60ec13`, and `9fc0af3f8439451f0e8ed570be9462b93a99746db01ee5c3ba6ae10ffbcd9b3c`. PRD root, bin, config, secrets, and backups are owner-only mode 0700. | Read-only SQLite schema 37; 3 unreleased session reservations, 2 unreleased command slots, 0 queued/running commands, 0 resumable jobs. Retained: failed exit-2 pair `sess-d76592166342428ba1b7b703fc87bb35:cmd-6817a947ceec8c3e54fbcdc2b8f2fb30`; lost pairs `sess-e4e08eb9c942672e900bd0b411f54f1e:cmd-4c76c371ee67375b6af606dbc0ce7668` and `sess-e7e8f0689316b308a9a8e5d15aaf34ae:cmd-36dc71219a33e1901e23272ae5a3127f`. |
| `linux-host` | `runnerd.service` active; PID 149913, running `/home/ubuntu/.local/share/remote-session-runner/bin/runnerd --config /home/ubuntu/.local/share/remote-session-runner/config/linux.yaml`; build `2802f00c93eb51a80f23b9380238c309545d6329`. | Installed `runnerd` SHA-256 `22f33d6cd3d429c6d91537031954222d7a648e3a3d46e1e169ddbeaa89c21104`; bridge SHA-256 `61f8cbfa22a0dec7e2916db7cf6077d5d2716a90f4db0b9f9506494383ec3203`; the old PRD bin lacks `runner-session-agent` and `runner-log-capture`. `linux.yaml` and `client-principals.yaml` hashes match the prior evidence. | Python SQLite reports schema 36, then fails to parse the existing `STRICT` schema feature because its linked SQLite is too old. This is a reader compatibility limitation, not a database corruption finding. Fresh counts/IDs will be read with the synchronized Go preflight before changes. Prior read-only evidence recorded 0 retained sessions/slots. |
| `sandbox-host` | `runnerd.service` active; PID 717823, same installed build revision `2802f00c93eb51a80f23b9380238c309545d6329`. | Installed `runnerd` SHA-256 `b0d864b1ff285f6591868aa463e913537955bf1a0a06130f31e8fdd635004491`; bridge SHA-256 `45a77bf5fcb28a89c23ec1f3718089e8793768c5294b9e97e216f3af8c760934`; the old PRD bin lacks `runner-session-agent` and `runner-log-capture`. Config hashes match the prior evidence. Filesystem has 935 MiB available (99% used); preserve all existing work and caches. | Read-only Python SQLite schema 36; 1 unreleased session reservation, 0 unreleased command slots, 0 queued/running commands, 0 resumable jobs. Retained `sess-57e644562a61b549602aa66769daca16:cmd-edc978d8b67c89c3a39157efb9bfd43f`, session lost and command failed exit 1. |
| `factory-sandbox` | `runnerd.service` active; PID 50302, same installed build revision `2802f00c93eb51a80f23b9380238c309545d6329`. | Installed `runnerd` SHA-256 `fafb7d9ba5d343d425fa1752c21a4859a3f42c344d5eca5a43ee5f3dfc0ee374`; bridge SHA-256 `3a13bff38320ab0098cecea38e349e4868c9ab9702a75f2f3ff88cbaad7ce42c`; the old PRD bin lacks `runner-session-agent` and `runner-log-capture`. Config hashes match the prior evidence. | Read-only Python SQLite schema 36; 0 unreleased session reservations, 0 unreleased command slots, 0 queued/running commands, 0 resumable jobs. |

The Mac `/bin/ps` process listing was denied in the restricted shell; launchd
reported the exact unsuffixed service labels and PIDs. Process and runtime
ownership will be rechecked through supported service/recovery guards after the
service stop. The host Python SQLite limitation will be resolved with
`runner-lane prd-preflight` after Git delivery. The owner stated submissions are
stopped; no mailbox counts are treated as proof of publisher state.

## Delivery and operation status

Source commit, primary Gitea, mirror SHA, and three Ubuntu source gate results
will be appended after delivery. Before PRD maintenance, the exact accepted
bundle contents on all four hosts, fresh per-host preflight, existing mailbox
contents, publisher pause, and available backup capacity must be verified.
Backups, recovery receipts, migration versions, installed hashes, and bounded
postflight are recorded only after their actual execution. Until then, PRD
upgrade and readiness are **NOT RUN**; keep publishers paused.
