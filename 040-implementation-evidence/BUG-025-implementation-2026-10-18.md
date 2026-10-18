# BUG-025 implementation and deployment evidence

## Scope and outcome

This closeout covers the owner-authorized BUG-025 correction on the existing
Mac, linux-host, sandbox-host and factory-sandbox, in PRD, Dev and QAS. No
Runner mailbox request or ACK was published. Project publishers were left
paused as instructed. Existing retained records and held ACKs were not replayed,
manually changed or released.

The correction is installed and the bounded postflight passed on all twelve
host/lane combinations. Normal multi-project observation remains pending because
publishers are paused. CPU samples also found elevated activity on Mac PRD
runner-local and sandbox-host PRD runnerd; record and follow those measurements
before calling performance resolved under normal load.

| Item | Result |
| --- | --- |
| Installed source revision | `00c1ca6e7694842c35d398583b3f26fea59e3613` |
| Correction and backup gate | `7c205239928f885b1a22e2aee3de1f03d30e52bf` and `0a921404c0a1d4b0e025e83771ad3a67ce253f1e` |
| Source delivery | Mac `dev`, primary Gitea, exact automatic GitHub mirror SHA, and all three clean Ubuntu `dev` checkouts reached `00c1ca6e7694842c35d398583b3f26fea59e3613` |
| Source gates | `make check` passed on Mac and linux-host, sandbox-host and factory-sandbox at the installed source revision |
| Dev then QAS acceptance | PASS; each host's same immutable candidate bytes were accepted in Dev, then QAS, before PRD |
| PRD deployment | PASS; each host's same accepted candidate installed through supported PRD maintenance, schema migrated 39 to 40, service restarted |
| Rollback | Not needed; supported pre-change backups remain in place |
| Mailbox use | Intentionally NOT RUN; no request or ACK publication was performed |

## Correction and source regressions

Pure status, intent, exchange and event reads now use consistent read
transactions without reserving SQLite's writer. Read-side SQLite failures still
increment storage-error accounting; identity, controller-scope, immutable
payload/hash, ambiguity and corruption checks remain in the read path.

Schema 40 adds a durable event-cleanup work queue. Each cleanup turn is bounded
to four command work items and 32 references per item, uses an exchange-ID
keyset cursor, rotates across commands and inboxes, and persists progress across
restarts. Selection uses narrow validated metadata rather than reloading and
hashing historical response bodies. ACK/deadline and terminal-state changes
invalidate progress. Completion removes only eligible event files and cleanup
work rows; immutable exchange/reference records are retained. The local worker
waits at least one second from completion before the next writer-owning cleanup
turn.

The source suite includes regressions for a real status getter succeeding while
another WAL connection holds `BEGIN IMMEDIATE`, SQLite error accounting,
bounded scans and restart continuation, retention and ACK/deadline invalidation,
fairness, local/remote ownership, malformed safety metadata, interrupted unlink
retry, cadence, and guarded schema-40-to-39 rollback.

The first supported Mac Dev install attempt stopped only Dev and exposed that
the generic backup validator expected schema 40 from a schema-39 source. It
refused before bundle activation or migration; the unchanged old Dev service
was restarted and ready. The regression failed before the backup correction,
then passed. The failed temporary backup was removed by the installer. No
production database was changed by that attempt.

## Frozen candidates and installed component identity

All bundle hashes below identify the exact per-host candidate accepted in Dev,
then QAS, then installed in PRD. The supported installer verified the bundle
manifest and component hashes. A read-only final comparison found all installed
components in every lane matched that host's accepted candidate.

| Host | Bundle SHA-256 | Source revision |
| --- | --- | --- |
| Mac | `6154ea67ea3337939982c6d798e0cf43acea9e06e8c2a0facff0f1136ed99310` | `00c1ca6e7694842c35d398583b3f26fea59e3613` |
| linux-host | `f60b3926d5edb7d256589173ada7a1952cf9ffbe8568644bdfc88f404ee13863` | `00c1ca6e7694842c35d398583b3f26fea59e3613` |
| sandbox-host | `2b65f490b88cbb069460a1e4276b64bdc2acc11bd326eded1e697a31160d0459` | `00c1ca6e7694842c35d398583b3f26fea59e3613` |
| factory-sandbox | `b59c7bdc53d50a4be2e5b78c9bb53d560e98b427c03b360ec5b394af59b11687` | `00c1ca6e7694842c35d398583b3f26fea59e3613` |

