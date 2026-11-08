# BUG-029 — PRD deployment closeout, 2026-11-08

**The unchanged Dev/QAS-accepted BUG-029 release is installed and accepted on all
four PRD hosts. The operation and PRD source-marker synchronization are complete.**
PRD clients remain owner-paused; the operator did not resume them.

## Authorization and immutable candidate

The owner confirmed all PRD clients/publishers stopped and separately approved
exact Sandbox closeout followed by the guarded PRD deployment. The actual
[execution approval](/private/tmp/rsr-bug029-prd-20261108-phr9whfy/approval-prd-execution.json) binds the release,
four predecessors and backup/recovery package `73f91af4bbc20e91243ebdfd6e0c9a6bb851d15b15c76a03b7a7dbdb4050c1e9`.
Its window is `2026-11-08T10:18:47Z` through `2026-11-08T14:19:47Z`.

| Identity | Value |
| --- | --- |
| Release | `rr-20261108-bug029-1263205` |
| Production correction | `14aadaa4e4c9d6e126f6fab1a71561245a2b0be4` |
| Frozen/tested service source | `126320552acf0a2609d7d75c09016b867b1dbd13` |
| Controller | `22cb7269a5b2ed12f8e30b24c94885687b567cc4` — includes tested operator correction `1ecf910` |
| PRD operation | `b874bf695e2411a0711832324b269fa2` |
| Release SHA-256 | `1b54ce565da1c94d64604f8ce74462dd49974f67d60e0fcc8f772339e82f0603` |
| Provenance SHA-256 | `162ecf9a9b40343c7a132d88014e66f35623fcc3e7d7b30b54471149efd981d2` |

| Host | Selected PRD bundle SHA-256 |
| --- | --- |
| `linux-host` | `46e4894083f22dc0258de773e1594c374ce1f8929f813576167dbf38fff7f29e` |
| `sandbox-host` | `d6f7488a4c5af758040bc82d02eb00c96202b267a6bdf1362f8a819ff804d8be` |
| `factory-sandbox` | `da716d140522405d158bfbb94f58eea4c35a51fa729ada9a3289dbce406a02f5` |
| `mac-control-plane` | `25b8b596b00dc96933ee947a9180e3c024d9cfd469047d7eb0811fffb635b004` |

The same sealed bytes previously passed [Dev](BUG-029-dev-deployment-2026-11-08.md)
and [QAS](BUG-029-qas-deployment-2026-11-08.md). No service rebuild/reseal or repeat
source qualification was performed for PRD. Original source/test failures remain in
[BUG-029 source evidence](BUG-029.md); they are not relabeled by this deployment.

## Separately approved exact Sandbox recovery

Only the original lost pair `sess-7c30ce1c0710edd6d3b1af113b5f0338` / `cmd-7eb4867eeef134497ec4ac742199d9ce`
(job `job-69751f154034674526111008723ef12e`) and commandless session `sess-93f705274a0ba787196e9000da7fba19`
(job `job-cf2e7e3e3ee85d39005b8d17f8e0c295`) were included. The approved close of the latter
produced an actual lost session/job before offline recovery; no missing command
row was treated as proof of success or nonexecution.

Fresh post-close backup and stopped-service/ownership guards preceded mixed
recovery by the selected installed E3 binary, SHA-256 `2c6ca92059e84b8a9040704f833f2e3a995e5d3db3dbef2321c8f9a88eb0344b`.
The [recovery command/result](/private/tmp/rsr-bug029-prd-20261108-phr9whfy/sandbox-approved-recovery.stdout.json)
exited 0 at `2026-11-08T10:15:51.275799+00:00`: one lost pair and one lost session
recovered; no jobs settled as successful. Its file SHA-256 is `90e33a5131109d47ca95e2bcd0e11e53b5bcda400a6750a5ccdb75b155f9e030`.

The [independent history comparison](/private/tmp/rsr-bug029-prd-20261108-phr9whfy/sandbox-recovery-history-proof.stdout.json)
passed at `2026-11-08T10:18:40.948023+00:00`, file SHA-256 `2575727b8c6e2b0f55fdaad8a8b484f52a72c33fd8c56f027466e65cc876b1a9`.
It proved equality to the post-close backup for two sessions, one command, two
jobs, seven lifecycle rows and three events, while confirming both reservations
and the exact command slot released. Lost outcomes and incomplete output were
preserved; the operator did not replay project work, redispatch CI or publish ACKs.

