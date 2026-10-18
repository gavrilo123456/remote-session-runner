# Controlled PRD diagnostic upgrade — 2026-10-18

## Status

**Dev/QAS candidate validation: PASS. PRD activation: BLOCKED and NOT RUN.** The old queue-claim failure cause is not proved. No PRD binary, database, configuration, service, route, mailbox, or retained-work record was changed. No Runner mailbox request or ACK was published; mailbox-specific acceptance is **NOT RUN**.

The owner reported that project submissions were stopped. This turn did not independently establish pause/fence confirmation from every affected publisher. The production service and retained-work gates below also remain unmet, so no production restart was attempted.

## Scope and source revision

- Mac source revision: `f117853a9f712e734eab787e648383f5468e719c` (`dev`). It was clean and synchronized to primary Gitea, GitHub mirror, and the three Ubuntu checkouts before this evidence-only change.
- The candidate contains the current queue-claim diagnostic code. `runner-locald` logs bounded operation/stage, component, build revision, UTC timestamp, elapsed time, error category and reason; SQLite primary/extended codes and typed OS codes are included only when present. Scripts, credentials, SQL parameters, and unrestricted error text are not logged.
- The installed Mac build revision `77910358a149d6e8fcdf4694498ebdf6c1f07f58` and Ubuntu PRD revision `2802f00c93eb51a80f23b9380238c309545d6329` are present in local Git history. Their `src/internal/queueworker/worker.go` claim-failure path logs `lifecycle_phase=command_claim` and a generic reason (including `operation_failed`), without transaction stage or SQLite codes. This confirms the diagnostic gap in the installed code; it does not identify the failing operation or cause.
- The diagnostic candidate was not installed into PRD. No new production error stage or SQLite code was captured; the original root cause remains unproven.
- Source gates `make check build` passed on the Mac and on `linux-host`, `sandbox-host`, and `factory-sandbox` at revision `f117853a9f712e734eab787e648383f5468e719c`. Each host checkout was clean on `dev` at that revision before candidate validation.

## Per-host candidate identity

Each host received the exact same bundle in Dev and QAS. Read-only inspection after validation confirmed both lane `current` links point to that host's bundle. Hashes below identify installed manifests and executable components.

| Host | Bundle SHA-256 (Dev = QAS) | Manifest SHA-256 | Component SHA-256 values |
|---|---|---|---|
| `mac-control-plane` | `fac006366aeb300eb37539f19fe2b120f60c419969a29c4a2eea700028895e5f` | `a66a167243dc640c5b4b05ae0a2e6a1841a6d6d306e266a027b60f6af7d2f78a` | `runner` `0eef9bf730e52391b0e444bc157d8142fa4e4ce331e7228cb4696be5cabde537`; `runner-local` `8fd056b200c5892f64c3d340cbf3b38d6443643b6920dc0959cb1425bddd8147`; `runner-locald` `c8f29a3dd730f64cbcd17e334837edefedb935e4ca4665eabee0a547727560b6`; `runner-log-capture` `b54a8d229051c8c335b1d6e8f9b6676735d9492c6dd9d2de43dc9d18d7ab3c12` |
| `linux-host` | `8776ae90f4183a2636557cbaff0b19a497624b5383cff2cee3056d50ecd68064` | `2a968de5f9d6b3a09b3d74508bac3a98a4319ddebd8c817c479e344dc37456bb` | `runnerd` `ff27547eace5ee60b38df200f5f3835f98a579a3625d712a15fe9fab27206baf`; `runner-ssh-bridge` `2794ddf012fb610faa70e11f7f2cf261af18c61dfaf0b1c446ad9a037b6d3a33`; `runner-session-agent` `c529e599f9b2b89dc69e958befc24f2c3388c97299b79b21f6cbde1243ef523b`; `runner-log-capture` `1e7fedb4def5dc8093ac03addb1c67d22aa1fa8307a8cb508bb10e2e33db31fe` |
| `sandbox-host` | `d7e757c2219f55d52893525c4b4bf54df664337e6b98094fb9e0f8a7a0d66e46` | `41b37431164edc4682c41e1316360ed5dbf58379edc3df6b016ad82dd79c496e` | `runnerd` `010651dbc5e3318f6940dab3cec6121ad5b315f8696adfa2b9a6bece7d566a84`; `runner-ssh-bridge` `49c6d88b5eab03c2f108317b9435e162e5fa6146df900da6997ef721dab257ba`; `runner-session-agent` `0105cfbbe5c92cc9afa60a6eff74166460c30732b3166e48b648fc2c498eb82d`; `runner-log-capture` `27a161861f44020330ff6405fc4e6d42c9a417369d77ad20c9224c76e8853488` |
| `factory-sandbox` | `3df6b1afa049716664120b08bdf409182904c8d312da71acf8eb4eb5a204ca5e` | `dce09d771ea934d97e6afadc41f5c8353e94d96f9fdb3c3a8e9724137ec7b469` | Same component hashes as `linux-host` (amd64 build). |

