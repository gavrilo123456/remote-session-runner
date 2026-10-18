# PRD in-place upgrade — implementation and fresh baseline

**Status (2026-10-18, in progress):** The authorized PRD maintenance is underway.
All four unsuffixed PRD services are stopped. The exact previously accepted
`f117853a9f712e734eab787e648383f5468e719c` bundles are installed on Mac and the
three Ubuntu workers; their configs and routes remain unchanged. Mac, linux-host,
and factory-sandbox are schema 39 with zero retained capacity. sandbox-host is
schema 39 but still has its one retained session reservation because the
current recovery guard initially rejected its remote queued-Mac identity. A
focused source correction and regression now pass on Mac; primary/mirror
delivery, Ubuntu source gates, and the guarded sandbox retry remain pending.
No service has been restarted. No mailbox request or ACK was published, and
Dev/QAS services were not changed.

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
  teardown pair. Linux recovery accepts remote `queued_mac` and `direct_mtls`
  sessions only after the same exact identity, complete-output, released-slot,
  idle-inventory, and runtime cleanup checks. It preserves original
  results/events, does not replay commands, and requires finalization retry
  handling.

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

## Initial read-only production baseline before maintenance

Collected 2026-10-18 UTC from the Mac shell and explicit SSH identities with
verified host keys. No mailbox command was used.

| Host | Installed unsuffixed PRD build/service | Configuration/artifact facts | Fresh database/work result |
|---|---|---|---|
| Mac | `runner-locald` build `77910358a149d6e8fcdf4694498ebdf6c1f07f58`; launchd labels `com.remote-session-runner.local` PID 48708 and `com.remote-session-runner.locald` PID 48627 are running. | `mac.yaml` SHA-256 `65840e66837dedfa17f5c32568d191a0bbfd6098bcc108d7ba4066f44fb95711`; installed `runner-local`, `runner-locald`, and `runner` hashes are `b632233613858ad2541dfd7d18915eb8924df3abe082926b6ece0a1df6b66455`, `e7f15eea593c402fb2b1e7605aafc9d558e3289222d54abade56b8bacb60ec13`, and `9fc0af3f8439451f0e8ed570be9462b93a99746db01ee5c3ba6ae10ffbcd9b3c`. PRD root, bin, config, secrets, and backups are owner-only mode 0700. | Read-only SQLite schema 37; 3 unreleased session reservations, 2 unreleased command slots, 0 queued/running commands, 0 resumable jobs. Retained: failed exit-2 pair `sess-d76592166342428ba1b7b703fc87bb35:cmd-6817a947ceec8c3e54fbcdc2b8f2fb30`; lost pairs `sess-e4e08eb9c942672e900bd0b411f54f1e:cmd-4c76c371ee67375b6af606dbc0ce7668` and `sess-e7e8f0689316b308a9a8e5d15aaf34ae:cmd-36dc71219a33e1901e23272ae5a3127f`. |
| `linux-host` | `runnerd.service` active; PID 149913, running `/home/ubuntu/.local/share/remote-session-runner/bin/runnerd --config /home/ubuntu/.local/share/remote-session-runner/config/linux.yaml`; build `2802f00c93eb51a80f23b9380238c309545d6329`. | Installed `runnerd` SHA-256 `22f33d6cd3d429c6d91537031954222d7a648e3a3d46e1e169ddbeaa89c21104`; bridge SHA-256 `61f8cbfa22a0dec7e2916db7cf6077d5d2716a90f4db0b9f9506494383ec3203`; the old PRD bin lacks `runner-session-agent` and `runner-log-capture`. `linux.yaml` and `client-principals.yaml` hashes match the prior evidence. | Python SQLite reports schema 36, then fails to parse the existing `STRICT` schema feature because its linked SQLite is too old. This is a reader compatibility limitation, not a database corruption finding. Fresh counts/IDs will be read with the synchronized Go preflight before changes. Prior read-only evidence recorded 0 retained sessions/slots. |
| `sandbox-host` | `runnerd.service` active; PID 717823, same installed build revision `2802f00c93eb51a80f23b9380238c309545d6329`. | Installed `runnerd` SHA-256 `b0d864b1ff285f6591868aa463e913537955bf1a0a06130f31e8fdd635004491`; bridge SHA-256 `45a77bf5fcb28a89c23ec1f3718089e8793768c5294b9e97e216f3af8c760934`; the old PRD bin lacks `runner-session-agent` and `runner-log-capture`. Config hashes match the prior evidence. Filesystem has 935 MiB available (99% used); preserve all existing work and caches. | Read-only Python SQLite schema 36; 1 unreleased session reservation, 0 unreleased command slots, 0 queued/running commands, 0 resumable jobs. Retained `sess-57e644562a61b549602aa66769daca16:cmd-edc978d8b67c89c3a39157efb9bfd43f`, session lost and command failed exit 1. |
| `factory-sandbox` | `runnerd.service` active; PID 50302, same installed build revision `2802f00c93eb51a80f23b9380238c309545d6329`. | Installed `runnerd` SHA-256 `fafb7d9ba5d343d425fa1752c21a4859a3f42c344d5eca5a43ee5f3dfc0ee374`; bridge SHA-256 `3a13bff38320ab0098cecea38e349e4868c9ab9702a75f2f3ff88cbaad7ce42c`; the old PRD bin lacks `runner-session-agent` and `runner-log-capture`. Config hashes match the prior evidence. | Read-only Python SQLite schema 36; 0 unreleased session reservations, 0 unreleased command slots, 0 queued/running commands, 0 resumable jobs. |