## Guarded PRD operation and fresh backups

The supported toolkit captured new PRD-specific consistent backups, synchronized
the cohort and fences, then switched Linux → Sandbox → Factory → Mac. Actual
recorded operation times are:

| Event | UTC |
| --- | --- |
| Initial CLI start | `2026-11-08T10:20:09.794805+00:00` |
| Common preparation complete | `2026-11-08T10:29:48.336385Z` |
| linux-host selector verified | `2026-11-08T10:34:44.072644Z` |
| sandbox-host selector verified | `2026-11-08T10:37:16.817974Z` |
| factory-sandbox selector verified | `2026-11-08T10:38:21.063233Z` |
| mac-control-plane selector verified | `2026-11-08T10:39:08.289281Z` |
| Initial attempt failed (NOT PASS) | `2026-11-08T10:46:03.621716+00:00` |
| First resume failed (NOT PASS) | `2026-11-08T10:54:57.150514+00:00` |
| Renewal synchronized | `2026-11-08T11:31:52.485629+00:00` |
| Second same-operation resume started | `2026-11-08T11:32:39.806662+00:00` |
| Functional acceptance | `2026-11-08T11:38:36.678079Z` |
| Cohort release recorded | `2026-11-08T11:41:44.363348Z` |
| Final source-marker/CLI completion | `2026-11-08T11:42:38.318214+00:00` |

The [initial attempt](/private/tmp/rsr-bug029-prd-20261108-phr9whfy/prd-promotion-command.json) exited 1 after
`1553.825` s at `2026-11-08T10:46:03.621716+00:00`; it remains **NOT PASS**.
All selectors were verified, but Mac `prd-start` status returned launchctl exit
`-1`/empty output before probes. Cause is unestablished; later [read-only checks](/private/tmp/rsr-bug029-prd-20261108-phr9whfy/mac-start-readonly-diagnostic.json)
found both agents running. [Resume 01](/private/tmp/rsr-bug029-prd-20261108-phr9whfy/prd-resume-01-command.json) also remains
**NOT PASS**: exit 1 after `255.844` s, Linux direct-helper empty
stdout; its existing scope was observed `ready`, which is not a terminal result.
Changed network source prevented Linux/Factory direct access. The proposed firewall
apply refused its source guard before root changes: [fresh SSH source](/private/tmp/rsr-bug029-prd-20261108-phr9whfy/firewall-apply-refused-source-check.json)
was the already allowed `81.95.203.65`. [Verified network reads](/private/tmp/rsr-bug029-prd-20261108-phr9whfy/prd-network-restored-summary.json)
then passed for all three workers; **no firewall update was needed or applied**.
The separate [supported renewal](/private/tmp/rsr-bug029-prd-20261108-phr9whfy/rsr-bug029-renewal-b874-20261108T113131Z-b49acd29/renewal-summary.json) synchronized exact evidence to
all four stopped/held hosts, preserving receipt and approval; effective deadline
`2026-11-08T14:19:47+00:00` did not extend the owner's window.
[Successful resume 02](/private/tmp/rsr-bug029-prd-20261108-phr9whfy/prd-resume-02-command.json) reused the same operation,
approval and verified-host flags, with no selector replay, rebuild, rollback,
additional recovery or manual executable-job submission. The operator published
no mailbox requests or ACKs.

No rollback was run. The [completed operation](/private/tmp/rsr-bug029-prd-20261108-phr9whfy/completed-operation.json)
retains all event history, selector journals and original predecessor evidence.
Fresh PRD backup bindings are preserved in the released receipt:

| Host | Backup directory | Manifest SHA-256 |
| --- | --- | --- |
| `linux-host` | `/home/ubuntu/.local/share/remote-session-runner/backups/prd-catalog-20261108T102742.731891663Z` | `9bfe383845a99ebfa05cdbd93dce6001cf23b3a4b4bcb9cdfb10ba78ce824e91` |
| `sandbox-host` | `/home/ubuntu/.local/share/remote-session-runner/backups/prd-catalog-20261108T102745.336691384Z` | `d3748080c172722caf877d4df0d0fb9fd89b64ce1c66bdf0487e07001557fc4a` |
| `factory-sandbox` | `/home/ubuntu/.local/share/remote-session-runner/backups/prd-catalog-20261108T102802.531894031Z` | `f59f6e1392cb45d794d598e22e0b3bb746ac28f7b9b1c96670cde324f54889ba` |
| `mac-control-plane` | `/Users/tomasz.walczuk/Library/Application Support/RemoteSessionRunner/backups/prd-catalog-20261108T102805.896515000Z` | `7606bf1c0599052bf72f418de257660197f1b0aa4fb0802df80522cb44dc11e0` |