The Mac Dev and QAS accepted binaries were the same Mac bundle later installed
in PRD. The Ubuntu Dev and QAS accepted each host's bundle before its PRD
promotion. Installed version output from every service reports
`build_revision=00c1ca6e7694842c35d398583b3f26fea59e3613`.

## Backups and production installation

Before each PRD stop, the supported preflight reported an active service,
schema 39, and zero active sessions, command slots/reservations, queued or
running commands, resumable jobs and pending recovery finalizations. The
supported online backup preserved the schema-39 database, configuration and
installed bundle. The service configuration hash was unchanged after each
installation.

| Host | Pre/post config SHA-256 | Consistent schema-39 backup | Database SHA-256 |
| --- | --- | --- | --- |
| Mac | `65840e66837dedfa17f5c32568d191a0bbfd6098bcc108d7ba4066f44fb95711` | `/Users/tomasz.walczuk/Library/Application Support/RemoteSessionRunner/backups/prd-upgrade-20261018T225407.949457000Z` | `e4c859e9e18b21c4ff4b470c0042b92b4d029152d7b47c81f11fe136406fc159` |
| linux-host | `c85442c4ed3ad5bf1062da82bfda4ba6eff1b7677180df9ca518fc4509faaf4f` | `/home/ubuntu/.local/share/remote-session-runner/backups/prd-upgrade-20261018T230046.739382621Z` | `be424411bedb30bb8eeec70fe51b83697e73d20c95f08d808c00d8fd1fe09bf` |
| sandbox-host | `50f704f1b692048a149c366d0e20be2448ee8236a8cdaefcee39b2a6449e5d9a` | `/home/ubuntu/.local/share/remote-session-runner/backups/prd-upgrade-20261018T230352.701954289Z` | `1a4db177040acff55185f091124424cf3fab86c498590612a1be6d557cb1badd` |
| factory-sandbox | `271994e4a31d04625335bb8d890c5df8ba63585a07c8da7749d4518e926ba699` | `/home/ubuntu/.local/share/remote-session-runner/backups/prd-upgrade-20261018T230530.160915873Z` | `ce1b9f41f54ef77c89e75c5324ef0107628475c839b8172723b32efac62770cf` |

The supported Dev/QAS installers also made rollback backups:

| Host | Dev backup | QAS backup |
| --- | --- | --- |
| Mac | `RemoteSessionRunner-dev/backups/.simple-backup-332464423` | `RemoteSessionRunner-qas/backups/.simple-backup-1227096129` |
| linux-host | `remote-session-runner-dev/backups/.simple-backup-1755398785` | `remote-session-runner-qas/backups/.simple-backup-488574651` |
| sandbox-host | `remote-session-runner-dev/backups/.simple-backup-4110603022` | `remote-session-runner-qas/backups/.simple-backup-2410940842` |
| factory-sandbox | `remote-session-runner-dev/backups/.simple-backup-18882389` | `remote-session-runner-qas/backups/.simple-backup-3047534746` |

Paths in this table are relative to each host's standard service root. The
PRD sequence used the supported stop, install, migrate and start operations,
one lane at a time. All twelve databases now report schema 40. Post-migration
work inventories are zero. No rollback was performed. Existing PRD configuration,
credentials, routes, mailbox roots and service names were preserved. No Dev/QAS
service was restarted during the PRD phase.

## Runtime postflight

The Mac has active `runner-local` and `runner-locald` LaunchAgents for each
lane. Each Ubuntu host has active `runnerd.service`,
`runnerd-dev.service` and `runnerd-qas.service`; Ubuntu does not use a
separate `runner-locald` service. All report the installed source revision,
and all lane-specific executable hashes match the frozen per-host candidate.
No unrelated service was restarted.

Direct API checks used Unix sockets on Mac and mTLS HTTPS on Ubuntu. Each row
records 20 successful status GETs after readiness passed.

