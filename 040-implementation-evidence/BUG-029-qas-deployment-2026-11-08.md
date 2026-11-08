# BUG-029 — QAS deployment closeout, 2026-11-08

**The Dev-accepted BUG-029 release is deployed and accepted on all four QAS
hosts. QAS branch synchronization is complete. Dev and PRD remain unchanged.**
All times below are UTC on 2026-11-08.

## Authorized scope and unchanged candidate

The owner confirmed **“QAS is unused; proceed”** at 09:02:31.336068.
This authorized QAS maintenance; runtime empty-work, ownership, stopped-service,
admission, consistent-backup and capacity guards remained independently required.
[Owner confirmation](/private/tmp/rsr-bug029-qas-20261108-_jzwz7df/owner-qas-use-confirmation.json) records the scope;
PRD clients were not required to pause.

| Identity | Value |
| --- | --- |
| Release | `rr-20261108-bug029-1263205` |
| Service source | `126320552acf0a2609d7d75c09016b867b1dbd13` |
| Release SHA-256 | `1b54ce565da1c94d64604f8ce74462dd49974f67d60e0fcc8f772339e82f0603` |
| Provenance SHA-256 | `162ecf9a9b40343c7a132d88014e66f35623fcc3e7d7b30b54471149efd981d2` |
| QAS operation | `68696a13af8cec48369c2ed88bd376d0` |
| Coordinator revision | `aa632c91d7dc399d666225f5edc4a27c0355f868` |

| Host | Selected QAS bundle SHA-256 |
| --- | --- |
| `linux-host` | `46e4894083f22dc0258de773e1594c374ce1f8929f813576167dbf38fff7f29e` |
| `sandbox-host` | `d6f7488a4c5af758040bc82d02eb00c96202b267a6bdf1362f8a819ff804d8be` |
| `factory-sandbox` | `da716d140522405d158bfbb94f58eea4c35a51fa729ada9a3289dbce406a02f5` |
| `mac-control-plane` | `25b8b596b00dc96933ee947a9180e3c024d9cfd469047d7eb0811fffb635b004` |

The [sealed release](/private/tmp/rsr-bug029-qas-20261108-_jzwz7df/release.stdout.json) and
[prior Dev acceptance](/private/tmp/rsr-bug029-qas-20261108-_jzwz7df/dev-acceptance.stdout.json) match the same digest,
profile and all four bundle hashes. The [Dev closeout](BUG-029-dev-deployment-2026-11-08.md)
preserves source qualification and the operator correction. **No service-candidate
rebuild/reseal or source-suite/operator-qualification rerun was required or performed.**
QAS executed its own release-bound functional acceptance.

## Fresh QAS preparation and ordered deployment

The [read-only plan](/private/tmp/rsr-bug029-qas-20261108-_jzwz7df/qas-plan.stdout.json) completed at 09:00:52.573213,
exit 0 after 80.450 s. All four QAS services were active on
`rr-20261030-bug028-7043c51`, schema 40, with empty work inventories and sufficient
capacity. The operating buffer remained 500,000,000 bytes; actual backup and
staging needs were checked by the guarded operations.

The supported toolkit started at 09:02:31.487039. Common preparation completed at
09:07:19 and captured **fresh QAS-specific consistent backups**, under each
`remote-session-runner-qas` / `RemoteSessionRunner-qas` backup root. Dev backups
were not reused. The four-host hold, receipt synchronization, journals and
compare-and-switch guards governed Linux → Sandbox → Factory → Mac selection.
No rollback was run. Exact backup references are retained in the
[released cohort receipt](/private/tmp/rsr-bug029-qas-20261108-_jzwz7df/released-mac-cohort-receipt.json).

## Passed QAS acceptance and released runtime