These are PRD backups; no Dev/QAS database or backup was substituted. Database
contents, configurations, selected-lane keys and mailbox assignments were preserved.

## Acceptance, release and independent postflight

All eight `catalog-functional-v1` gates passed: **capacity, direct mTLS,
isolation, lifecycle, limits, Mac-local, queued SSH and teardown**.
The [PRD acceptance](/private/tmp/rsr-bug029-prd-20261108-phr9whfy/prd-acceptance.json) binds the exact release and
all four bundle hashes; evidence SHA-256 is `0845a3f00be0983588913769dae6a61cd2b2dbcd8309f73614a78c22ee6163a6`,
and the acceptance-file SHA-256 is `1b55be25b3683f9f994d263c07b3b785c68a6ac27361fa1bb2b13adf39cc2735`.

All four receipt copies are **released**, with **20 transitions**
and identical SHA-256 `a1b3f0aa1e7ae88d0b6ba8de5a888b9c049164ca8f59c55b77afee29125190b5`.
The [five-endpoint health proof](/private/tmp/rsr-bug029-prd-20261108-phr9whfy/prd-health-postflight.json) verifies
ready services on exact source `1263205`, zero active session/command slots,
queued commands and storage/cleanup errors. Worker pending gauges are zero.
Mac historical gauges are bounded by pre-resume 13 queued intents / 4 backlog:
`{"local-api.sock": {"mailbox_backlog": 4, "queued_intents": 13}, "locald.sock": {"mailbox_backlog": 4, "queued_intents": 13}}`.
These metadata counts are separate from execution; baseline inventory had 3,209
accepted local intents and 25 remote intents (12 accepted / 13 uncertain).
The postflight reports `3210` local intents and
`25` remote intents, including `13` uncertain.
The [identity proof](/private/tmp/rsr-bug029-prd-20261108-phr9whfy/mac-prd-historical-identity-proof.json) confirms remaining
uncertain identities are original, with no newly uncertain records. The held
mailbox baseline had 0 accepted/actionable pairs and 37 historical terminal
responses awaiting ACK; the operator did not settle them. Neither these reads
nor cumulative dispatch counters establish normal-load health or a terminal result.
They do not exclude normal runtime reconciliation/retry under configured policy.

The [fresh plan/ref proof](/private/tmp/rsr-bug029-prd-20261108-phr9whfy/prd-plan-postflight-proof.json) passed at
`2026-11-08T11:45:21.826682+00:00`: four active services, schema 40, empty restart inventories,
unchanged PRD configuration hashes, no admission holds and adequate measured
capacity. The 500,000,000-byte operating buffer and separate 67,108,864-byte
selector reserve remain distinct checks; postflight is not a new mutation approval.
Dev/QAS stable installation identities matched the pre-operation baseline.

Primary Gitea and automatic GitHub PRD refs independently match accepted service
source `126320552acf0a2609d7d75c09016b867b1dbd13`, rather than controller `22cb726`.
The successful resume preserved `runtime_accepted_branch_sync_pending`; only the supported
PRD branch-sync action was retried. It completed at `2026-11-08T11:42:38.318214+00:00`,
without redeployment, service control or functional-probe replay.

## Boundaries and Git delivery

Dev and QAS were not promoted or rerouted by this PRD action. Mailbox routing and
historical request identities remain unchanged. **Filesystem-mailbox acceptance
is NOT RUN**: the operator published no mailbox requests/ACKs or CI redispatch.
The owner was left responsible for resuming external clients; none were resumed
by the operator. No normal-load observation is claimed.

Final evidence-only Git delivery is separate. Verify the matching final
Mac/Gitea/GitHub/worker SHAs in the private
[delivery record](/private/tmp/rsr-bug029-prd-20261108-phr9whfy/deployment-closeout-delivery.json) **before claiming
that documentation delivery is complete**. This document does not self-reference
its own eventual commit. Its runtime claims are bound to the immutable records above.
