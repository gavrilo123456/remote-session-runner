# R100 — exact Mac completed-command teardown recovery, 2026-11-10

**COMPLETE: Runner cleanup verified; one session reservation released. The Mac
PRD Runner is ready for fresh work. Factory eligibility and reservations are
not cleared by this recovery.**

## Exact subject and authority

| Subject | Identity |
| --- | --- |
| Host/account | `mac-control-plane`, `tomasz.walczuk`, UID 502 |
| Actual Runner namespace | PRD unsuffixed; policy `mac-dev`, local profile `mac-workstation` |
| Request | `req-asf-p09-user33-r100-bounded-codex-qualification-20261110-agent-001` |
| Session | `sess-2b19287a7d095dc69abe7555010a5f3d` |
| Command | `cmd-bdb6242216a865ab94c13506f1f36341` |
| Job | `job-807ad3c21220dbbc1c1f7fd9622903ce` |
| Runtime generation | `29b126ec7650b96c1acf1eb52790b1eb` |

The owner authorized supported exact Runner teardown/capacity recovery and
confirmed PRD clients/publishers paused before maintenance. The owner separately
authorized a Linux-only HTTPS mirror exception for this recovery; TLS, exact
primary/mirror SHA, clean `dev` and fast-forward checks remained enforced.
No request replay, replacement qualification, ACK or Factory recovery was
assigned or performed.

## Preserved result and initial inventory

The command is an ordinary **failed exit 2**, complete/untruncated output,
response revision **3**, final/available event **10**. Its session and job
teardown were LOST with `runtime_cleanup_unconfirmed`. Its command slot was
already released; the sole retained Runner reservation belonged to the exact
session above. Complete supported inventories identified no unrelated active,
queued or resumable execution or pending recovery finalizations on the Mac's
three lanes. Historical accepted/uncertain delivery metadata was kept distinct
from execution state and preserved.

The original mailbox response and ten-event stream remain byte-for-byte:

- Response SHA-256: `0a6513d361460d73356e308dcbf3ff5d83e43558c61446f671addcd9ae2c90df`
- Events SHA-256: `e3e0feb25c5e13339cf756efaece57f2d0051a6d2cd12497b8ac3728927347dd`
- Factory terminal archive SHA-256: `62cb061dd6339694cdc62a784fc18e99f78055fd125e5cd317d817ccb9490589`

The original ACK paths remained absent; supported lifecycle status after
maintenance is `terminal_unacknowledged`. Recovery did not rewrite the
historical failed/LOST job/session as CLOSED or successful.

## Ownership and cleanup evidence

The retained owner-only marker bound Bash PID/PGID **14445**, UID502,
start identity **1794346826034072**, to this session/generation and the owned
Runner workspace `session-2901634809`. Its pre-recovery SHA-256 was
`bb336da93c5c1f6bc26d481b14edb066aeb9b2deac04f3ae72d2da418cd273b3`.

Fresh kernel libproc observations at 22:44:46 UTC found the recorded root PID
absent with ESRCH at both observation ends, and zero members of PGID14445.
The sealed executor independently re-proved its exact ownership/cleanup guards
under its lifecycle lock before capacity release. Operator-issued process
signals were unnecessary. Factory's previously registered PID18928 was a
correlation only, not the authority for Runner recovery or a complete
Factory descendant inventory.

The original close was authorized at 21:46:25.361126 UTC, followed by LOST at
21:46:25.873564 UTC. Durable status records failure to confirm runtime cleanup;
the retained evidence does not establish which process or inspection caused
that earlier refusal. Later root/group absence and successful recovery do not
prove a preventive runtime fix or Factory whole-run coverage.

## Narrow catalog control correction and qualification

The legacy installer refuses catalog-selected PRD and would rebuild/install
service binaries. The existing catalog wrapper supported only lost-command
pairs. Source commit **`fe807c65a3fc3fbfa5eb041dfbf180c6f89b19ef`** added the
Mac-only `catalog-prd-recover --teardown-pair` selection, bound to the exact
already selected release/digest/bundle. It preserves the stopped-service,
fresh-backup, schema, complete idle/exact-set and executor ownership guards.
It does not edit service code, rebuild/promote a release or invoke the
executor outside the wrapper. Mixed lost/completed sets, wrong hosts,
selectors, backups and identities are refused.

Meaningful fixture coverage includes failed exit2/event10/unACKed result
preservation and selection/refusal cases. Focused Mac catalog, CLI,
runner-locald, execution and store completed-teardown tests passed; the full
runner-lane package tests and changed-package vet/native build passed.
Changed-package tests, native vet/build and help/version smoke passed with
Go1.27.1 on all three Ubuntu workers (Linux/Factory amd64, Sandbox arm64).
All four checkouts were clean at `fe807c65…` after primary Gitea push, exact
GitHub mirror verification and clean fast-forward pulls. Linux used only the
owner-approved HTTPS mirror exception. The final source was tested; this is
not a new full-repository qualification claim.

