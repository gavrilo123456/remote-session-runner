# PRD in-place upgrade — implementation and fresh baseline

**Status (2026-10-18, pre-activation):** The authorized PRD maintenance is
ready for postflight activation. The exact previously Dev/QAS-accepted
`f117853a9f712e734eab787e648383f5468e719c` bundles are installed on Mac and all
three Ubuntu workers. Fresh native-host preflights show all four unsuffixed
services stopped, schema 39, unchanged PRD configs, exact accepted component
hashes, and zero active work, slots, reservations, resumable jobs, or pending
recovery finalizations. The four retained identities were handled only by
guarded recovery; their historical failed/lost results and terminal events
remain intact. Source correction commit `6b59e8c546d518ed0431430059f8718eb5cc171c`
passed the Mac and all three Ubuntu `make check build` gates and is on primary
Gitea, the exact GitHub mirror, and all three clean Ubuntu `dev` checkouts. No
PRD service has been restarted yet. No mailbox request or ACK was published,
and Dev/QAS services were not changed.

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
  `prd-migrate`; both remain stopped with zero inventory. The initial
  sandbox-host recovery attempt with synchronized `6cfd9e2` applied the schema
  migration before refusing capacity release: its remote `queued_mac` session
  was excluded by the then-current local-only teardown predicate. That refusal
  changed no result, event, slot, or capacity record. The follow-up source fix,
  delivery, and successful exact-pair recovery are recorded below.
- A regression reproduces that pre-fix refusal for remote queued-Mac jobs; the
  correction allows remote worker sessions owned by `queued_mac` or
  `direct_mtls` only. The focused `store` and `execution` tests pass, including
  the no-replay, unchanged terminal-result/events/slot, and cleanup-proof
  checks. `make check build` passed on Mac in the authorized shell; the first
  restricted-shell attempt failed only because `/bin/ps` was unavailable to an
  existing runner-locald ownership test.
- The remote queued-Mac eligibility correction was committed and delivered as
  `6b59e8c546d518ed0431430059f8718eb5cc171c`; the primary Gitea `origin/dev`,
  automatic GitHub `dev` mirror, and every Ubuntu checkout resolve to that exact
  SHA. Each Ubuntu checkout is clean on `dev`.
- Full `make check build` passed at that SHA on linux-host and sandbox-host.
  factory-sandbox's first run failed only because its long task temp path made
  a Unix socket path invalid. Re-running with a short task-owned temp directory
  passed all tests, vet, and build with exit 0. A temporary factory controller
  build first exposed missing `gcc`; rebuilding it with CGO disabled and output
  under `/tmp` succeeded without cleaning shared caches. The Mac full gate had
  already passed in the authorized shell.
- The synchronized recovery helper was built on sandbox-host from the clean
  delivered checkout at `6b59e8c` (SHA-256
  `237a63b49d5f28fe6ed132e7cd23576a0d498debeb5598e4af516c3411b76735`). Its
  guarded `recover-stalled --apply --teardown-pair` operation recovered exactly
  `sess-57e644562a61b549602aa66769daca16:cmd-edc978d8b67c89c3a39157efb9bfd43f`.
  A read-only comparison against the immutable schema-36 backup confirms the
  session remains `lost`, the command remains `failed` with exit 1 and complete
  non-truncated output, all four original events and the `command_failed`
  terminal event remain unchanged, and the command slot remains stopped and
  released. The live schema-39 reservation is cleanup-confirmed and released;
  pending finalization count is zero.

## Final pre-activation gate

All checks below were repeated after the sandbox recovery and before restarting
any PRD service. Native `runner-lane prd-preflight` reports are the source for
the service, schema, config, component-hash, and work facts.