## Candidate validation

All runs used the per-lane direct Runner CLI or the Mac lane-local API. No mailbox request or ACK path was used.

The source gate command was `make check build` on each synchronized checkout. Candidate functional checks used the form `runner --endpoint <selected-direct-profile|local> --config <lane-config> --wait-timeout 60s run --environment <lane-environment> --target <local|remote> --profile <selected-profile> -- "printf '<unique marker>\\n'"`, followed by a full `events <command_id>` read. Direct readiness probes used TLS client credentials from the selected lane and `openssl s_client` with CA validation and `-verify_ip`; no verification was disabled. Read-only live inventory used the task-local inventory helper with `--inventory-read-only <db-path>`, which opens `mode=ro` and sets `PRAGMA query_only=ON`.

### Dev

At `f117853a9f712e734eab787e648383f5468e719c`, Mac local, direct mTLS, and queued control-plane commands all completed with exit 0, complete output, complete terminal event history, and closed teardown:

| Route | Command IDs |
|---|---|
| Mac local Dev | `cmd-9b85ac1a2e440f09b35d2fdb0d876911` |
| Direct mTLS: Linux / Sandbox / Factory | `cmd-37dd3a64b1119c31b3907280d3893140`; `cmd-9bd4fbf3405cf695e14db98a590c6c75`; `cmd-28305888436fb0ab783c15beb0366fdb` |
| Queued through Mac Dev: Linux / Sandbox / Factory | `cmd-b45e000f8b8d844994634cfe981c091f`; `cmd-fd2ab24cc58232bf5acb2c694ae61be8`; `cmd-5b183b1d7e3ffa56a6f546266cc77bb3` |
| Persistent Linux Dev session | `sess-815b9ac0aeff5878aae966a5909efc73`, `cmd-61e7862d48577e7d2bce980c4d180dda`; command succeeded, then graceful close completed. |

### QAS

At the same per-host bundle hashes as Dev, Mac QAS local, all direct mTLS routes, and all queued control-plane routes completed with exit 0, complete output, complete terminal event history, and closed teardown:

| Route | Command IDs |
|---|---|
| Mac local QAS | `cmd-f9cdcd81affeb3df43c83884d335f5a9` |
| Direct mTLS: Linux / Sandbox / Factory | `cmd-d6a927db8343dd221db15e7d9c235f33`; `cmd-cc480195797532e6c994f9e53df53a8c`; `cmd-cf282b3b09c4d6d0adcc5b825e6cf7cf` |
| Queued through Mac QAS: Linux / Sandbox / Factory | `cmd-91b4e0561f91ebe811cc36749b4c97da`; `cmd-f76a857895a6410f267c1690899ed591`; `cmd-7d21d8d6b387df696ce44bef57fa673c` |
| Earlier QAS persistent Linux session check | `sess-24a6d54825a10298d7e7ccbde17fbdd5`, `cmd-434d306d4b96f2aa407c2b249fdd97ad`; successful exit 0, complete output, `command_succeeded`, slot released, session closed. |

Fresh mTLS `/health/ready` calls at `2026-10-18T16:03:22Z` returned HTTP 200 and `readiness=ready`, build revision `f117853a9f712e734eab787e648383f5468e719c`, and zero active slots, queued work, storage errors, and cleanup failures on each of the six Ubuntu Dev/QAS services. Mac Dev and QAS local-api/locald sockets also reported ready and zero active slots, queued work, storage errors, cleanup failures, and mailbox backlog at `2026-10-18T15:55:32Z`. Post-test read-only inventories on all four workers confirmed zero unreleased session reservations and command slots in both Dev and QAS.

Mailbox-specific acceptance is **NOT RUN** by instruction. No mailbox contents or held ACKs were modified by this work.

## Production baseline and retained work

Fresh Mac PRD Unix-socket readiness at `2026-10-18T15:55:32Z` returned `ready`, build `77910358a149d6e8fcdf4694498ebdf6c1f07f58`, zero queued commands/intents and zero mailbox backlog, but retained capacity remained:

