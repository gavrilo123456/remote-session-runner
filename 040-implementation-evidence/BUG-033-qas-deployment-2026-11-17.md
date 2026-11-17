# BUG-033 — QAS deployment — 2026-11-17

**COMPLETE — QAS accepted, admissions released and QAS source-marker synchronization complete.**
The owner authorized QAS deployment and confirmed nothing is running in QAS.
Operation `14cc57668bfd47768a1d49fe49c68440` completed without rollback. The initial
supported CLI finished runtime acceptance and release at `2026-11-17T21:45:40.768513+00:00`
after 1290.963 seconds, including fresh baselines, backup/preparation,
ordered switching, acceptance and admission release. It truthfully returned
runtime_accepted_branch_sync_pending: Gitea accepted the QAS marker, while the
automatic GitHub mirror was still catching up. The supported branch-sync-only
retry completed at `2026-11-17T21:47:00.458012+00:00` in 2.423 seconds;
no redeployment, rollback or service action was repeated. The current durable
operation and branch_sync fields both record complete. Source checks and
network preparation are separate from those elapsed times.

## Same release accepted in Dev

The immutable release `rr-20261117-bug033-fd20b1e` was promoted without
rebuilding. Its runtime source is fd20b1ebb5b551a1e9d143082101ae5d8843dd7b;
the attested coordinator source is 01f91f0475f00014c68def4e0c9828ff011864f0.
That documentation-only coordinator successor does not change the service bytes.

- Release SHA-256: `38127d62b469cddcb87968d86615f3c212d1e06de42c79862bf07d24ef1425e8`.
- Provenance SHA-256: `0d116cc90cbd61fad5f027170c550b278076112a366c238b7695d52856c15e89`.
- Build: `1391d85143b789f6bceb62e9ab27f5b5`, ready and published on four hosts.
- Passed Dev operation: `3b02eecc610445438a61f2387457bde5`.
- Dev acceptance evidence: `38dbc2cbf955bc4435a53c5cfa176d70e2cf7697d6ad996e8a9d1c154c7c674b`.

[Dev evidence](BUG-033-dev-deployment-2026-11-17.md) records the four full native
source-suite, vet, build and smoke results, original failures and corrections.
Those source checks were not repeated for this unchanged release promotion.

## Preparation and switching

Fresh plans found four active QAS predecessors rr-20261111-bug031-0b7c031,
schema 40, empty work inventories and passing capacity. The toolkit captured
fresh consistent backups, held admissions, stopped only QAS, synchronized its
cohort receipt and switched Linux → Sandbox → Factory → Mac. No database
restore was performed. Read-only planning did not create the operational backups.

| Host | Installed bundle SHA-256 | Switch intent → verified | Selector journal |
| --- | --- | ---: | --- |
| linux-host | `d9042be80f966fb1c55aef5c62882b9399f0d791c7eb3af2a77d8c80fdd0b4be` | 53.01s | `91b32d343bc8146014f96160e4f45c7b` |
| sandbox-host | `28d65c8b623a4a609dc654656e86e8f480518a1895ca1f565f4aef3b644dc590` | 52.421s | `090b83506a43d89301efcc9cefb43506` |
| factory-sandbox | `979e27c3db919976a78c23a6410f79e8813b2efd0e74d8472053d01a1b2b548c` | 57.371s | `d67013b2c267cc6df9fa6d362a0b8562` |
| mac-control-plane | `a9fceaf3f1bdf3a5ad851675cce58efd97eb47423bf9f48856bb1ecde507c60d` | 36.345s | `134595c617bf66d6d903ea376787c658` |

Switch durations cover guarded selection and journal verification, rather than
functional acceptance or final release. All selected bundle hashes match Dev.