| Host | PRD service | Schema | Config SHA-256 | Candidate component SHA-256 check | Work inventory |
|---|---|---:|---|---|---|
| Mac | stopped | 39 | `65840e66837dedfa17f5c32568d191a0bbfd6098bcc108d7ba4066f44fb95711` | runner `0eef9bf730e52391b0e444bc157d8142fa4e4ce331e7228cb4696be5cabde537`; runner-local `8fd056b200c5892f64c3d340cbf3b38d6443643b6920dc0959cb1425bddd8147`; runner-locald `c8f29a3dd730f64cbcd17e334837edefedb935e4ca4665eabee0a547727560b6`; log-capture `b54a8d229051c8c335b1d6e8f9b6676735d9492c6dd9d2de43dc9d18d7ab3c12` | all zero |
| linux-host | stopped | 39 | `c85442c4ed3ad5bf1062da82bfda4ba6eff1b7677180df9ca518fc4509faaf4f` | runnerd `ff27547eace5ee60b38df200f5f3835f98a579a3625d712a15fe9fab27206baf`; bridge `2794ddf012fb610faa70e11f7f2cf261af18c61dfaf0b1c446ad9a037b6d3a33`; session-agent `c529e599f9b2b89dc69e958befc24f2c3388c97299b79b21f6cbde1243ef523b`; log-capture `1e7fedb4def5dc8093ac03addb1c67d22aa1fa8307a8cb508bb10e2e33db31fe` | all zero |
| sandbox-host | stopped | 39 | `50f704f1b692048a149c366d0e20be2448ee8236a8cdaefcee39b2a6449e5d9a` | runnerd `010651dbc5e3318f6940dab3cec6121ad5b315f8696adfa2b9a6bece7d566a84`; bridge `49c6d88b5eab03c2f108317b9435e162e5fa6146df900da6997ef721dab257ba`; session-agent `0105cfbbe5c92cc9afa60a6eff74166460c30732b3166e48b648fc2c498eb82d`; log-capture `27a161861f44020330ff6405fc4e6d42c9a417369d77ad20c9224c76e8853488` | all zero |
| factory-sandbox | stopped | 39 | `271994e4a31d04625335bb8d890c5df8ba63585a07c8da7749d4518e926ba699` | same Linux components as linux-host: runnerd `ff27547eace5ee60b38df200f5f3835f98a579a3625d712a15fe9fab27206baf`; bridge `2794ddf012fb610faa70e11f7f2cf261af18c61dfaf0b1c446ad9a037b6d3a33`; session-agent `c529e599f9b2b89dc69e958befc24f2c3388c97299b79b21f6cbde1243ef523b`; log-capture `1e7fedb4def5dc8093ac03addb1c67d22aa1fa8307a8cb508bb10e2e33db31fe` | all zero |

Accepted immutable per-host bundle and manifest hashes remain those in the
Dev/QAS candidate evidence: Mac bundle
`fac006366aeb300eb37539f19fe2b120f60c419969a29c4a2eea700028895e5f` / manifest
`a66a167243dc640c5b4b05ae0a2e6a1841a6d6d306e266a027b60f6af7d2f78a`; linux-host
`8776ae90f4183a2636557cbaff0b19a497624b5383cff2cee3056d50ecd68064` / manifest
`2a968de5f9d6b3a09b3d74508bac3a98a4319ddebd8c817c479e344dc37456bb`;
sandbox-host `d7e757c2219f55d52893525c4b4bf54df664337e6b98094fb9e0f8a7a0d66e46` /
manifest `41b37431164edc4682c41e1316360ed5dbf58379edc3df6b016ad82dd79c496e`;
factory-sandbox `3df6b1afa049716664120b08bdf409182904c8d312da71acf8eb4eb5a204ca5e` /
manifest `dce09d771ea934d97e6afadc41f5c8353e94d96f9fdb3c3a8e9724137ec7b469`.
Per-host bundle acceptance and mailbox-specific acceptance status are recorded
in [the candidate evidence](PRD-diagnostic-upgrade-2026-10-18.md); mailbox
acceptance remains **NOT RUN** by instruction.

The immutable SQLite online-backup digests were rechecked against their
manifests immediately before activation: Mac schema 37
`6ed1bbfc4d22c21a71e30d2c9e43e46d1a6166763acdaedac1bffdb225fc30c7`, linux-host
schema 36 `9c77882c5a6cdc1748054991f1b4ad83b23c84b28c25e365ee8fcfa292f5a70f`,
sandbox-host schema 36
`7c012682b5f03be99f9e617fe0ab6ad0333f561d6fa2f248f370b67bb5725c62`, and
factory-sandbox schema 36
`40e6fdfc49e9bf23c73d379dd95ba214baa45885d9c19da1c254124d8b19a5f2`. Earlier
private-copy migrations of each schema to 39 passed `quick_check` and preserved
all common non-ledger tables. A repeat `quick_check` on the linux-host backup
was unavailable because its system SQLite rejects the existing `STRICT`
schema; factory-sandbox has no `sqlite3` CLI. The backup digests still match,
and this is a reader-tool limitation, not a database integrity finding.

All three Ubuntu source checkouts are clean on `dev` at the exact primary and
mirror commit `6b59e8c546d518ed0431430059f8718eb5cc171c`. PRD binaries remain the
exact f117 Dev/QAS-accepted artifacts above. The newly delivered remote-session
recovery predicate is used only by the synchronized one-off recovery helper;
it did not change the accepted service bundles. The supported rollback
procedure remains `deploy/PRD_UPGRADE.md`: stop the selected unsuffixed service,
then use `runner-lane prd-rollback` with that host's private backup. Rollback
refuses to replace any subsequently accepted work and leaves the service
stopped. No newer database is restored over accepted work.

No service is active yet. Fresh zero-work, stopped-service, exact-artifact,
config, backup, source-gate, and mirror/pull checks passed before activation.
After this evidence is delivered under AGENTS.md, start only the four
unsuffixed services sequentially and complete the direct-interface postflight.
Keep publishers paused until that postflight passes. No Runner request or ACK
was published during this transaction.