- `runner-local`: 3 active session slots, 2 active command slots, `storage_errors_total=4`, `cleanup_failures_total=0`.
- `runner-locald`: 3 active session slots, 2 active command slots, `storage_errors_total=719`, `cleanup_failures_total=1`.
- At the previous `15:31Z` observation locald had `storage_errors_total=718`; the observed delta is +1. It is not attributed to a nearby warning, health probe, or operation. The diagnostic candidate has not run in PRD.
- The unsuffixed LaunchAgent arguments still select `/Users/tomasz.walczuk/Library/Application Support/RemoteSessionRunner/bin/runner-local{,d}` and `config/mac.yaml`; that config SHA-256 is `65840e66837dedfa17f5c32568d191a0bbfd6098bcc108d7ba4066f44fb95711`.
- `launchctl print` reported the unsuffixed agents running: `com.remote-session-runner.local` PID 48708 and `com.remote-session-runner.locald` PID 48627. Installed binary SHA-256 values are `runner-local` `b632233613858ad2541dfd7d18915eb8924df3abe082926b6ece0a1df6b66455` and `runner-locald` `e7f15eea593c402fb2b1e7605aafc9d558e3289222d54abade56b8bacb60ec13`.
- The three Ubuntu PRD `runnerd.service` units were active. Their service processes use the existing unsuffixed `/home/ubuntu/.local/share/remote-session-runner/bin/runnerd` and `config/linux.yaml`, all report build `2802f00c93eb51a80f23b9380238c309545d6329`; installed `runnerd` SHA-256 values are Linux `22f33d6cd3d429c6d91537031954222d7a648e3a3d46e1e169ddbeaa89c21104`, Sandbox `b0d864b1ff285f6591868aa463e913537955bf1a0a06130f31e8fdd635004491`, and Factory `fafb7d9ba5d343d425fa1752c21a4859a3f42c344d5eca5a43ee5f3dfc0ee374`.
- PRD Linux configuration fingerprints (the actual files and route/mailbox settings were not edited) are: `linux-host` `linux.yaml` `c85442c4ed3ad5bf1062da82bfda4ba6eff1b7677180df9ca518fc4509faaf4f`, `client-principals.yaml` `5e76f85a6f092cd501fc90d1f00c861e838465197b3677f0a36b8878e7859dab`; `sandbox-host` `linux.yaml` `50f704f1b692048a149c366d0e20be2448ee8236a8cdaefcee39b2a6449e5d9a`, `client-principals.yaml` `819452d2c82654ee2bd2dbac9f6f6c26c44d7e8177ac483bb3cc64c598127e65`; `factory-sandbox` `linux.yaml` `271994e4a31d04625335bb8d890c5df8ba63585a07c8da7749d4518e926ba699`, `client-principals.yaml` `41ff3ecbc6dd5bc0ea2eb3afc1d0f20f4fd0fd6d6aef801afe2bb01582e999ad`.

Read-only live inventories at approximately `2026-10-18T16:00Z` used SQLite `mode=ro` and `PRAGMA query_only=ON`; they did not use `store.Open` on any live database:

| PRD host | Schema | Unreleased sessions | Unreleased command slots | Retained identities |
|---|---:|---:|---:|---|
| Mac control plane | 37 | 3 | 2 | Detailed below. |
| `linux-host` | 36 | 0 | 0 | None. |
| `sandbox-host` | 36 | 1 | 0 | Detailed below. |
| `factory-sandbox` | 36 | 0 | 0 | None. |

Mac retained records:

1. `sess-d76592166342428ba1b7b703fc87bb35` / `cmd-6817a947ceec8c3e54fbcdc2b8f2fb30`: session state `lost`; command `failed`, exit 2, final event 25, complete and untruncated output, command stop and command-slot release confirmed `2026-10-16T12:37:05.386605Z`; the session reservation remains unreleased because runtime cleanup is unconfirmed.
2. `sess-e4e08eb9c942672e900bd0b411f54f1e` / `cmd-4c76c371ee67375b6af606dbc0ce7668`: command state `lost`, no exit code, output incomplete (`capture_boundary_unconfirmed`), no stop/release proof, session reservation unreleased.
3. `sess-e7e8f0689316b308a9a8e5d15aaf34ae` / `cmd-36dc71219a33e1901e23272ae5a3127f`: command state `lost`, no exit code, output incomplete, no stop/release proof, session reservation unreleased.

Sandbox retained record:

- `sess-57e644562a61b549602aa66769daca16` / `cmd-edc978d8b67c89c3a39157efb9bfd43f`: session state `lost`; command failed exit 1 with complete output and final event 4; command stop/release was confirmed `2026-10-17T17:00:48.92719325Z`, but the session reservation remains unreleased because runtime cleanup is unconfirmed.