| Host | Consistent backup manifest SHA-256 |
| --- | --- |
| linux-host | `a5c2cc2db685197989baa1f17bcefe49dbd9c9ddcc13e7a724bdc7ae266cc532` |
| sandbox-host | `f2669443116b5ea720b74b07484b6871e306ad12da52a40274483b5770e151a2` |
| factory-sandbox | `0c64273821f21c54570f649c673883127860a7e05739978c06c0259d04835477` |
| mac-control-plane | `8575138a2e93fc757b9f7651bd2a2e9de8215f68646ccffb60d300ca8d2fd700` |

## Acceptance and postflight

All eight release-bound catalog-functional-v1 gates passed: capacity,
direct-mTLS, isolation, lifecycle, limits, Mac-local, queued-SSH and teardown.
Acceptance was recorded at `2026-11-17T21:41:53.795791Z`, evidence SHA-256
`486fdd8904952910329c01705e1a8bb3df36efd3cebcf8875fd0cbc2367d0813`.

Supported cohort status returned byte-identical released receipts on all four
hosts: 16 transitions, SHA-256
`3f7b0f3f44e90366f893206aae5ea3cc134e42d27ac120b112b62e02c6081963`. Canonical Mac file bytes matched the public
status output. The toolkit completed the QAS source-marker synchronization.

Fresh postflight found all four services active on the exact bundles, schema40,
empty work inventories, no admission holds and passing capacity. Sandbox had
756391936 bytes free against 500000000 required.
Dev and PRD stable identity evidence matched the fresh pre-operation baseline;
their selectors, configurations and service identities were preserved.
Worker authenticated TLS1.3 readiness and both Mac QAS private health sockets
passed after final release/restart. Functional commands passed before final
admission release; the post-restart health checks submitted no commands.

## QAS network policy

The owner's earlier IP-independent mandatory-certificate policy selection was
applied to this newly assigned QAS lane. Linux and Factory initially refused
8445 before TLS despite active QAS listeners. Exact before/candidate snapshots,
backups, static selected-runtime/config/CA/principal evidence and syntax tests
preceded an independently reviewed, hash/identity-guarded application. Each
host received one INPUT NEW TCP8445 any-IPv4-source ACCEPT rule, inserted before
its existing terminal reject. No full live firewall restore was performed.

Dev8444 and PRD8443 rules were preserved. Required TLS1.3, client certificates,
mapped principals, hostname verification and CA/certificate/SPKI/URI pins remain
enabled. Before promotion and after final restart, all three workers returned
HTTP200/ready to the configured client, while all three missing-client-cert
connections failed at TLS with certificate required. No credentials or live
host policy files are committed.

Both persistent files match their reviewed candidates and retain root:root0640,
one link. Both netfilter-persistent units are enabled/active with successful
status. No reload or reboot was used to claim a boot-time retest.
The first post-apply checker wrapper had a Ruby syntax error before making any
connection; the corrected read-only check passed and the original script is
preserved. This was not a failed QAS runtime probe.

## Boundaries and delivery

PRD deployment is NOT RUN for this release. This QAS task published no mailbox
requests or ACKs and changed no project mailbox assignment. Filesystem-mailbox
acceptance and live lost-acceptance fault injection remain NOT RUN. BUG-033's
observation/no-replay behavior is covered by disposable source regressions.
The historical GPU exchange remains administratively indeterminate and was
not replayed, replaced or ACKed; this deployment does not prove its execution
or teardown. Existing unrelated holds and caches were preserved.

Private captures: `/private/tmp/rsr-bug033-qas-20261117-q9arsx34` and `/private/tmp/rsr-bug033-qas-network-20261117-smmkmqzq`. Durable operation/acceptance records
remain under the existing runner-release-operations store. Tracked changes in
this closeout are evidence/documentation only, verified with diff review and
git diff --check. Mac primary push, exact automatic GitHub mirror and clean
three-worker fast-forwards are recorded in `/private/tmp/rsr-bug033-qas-20261117-q9arsx34/qas-closeout-git-delivery.json`.