The Mac `/bin/ps` process listing was denied in the restricted shell; launchd
reported the exact unsuffixed service labels and PIDs. Before stopping, fresh
process ownership was rechecked with the authorized shell. The host Python
SQLite limitation was resolved by the synchronized `runner-lane prd-preflight`
tool. The owner stated submissions are stopped; no mailbox counts are treated
as proof of publisher state.

## Maintenance progress and recovery diagnosis

- Private consistent backups were created before service stops. Mac backup:
  `/Users/tomasz.walczuk/Library/Application Support/RemoteSessionRunner/backups/prd-upgrade-20261018T183038.970835000Z`, schema 37, online snapshot SHA-256
  `6ed1bbfc4d22c21a71e30d2c9e43e46d1a6166763acdaedac1bffdb225fc30c7`. Linux,
  sandbox, and factory backups are under each host's canonical PRD `backups`
  directory at `prd-upgrade-20261018T183257.051421888Z`,
  `prd-upgrade-20261018T183311.080531180Z`, and
  `prd-upgrade-20261018T183332.561599805Z`; their schema-36 online snapshot
  hashes are `9c77882c5a6cdc1748054991f1b4ad83b23c84b28c25e365ee8fcfa292f5a70f`,
  `7c012682b5f03be99f9e617fe0ab6ad0333f561d6fa2f248f370b67bb5725c62`, and
  `40e6fdfc49e9bf23c73d379dd95ba214baa45885d9c19da1c254124d8b19a5f2`.
- Disposable-copy migration validation passed for the fresh Mac schema-37
  backup: schema 39, `quick_check=ok`, and all 33 common non-ledger table counts
  preserved. The previously recorded schema-36 worker backups also migrated
  to schema 39 with `quick_check=ok` and preserved all 33 common non-ledger
  table counts.
- `runner-lane prd-stop` stopped only the four unsuffixed services. Post-stop
  inventories were Mac 3 reservations/2 slots, linux-host zero, sandbox-host 1
  reservation/0 slots, and factory-sandbox zero. Dev/QAS service processes
  remain separate and active.
- The installed bundle manifests and component hashes match the accepted
  f117 candidate recorded above. The services remain stopped after installation.
- The first installed Mac `runner-locald` recovery invocation refused mixed
  `--lost-pair` plus `--teardown-pair` options with exit 2 before opening the
  database. A synchronized `6cfd9e2` recovery helper was then built outside the
  checkout (SHA-256
  `6fdf4a5fc3c6dc6a2db714fb739101a4e33d1fe6a76b377755d321d64d128fd6`) and
  recovered exactly two lost pairs and one completed exit-2 teardown pair.
  Mac is schema 39 with all work/reservation counts zero; command results and
  events remain terminal and unchanged.
- linux-host and factory-sandbox migrated from schema 36 to 39 through guarded
  `prd-migrate`; both remain stopped with zero inventory. sandbox-host's
  synchronized `6cfd9e2` helper applied schema migration before correctly
  refusing capacity release: the stored session is a remote target controlled
  by `queued_mac`, while the completed-teardown predicate allowed only local
  `local_user` sessions. Its reservation remains retained; no result, event,
  slot, or capacity record was changed by that refusal.
- A regression reproduces that pre-fix refusal for remote queued-Mac jobs; the
  correction allows remote worker sessions owned by `queued_mac` or
  `direct_mtls` only. The focused `store` and `execution` tests pass, including
  the no-replay, unchanged terminal-result/events/slot, and cleanup-proof
  checks. `make check build` passed on Mac in the authorized shell; the first
  restricted-shell attempt failed only because `/bin/ps` was unavailable to an
  existing runner-locald ownership test.
- The source correction is not yet committed or delivered. The full Ubuntu
  source gates, exact sandbox recovery retry, final work/historical-record
  checks, and service postflight are pending. Keep every PRD service stopped and
  external publishers paused meanwhile.

## Delivery and operation status

The source commit, primary Gitea SHA, exact GitHub mirror SHA, and three Ubuntu
source gate results will be appended after delivery. The source correction is
still pending delivery; the completed backup, stop, install, Mac recovery, and
three migrations are recorded above. PRD service start and final readiness are
not yet complete. Keep publishers paused.
