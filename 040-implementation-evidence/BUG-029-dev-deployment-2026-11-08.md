# BUG-029 — Dev deployment closeout, 2026-11-08

**Dev deployment and release-bound functional acceptance are complete on all
four hosts. QAS and PRD were not promoted or rerouted.**

## Authorization and immutable release

The owner confirmed “Dev is unused; proceed,” recorded at 07:21:02 UTC.
This authorized Dev maintenance; host quiescence, backups and admission fencing
were independently enforced by the supported promotion toolkit.

| Identity | Value |
| --- | --- |
| Release | `rr-20261108-bug029-1263205` |
| Tested service source | `126320552acf0a2609d7d75c09016b867b1dbd13` |
| Build | `7f3fd807a48e934d09df5a7cfc344680` |
| Release SHA-256 | `1b54ce565da1c94d64604f8ce74462dd49974f67d60e0fcc8f772339e82f0603` |
| Provenance SHA-256 | `162ecf9a9b40343c7a132d88014e66f35623fcc3e7d7b30b54471149efd981d2` |
| Dev operation | `9dd615f3f0ee4e54b02a8dc1863a3c3d` |

| Host | Installed Dev bundle SHA-256 |
| --- | --- |
| `linux-host` | `46e4894083f22dc0258de773e1594c374ce1f8929f813576167dbf38fff7f29e` |
| `sandbox-host` | `d6f7488a4c5af758040bc82d02eb00c96202b267a6bdf1362f8a819ff804d8be` |
| `factory-sandbox` | `da716d140522405d158bfbb94f58eea4c35a51fa729ada9a3289dbce406a02f5` |
| `mac-control-plane` | `25b8b596b00dc96933ee947a9180e3c024d9cfd469047d7eb0811fffb635b004` |

Four canonical owner-only `native-full-v1` receipts bind the original passing
native gates to source `1263205`, each host/platform and its measured Go 1.27.1
binary. The source and historical failed attempts remain in [BUG-029 source
evidence](BUG-029.md). No service candidate was rebuilt after sealing.
Private [owner confirmation](/private/tmp/rsr-bug029-dev-20261108-wv8ybdvf/dev-owner-unused-confirmation.json),
[build invocation](/private/tmp/rsr-bug029-dev-20261108-wv8ybdvf/native-build-command.json) and
[sealed build/release](/private/tmp/rsr-bug029-dev-20261108-wv8ybdvf/native-build.stdout.json) preserve the exact inputs.

## Sandbox catalog repair before activation

Sandbox's selected catalog payloads had been deleted while its services remained
running. Native reconstruction used new scratch build identities and the exact
old sources. Every original cohort identity, canonical manifest, bundle digest
and component hash/size matched before supported `host-publish` restored:

- `rr-20261030-bug028-7043c51`, Sandbox bundle
  `0a0656c3451d0d94b28d71a8cc327a6adb65e0ad66d2055a29a720883a1dceab`.
- `rr-20261030-e3d3f5a`, Sandbox bundle
  `bcc5907e776addd80bd3bfeeb993befed5612518d2da0338ad0e2eaebad28b78`.

The [repair proof](/private/tmp/rsr-bug029-dev-20261108-wv8ybdvf/sandbox-catalog-restoration.stdout.json), SHA-256
`06ce2858701e799518f1193f4e2accd572ea21ec6c07f6d2d5e0b43012de42b3`,
records completion at 07:31:56 UTC. Sandbox Dev/QAS/PRD PIDs, restart counts,
start timestamps, selectors, configuration hashes and receipt hashes were
unchanged by this exact artifact restoration. No peer service was restarted.
The [verified restoration package](/private/tmp/rsr-bug029-dev-20261108-wv8ybdvf/sandbox-exact-catalog-restore-package.json)
retains the original manifests and component declarations.

## Interrupted attempt and operator correction

The first promotion stopped after Linux switched, when Sandbox's
`catalog-selector-find` fully validated missing payloads belonging to unrelated
historical journals before filtering the requested cohort. Metadata was retained;
no journal or historical receipt was deleted to bypass this guard.

Controller correction `1ecf91075d75a71944a7dc33b68c0a7e72ee07b5` preserves
canonical/owner/mode/link/filename, host/lane and lexical-path checks for every
journal. Live artifact checks apply to matching journals. Actual select,
inspect, reconcile and rollback retain full validation; ambiguity, receipt-bound
journal hashes and expected-predecessor guards still fail closed. This operator
correction is recorded separately as [BUG-030](../050-bugs/030-selector-discovery-requires-unrelated-historical-artifacts.md);
sealed source `1263205` is unchanged. The resumed coordinator binaries embed
the delivered full controller revision; their [attestation](/private/tmp/rsr-bug029-dev-20261108-wv8ybdvf/bin-1ecf910/attestation.json)
records executable hashes and version output.