The new wrapper conservatively refuses pending-finalization/released-set
retries; it does not claim generalized interruption recovery. Preserve any
such partial evidence for a supported disposition rather than bypassing it.

Qualified Mac controller SHA-256:
`d5f201d91fa348c9ffebbf11d39c142a72b7b2e47c44875630ae8e3144efcdfb`.

## Exact maintenance and postflight

The supported sequence was `catalog-prd-backup` → `prd-stop` →
`catalog-prd-recover --teardown-pair` → stopped zero-work postflight →
`prd-start`, all scoped to `mac-control-plane`. No direct executor command,
manual launchctl/SQL, selector change, config edit or Factory operation was
used.

| Step | Completed UTC, 2026-11-10 | Result |
| --- | --- | --- |
| Fresh consistent backup | 22:54:58.947634 | PASS |
| Mac PRD service stop | 22:55:00.303633 | PASS |
| Exact completed-teardown recovery | 22:55:01.963048 | PASS; one reservation released |
| Stopped native zero-work postflight | 22:55:02.279233 | PASS |
| Same selected services restarted | 22:56:11.828565 | PASS |
| Complete live inventory | 22:56:12.617234 | PASS; execution/capacity/finalizations zero |
| Read-only status/health/artifact postflight | 22:56:49.329441 | PASS |

After the successful recovery, a private postflight reader incorrectly expected
a flat aggregate response and halted on `active_sessions`. The actual supported
postflight already showed `work_inventory_empty=true` and all zero nested
counts. The error was preserved; a postflight-only continuation validated that
wrapper and started the services. **Recovery was not repeated.**

Backup: `~/Library/Application Support/RemoteSessionRunner/backups/prd-catalog-20261110T225456.405431000Z`.
Backup manifest SHA-256:
`788e99f1f426dc95b47d4562b98ee0429717ca938ba31bdaed939003ceca80f8`.

The exact recovery report records `recovery_mode=completed_teardown_pairs`,
`recovered_teardown_pairs=1`, `recovered_lost_pairs=0`, verified runtime
ownership, unchanged selector/schema and an empty post-recovery inventory.
The session reservation was released only after cleanup proof. Its ownership
marker and owned Runner workspace were finalized; no recovery-finalization
records remain. The command slot was already released before maintenance.

The release remains **`rr-20261108-bug029-1263205`**, source
`126320552acf0a2609d7d75c09016b867b1dbd13`, schema40:

- Release digest: `1b54ce565da1c94d64604f8ce74462dd49974f67d60e0fcc8f772339e82f0603`
- Mac bundle: `25b8b596b00dc96933ee947a9180e3c024d9cfd469047d7eb0811fffb635b004`
- Sealed runner-locald: `6350fd9029e8418e9c55372601d02a72ecd72bbf42e25ae2c7cb24801be518e7`
- Configuration: `2b35d897a7df65b13e77fbcdc0749da6f7df8d654d16a535a33faac5afbf2eb2`

All six Mac Dev/QAS/PRD ingress/executor readiness endpoints returned HTTP200
and `ready` on that selected source. Dev/QAS selector/config/DB identities were
unchanged. No Ubuntu runtime service was operated. The complete PRD inventory
contained zero relevant sessions/commands/jobs, reservations, command slots,
retained pairs or pending finalizations. Delivery-intent identities/states
were unchanged across maintenance; none was replayed or reconciled.

## Resumption boundary and raw evidence

**Runner-only operator disposition: safe to resume fresh work on the affected
Mac route.** This conclusion combines the exact recovery receipt, process
proof and live postflight. The native detailed inventory independently retains
its conservative `safe_to_resume=not_established`; its field was not changed
or represented as a Factory clearance.

Factory's qualification is still NOT_QUALIFIED; its reservations, fences,
holds, finalization and separate eligibility remain untouched. This recovery
does not clear #005, watchdog refresh or unrelated uncertain remote intents.
External publishers were not resumed by the maintainer. The original
exchange remains failed and unACKed; do not replay it.

Private evidence packet: `/private/tmp/rsr-r100-teardown-20261110-lgx4_q68`. It contains the exact recovery receipt,
consistent-backup receipt, before/after inventories, kernel owner observations,
status/lifecycle/health captures, source/native validation and artifact hashes.
`SHA256SUMS.json` binds the private packet. Raw operational inputs remain
outside Git; no credentials are included in this record.