No retained record was replayed, deleted, edited, manually released, or migrated in place. The complete result/event history and held ACK state remain preserved.

## Private consistent backups and migration tests

The source connections were read-only (`mode=ro`, query-only); SQLite online backup produced private consistent snapshots outside Git. Migration was run only on separate disposable copies with current source. No worker ran against a backup.

| Host | Preserved online backup | SHA-256 | Source schema → tested copy | Migration result |
|---|---|---|---:|---|
| Mac | `/private/tmp/rsr-prd-upgrade-20261018/mac-control-plane/local.db.online` | `16029a3c2ee0cedbf30ea6b8f8c4cd885f2e44fc6dd4f193768388e01e9444cc` | 37 → 39 | `quick_check=ok`; 34 pre-existing table counts preserved; 0 mismatches. |
| `linux-host` | `/home/ubuntu/.local/share/remote-session-runner-prd-diagnostics/20261018/linux-host/remote.db.online` | `9c77882c5a6cdc1748054991f1b4ad83b23c84b28c25e365ee8fcfa292f5a70f` | 36 → 39 | `quick_check=ok`; 33 pre-existing table counts preserved; 0 mismatches. |
| `sandbox-host` | `/home/ubuntu/.local/share/remote-session-runner-prd-diagnostics/20261018/sandbox-host/remote.db.online` | `7c012682b5f03be99f9e617fe0ab6ad0333f561d6fa2f248f370b67bb5725c62` | 36 → 39 | `quick_check=ok`; 33 pre-existing table counts preserved; 0 mismatches. |
| `factory-sandbox` | `/home/ubuntu/.local/share/remote-session-runner-prd-diagnostics/20261018/factory-sandbox/remote.db.online` | `40e6fdfc49e9bf23c73d379dd95ba214baa45885d9c19da1c254124d8b19a5f2` | 36 → 39 | `quick_check=ok`; 33 pre-existing table counts preserved; 0 mismatches. |

The Mac also retains the earlier consistent private backup `/private/tmp/rsr-prd-upgrade-20261018/mac-control-plane/local.db` (SHA-256 `ec6a74932bbf6cfdc3ef12cd6e4c87a3d1cdcda258c9dddb12bcd8652919973a`). Required `.online` backups and their SQLite sidecars were retained. Disposable migration copies and temporary read-only inspection tools were removed after all processes exited.

## Production installation/rollback gate

The supported `runner-lane` command and implementation currently accept only existing-host `dev` and `qas` lanes. There is no PRD plan, install, start, or rollback path. The candidate was therefore not staged or installed in PRD. The user expressly prohibited bypassing this restriction and manual binary replacement; no such bypass was attempted.

The components a future approved PRD transaction would affect are Mac `com.remote-session-runner.local` and `com.remote-session-runner.locald`, and the unsuffixed Ubuntu `runnerd.service` on `linux-host`, `sandbox-host`, and `factory-sandbox`. No Dev/QAS service or host bridge should be part of that PRD transaction.

The available Dev/QAS lane backup receipts are not a PRD rollback procedure. Although the database migration was validated on private copies, an exact supported PRD artifact installation and rollback path has not been prepared or tested. An older database must not be restored over work accepted after a production activation.

PRD activation remains blocked until both conditions are met:

1. A reviewed, supported PRD-aware installation and rollback workflow exists within the deployment tool, and passes its exact-artifact, service-ownership, schema-compatibility, backup, and postflight guards. This is a technical gap, not a missing credential or administrator password.
2. Each retained identity has a supported, evidence-preserving disposition. The complete failed-command pairs may be considered only through the existing guarded recovery supported for those exact records. The two Mac incomplete/lost commands have no complete output/stop proof; no safe release was established. Do not clear their slots to satisfy a numeric zero gate.

The authorized service restarts, direct SSH access, and owner-reported publisher pause do not waive these gates. No PRD service restart, binary install, config change, database migration, route change, or rollback was performed. PRD is **not ready for project activity to resume after an upgrade**; its current Mac readiness response is not evidence that the diagnostic fix is deployed or that the old error is resolved.

## Delivery record

This file is an evidence-only change. Candidate source/build revision remains `f117853a9f712e734eab787e648383f5468e719c`; the hashes above remain the frozen Dev/QAS-tested artifact snapshot. Follow the repository delivery chain for this evidence commit: Mac `dev` → primary Gitea → exact GitHub mirror SHA → clean fast-forward on all three Ubuntu Dev checkouts. No PRD action is part of that Git delivery.