| Host | Lane | Status reads | p50 / max (ms) | Storage-error delta | Cleanup-failure delta |
| --- | --- | ---: | ---: | ---: | ---: |
| Mac | Dev | 20/20 HTTP 200 | 1.615 / 4.687 | 0 | 0 |
| Mac | QAS | 20/20 HTTP 200 | 2.410 / 11.110 | 0 | 0 |
| Mac | PRD | 20/20 HTTP 200 | 15.332 / 26.757 | 0 | 0 |
| linux-host | Dev | 20/20 HTTP 200 | 253.319 / 551.548 | 0 | 0 |
| linux-host | QAS | 20/20 HTTP 200 | 257.134 / 650.999 | 0 | 0 |
| linux-host | PRD | 20/20 HTTP 200 | 274.154 / 784.684 | 0 | 0 |
| sandbox-host | Dev | 20/20 HTTP 200 | 164.285 / 832.787 | 0 | 0 |
| sandbox-host | QAS | 20/20 HTTP 200 | 145.038 / 188.521 | 0 | 0 |
| sandbox-host | PRD | 20/20 HTTP 200 | 225.384 / 266.510 | 0 | 0 |
| factory-sandbox | Dev | 20/20 HTTP 200 | 233.255 / 826.208 | 0 | 0 |
| factory-sandbox | QAS | 20/20 HTTP 200 | 220.528 / 303.836 | 0 | 0 |
| factory-sandbox | PRD | 20/20 HTTP 200 | 165.968 / 208.917 | 0 | 0 |

All measured post-restart storage-error and cleanup-failure counters remained
zero across these read windows. sandbox-host and factory-sandbox PRD counters
were also zero before restart and remained zero after restart. The old Mac and
linux-host PRD process-local counters were not captured before restart, so their
cross-restart deltas are unavailable; their post-restart 20-read windows were
zero-to-zero. A reset counter is not used as evidence of repair.

Mac PRD's cleanup queue reported 135 completed scans at 2026-10-19 00:57
Europe/Warsaw, 179 shortly afterward, and 265 of 269 work items at
01:13 Europe/Warsaw. The latest two read-only samples 15 seconds apart both
reported 269 work items, 265 scanned references, 265 scan-complete items and
four cleanup mailboxes. Cleanup failures and storage errors remained zero.
Other host/lane databases reported zero cleanup work.

Bounded process CPU samples during paused-project postflight:

| Process | Observed samples | Notes |
| --- | --- | --- |
| Mac PRD `runner-local` | 34–64% in the five samples after its initial 0% sample | Elevated and sustained in this short sample; Mac host idle ranged about 63–75% |
| Mac PRD `runner-locald` | 5–7% after its initial 0% sample | Low activity |
| Mac Dev/QAS `runner-local` | Dev and QAS each 0–1.5% in three one-second samples | LaunchAgent wrappers were distinguished from their child workers |
| linux-host PRD `runnerd` | 0–10.9% in sampled intervals | Dev/QAS 0% in three one-second samples |
| sandbox-host PRD `runnerd` | 36–53% in sampled intervals | Elevated while its measured work and cleanup queues were zero; Dev/QAS 0% |
| factory-sandbox PRD `runnerd` | 0–1% | Dev/QAS 0% |

These short samples are not a normal-load performance test and do not attribute
the elevated Mac or sandbox CPU to BUG-025 cleanup. Keep this as a specific
post-deployment observation. Project publishers remain paused; no claim of
normal multi-project queue progress is made yet. The unsuffixed PRD V1 readiness
and direct routes passed. The optional V3 attestation endpoint still reports
`v3_attestation_unavailable` for the unchanged legacy PRD configuration; no
PRD configuration was changed to add that separate attestation.

## Resolution and remaining observation

The source regressions, full source gates, Dev acceptance, QAS acceptance,
supported PRD migration, artifact identity checks, readiness and bounded direct
status-read checks passed. The implementation is deployed everywhere and
rollback was not needed. Keep BUG-025 open as
`FIXED — DEPLOYED; NORMAL-LOAD OBSERVATION PENDING` until project publishers
are resumed by the owner and multiple-project queue progress, error-counter
deltas, cleanup cost and CPU are observed over the former warning intervals.
The Mac PRD cleanup backlog and elevated Mac/sandbox CPU samples must be
rechecked during that observation. Do not attribute unrelated historical
queue-claim warnings to this correction without evidence.