The first Mac operator package run remains **NOT PASS**: exit 1 after 51.027 s.
Its task TMPDIR inherited group 0 rather than the account's primary group 20,
causing owner/group-sensitive fixtures to reject their temporary artifacts.
After correcting only the private TMPDIR group, the same `laneinstall` and
`releaseflow` package tests passed without changing those fixtures.

| Operator correction check | Mac | Linux | Sandbox | Factory |
| --- | --- | --- | --- | --- |
| Changed-package tests | PASS 54.643 s | PASS 106.018 s | PASS 187.362 s | PASS 99.020 s |
| Relevant vet | PASS 2.168 s | PASS 15.090 s | PASS 17.040 s | PASS 14.136 s |
| Native operator build | PASS 1.312 s | PASS 157.016 s | PASS 144.312 s | PASS 136.623 s |

[First Mac result](/private/tmp/rsr-bug029-dev-20261108-wv8ybdvf/operator-correction-mac/summary.json),
[corrected retry](/private/tmp/rsr-bug029-dev-20261108-wv8ybdvf/operator-correction-mac-retry/summary.json),
[three-worker results](/private/tmp/rsr-bug029-dev-20261108-wv8ybdvf/operator-validation-1ecf910/SUMMARY.json) and
[operator delivery](/private/tmp/rsr-bug029-dev-20261108-wv8ybdvf/operator-correction-delivery.json) preserve the results.
Gitea and its automatic GitHub mirror matched `1ecf910`; workers fast-forwarded
before their checks. These operator checks do not relabel the initial failure.

## Completed same-operation promotion and acceptance

The supported toolkit resumed **the same** operation and immutable candidate,
preserving Linux's prior switch, original backups and cohort history. The resume
CLI exited 0 at **08:27:10 UTC**; operation stage is `complete`, branch synchronization
is `not_applicable` for Dev, and no rollback was run. Completion preceded the
original 08:48:13 UTC cohort expiry; no renewal was needed.
[Resume invocation/result](/private/tmp/rsr-bug029-dev-20261108-wv8ybdvf/dev-resume-command.json) and
[completed operation](/private/tmp/rsr-bug029-dev-20261108-wv8ybdvf/completed-operation.json) retain the exact sequence.

All eight `catalog-functional-v1` gates passed: **capacity, direct mTLS,
isolation, lifecycle, limits, Mac-local, queued SSH and teardown**.
Acceptance was recorded at `2026-11-08T08:23:35.399774Z`, evidence SHA-256
`b6328a946d26ecc7c50304331d6553cc853a01d155551792c96b473178e02ded`.
The [acceptance file](/private/tmp/rsr-bug029-dev-20261108-wv8ybdvf/dev-acceptance.json) has SHA-256
`b2f272298e7240db732e69e1000b1117aec96096b78b7f92e809393631b2c22d`.

## Released state, postflight and boundaries

The [four-host health postflight](/private/tmp/rsr-bug029-dev-20261108-wv8ybdvf/dev-health-postflight.json) at approximately
08:27:55–08:27:57 UTC reports ready services on exact source `1263205`, including
both Mac ingress/executor. Active session/command slots, queued work, mailbox
backlog, storage errors and cleanup failures were zero. Mac's cumulative dispatch
attempt counter was 51; zero error/work gauges are not a claim all counters reset.
All four cohort copies are **released**, with **16 transitions** and identical SHA-256
`eafe061dad566fdea662e10f474e07e6ac38beb73c68a05becc45adae7256df0`.

The [fresh plan postflight](/private/tmp/rsr-bug029-dev-20261108-wv8ybdvf/dev-plan-postflight-proof.json) passed at
08:29:15 UTC: all four Dev services active, correct candidate selected, schema 40,
empty work inventories, unchanged Dev configuration hashes and no admission holds.
Measured available bytes were Linux 36,883,292,160; Sandbox 5,480,493,056;
Factory 34,693,853,184; Mac 90,781,126,656, each above the 500,000,000-byte buffer.
This postflight is status evidence, not a new backup or selector authorization.

Peer-lane identities matched the preflight baseline: **QAS/PRD runtime selections,
configuration and mailbox assignments remain unchanged; neither lane was promoted**.
**Filesystem-mailbox acceptance is NOT RUN.** No mailbox requests or ACKs were
published, no historical project request was replayed, and no CI was redispatched.
Normal-load mailbox observation is not established by these bounded functional gates.

Final evidence-only Git delivery is recorded in the private
`/private/tmp/rsr-bug029-dev-20261108-wv8ybdvf/deployment-closeout-delivery.json`.
No source tests are repeated solely for that Markdown update.