All eight `catalog-functional-v1` gates passed: **capacity, direct mTLS,
isolation, lifecycle, limits, Mac-local, queued SSH and teardown**.
Acceptance was recorded at **09:18:11.954740**, evidence SHA-256
`28856c37ffbec9b791f91b5ec6d66f0ea2f0041aba0b0b6ad57f75b42d077f7a`.
The [QAS acceptance file](/private/tmp/rsr-bug029-qas-20261108-_jzwz7df/qas-acceptance.json) has SHA-256
`d8f3fd93416245ccd94d25bd9e88459917a433e95de4749185e603a1ae73aa42`.

The coordinator recorded `cohort-released` at **09:21:37.571697**.
All four local receipt copies are **released**, with **16 transitions** and
identical SHA-256
`a8339d4ee458fc322b6025425f2f87d76ceb612b1b1d6723389cfefe810586cd`.
The [health postflight](/private/tmp/rsr-bug029-qas-20261108-_jzwz7df/qas-health-postflight.json) reports all five
endpoints ready: three workers plus Mac ingress and executor, all on source
`1263205`. Sampled active/queued work, mailbox backlog, storage errors and cleanup
failures were zero. Mac's cumulative dispatch-attempt counter was 1,266; this
is not a claim that every counter was zero or that normal load was observed.

## Preserved mirror lag and source-marker-only retry

Before promotion, independently verified Gitea and GitHub `qas` refs both pointed
to `7043c5100adcc4b9da239074fff3d55addcf67fa`.
The [initial command](/private/tmp/rsr-bug029-qas-20261108-_jzwz7df/qas-promotion-command.json) exited **0** at
09:21:41.753917 with outcome `runtime_accepted_branch_sync_pending`: runtime
acceptance/release had passed, while the automatic mirror had not yet caught up.
That original pending event remains in the operation history.

Only the supported `runner-release branch-sync --lane qas --operation 68696a13af8cec48369c2ed88bd376d0`
was retried. It exited 0 after **2.133 s**, completing at **09:22:13.936934**.
No redeployment, selector switch, service restart or functional-probe replay
was performed for this retry. [Retry command](/private/tmp/rsr-bug029-qas-20261108-_jzwz7df/branch-sync-retry-command.json)
and [result](/private/tmp/rsr-bug029-qas-20261108-_jzwz7df/branch-sync-retry.stdout.json) retain the exact action.
The durable operation is now **`complete`**, with **`branch_sync=complete`**.

Independent primary/mirror ref checks confirm `qas` now names the accepted
**service source `126320552acf0a2609d7d75c09016b867b1dbd13`**,
not the coordinator revision `aa632c9`. Runtime and source-marker completion are
separate recorded results, both now complete.

## Final postflight and boundaries

The [fresh plan and independent ref proof](/private/tmp/rsr-bug029-qas-20261108-_jzwz7df/qas-plan-postflight-proof.json)
passed at **09:24:19.632243**: all four QAS services active on the expected
bundles, schema 40, empty work inventories, unchanged QAS configuration hashes,
verified capacity and no admission holds. Available bytes were:

| Host | Postflight available bytes |
| --- | --- |
| `linux-host` | 36,881,481,728 |
| `sandbox-host` | 5,468,930,048 |
| `factory-sandbox` | 34,691,899,392 |
| `mac-control-plane` | 85,921,034,240 |

The same proof compared peer lanes and found **Dev/PRD identities unchanged**.
Neither peer was promoted, reconfigured or rerouted. Existing retained work
and project mailbox assignments were preserved.
**Filesystem-mailbox acceptance remains NOT RUN.** No mailbox request or ACK
was published, no historical project request was replayed, and no CI was
redispatched. Readiness and bounded functional gates do not establish normal-load
mailbox observation. PRD deployment is outside this completed QAS action.

Final evidence-only Git delivery is recorded in the private
`/private/tmp/rsr-bug029-qas-20261108-_jzwz7df/deployment-closeout-delivery.json`.
Source/operator tests were not repeated solely for that Markdown update.
