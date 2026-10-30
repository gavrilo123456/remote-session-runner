# BUG-027 — PRD deployment closeout, 2026-10-30

**PRD is deployed, functionally accepted and open for admission on all four hosts.** The owner may resume PRD clients. Clients were left paused; normal project-load observation remains pending. Filesystem-mailbox functional acceptance was NOT RUN, and no mailbox requests or ACKs were published.

All timestamps below are UTC; Warsaw was UTC+1 on this date. The current Linux worker is `79.72.94.59`. The retired `129.151.232.40` was not restored or used as a migration prerequisite.

## Release and actual completion

- Immutable release: `rr-20261030-e3d3f5a`.
- Release digest: `15de9ab8e33749e00b6c5121d691b23bfa79ead4ee64ea5b7b930611404f96df`.
- Sealed service source: `e3d3f5a133e58c97040920162a221504610b9e1b`.
- Public promotion operation: `38f966a32a0e4bb094dcbd37d994af0b`; final stage `complete`, branch synchronization `complete`.
- Dev acceptance `b7a87e98f78c80c95cc47306851911ee` and QAS acceptance `74518a25360a2364f37e83f52c88b83e` preceded PRD and bind the same release digest and bundle bytes.
- PRD acceptance was recorded at `2026-10-30T18:13:11.3116Z`; acceptance evidence SHA-256 `275d2b1d35a6f2b8eba7c9ddb85dc6897d702af42ce0b4bee17319f18c0c1c84`. The acceptance file at `/Users/tomasz.walczuk/projects-runtime/runner-release-operations/acceptances/prd/rr-20261030-e3d3f5a.json` has SHA-256 `7383decc6a0d0b222a9415c4266ba92a4ea7147cb87315cb3c179eb0e30b65fc`.
- Cohort release was recorded at `18:16:12.064084Z`. All four final receipt copies are `released`, contain 20 transitions and share final head SHA-256 `4dc2e120e8716aa7439af8373dada0888522ecaf87d4df6ab0018185c5c28e33`. Each host's discovery reports `admission_held=false`.
- Final four-host metadata at approximately 18:17 UTC confirms selected bundles, active services, schema 40 and empty active/resumable/capacity inventories: `/private/tmp/rsr-prd-resume-shared-flow-20261030/final-four-host-metadata.json`, SHA-256 `c1f01970e6d006ead21fb9f2011be4010c5acbfcba2840fd61b31447afe705d8`.

| Host | Selected bundle SHA-256 | Schema | Service / admissions |
| --- | --- | ---: | --- |
| linux-host | `cf41ccd2e07289acdb68d4303eac1953da21bc8b69a3b99c29f7f07a099e6182` | 40 | active / open |
| sandbox-host | `bcc5907e776addd80bd3bfeeb993befed5612518d2da0338ad0e2eaebad28b78` | 40 | active / open |
| factory-sandbox | `b2539a5955308e58c482c10528576f77cdb11eea15986b7fec3a353ffda0b5a8` | 40 | active / open |
| mac-control-plane | `4d8238d1d3fe2ae8f7dc5955b2bfdda741c4c1bc2b09e580f0cebc6db4a6f43a` | 40 | active / open |

The full continuation resumed at `18:07:37Z` using the supported `promote-to prd all --resume 38f966a32a0e4bb094dcbd37d994af0b` path. Runtime acceptance and release completed before the automatic GitHub mirror had caught up with the PRD source marker; this was recorded as `runtime_accepted_branch_sync_pending`, not deployment failure. A subsequent public branch-sync retry completed. Captures:

- `/private/tmp/rsr-prd-resume-shared-flow-20261030/resume-20261030T180737Z.stdout.json`, SHA-256 `1b1c3793ad2e3fd69c0018bd37bfd7412e7fc4bd0201498feaa0f4000c9e34b9`; stderr empty.
- `/private/tmp/rsr-prd-resume-shared-flow-20261030/branch-sync.stdout.json`, SHA-256 `882f518e5d2e304e6ba95168bb2b2e0a84a82fb652ea1fea88b86ce0f82ffa29`; outcome `completed`, operation and branch-sync stages `complete`; stderr empty.

## Functional acceptance and post-restart observations

The toolkit's release-bound `catalog-functional-v1` acceptance records these actual gate outcomes:

| Gate | Result |
| --- | --- |
| direct-mtls | PASS |
| queued-ssh | PASS |
| mac-local | PASS |
| limits | PASS |
| capacity | PASS |
| lifecycle | PASS |
| teardown | PASS |
| isolation | PASS |

These are the saved cohort acceptance results, distinct from readiness checks. The acceptance record contains the gate results and combined proof digest; it does not separately persist route-level job/session IDs or raw helper output. This closeout does not invent those identities or an additional BUG-026 probe result.

After the final receipt synchronization and admission-reload restarts, the 18:16:40 UTC postflight passed for all five components: Linux, Sandbox and Factory `runnerd`, Mac `runner-local` and Mac `runner-locald`. Every component reported the expected service source. Worker routes negotiated TLS1.3 with certificate-chain and hostname/IP verification result 0. Storage-error and cleanup-failure counters were zero. No additional functional command was submitted by this readiness observation. Capture: `/private/tmp/rsr-prd-resume-shared-flow-20261030/final-readiness-20261030T181640759251Z.json`, SHA-256 `bc6fdbf52a3d35804a80c277e8b8e6b42b57422fb49cf6ce1b89d7519e371b3f`.

The Mac still reports five uncertain remote intents and degraded optional stale-router health, while required readiness passes. These preserved intent records do not establish target outcomes and must not be replayed, ACKed or manually released based on health counters.

## Qualified Mac PRD detailed inventory

The separately qualified diagnostic was installed at `~/Library/Application Support/RemoteSessionRunner/bin/runner-lane`, mode `0700`, owner UID 502, regular file without a symlink. Its SHA-256 is `6636a43c0b2eff4013a8696d4e3fec3d3cfd83ec1dfd0df47c0501b227e0d196`; diagnostic source `883e2af0373b7de94f172df31909434f9a23c38d`, Go 1.27.1 darwin/arm64. The exact already-qualified bytes were reused; no service bundle was rebuilt for this diagnostic.

```sh
# Mac — installed owner-only diagnostic, selected PRD authority
"$HOME/Library/Application Support/RemoteSessionRunner/bin/runner-lane" \
  catalog-work-inventory --host mac-control-plane --lane prd --details
```

The command exited 0. Its complete read from `18:17:16.695195Z` through `18:17:17.386478Z` binds the selected PRD release/Mac bundle/configuration and schema 40. The outstanding session, command, job, reservation, command-slot, retained-pair and recovery-finalization sections each reported zero records. This does not mean historical rows were deleted. It separately reports 1,441 local delivery intents and nine remote delivery intents: five `uncertain`, four `accepted`; mailbox bindings zero. Delivery intent state does not by itself prove terminal execution or cleanup.

The safety fields remain `safe_to_resume=not_established` and `recovery_authorized=false`, as required by this inventory-only contract. PRD readiness for client resumption comes from the separate successful deployment/acceptance/release evidence, not from overriding those diagnostic fields. #514 was not recovered again.

Capture: `/private/tmp/rsr-prd-resume-shared-flow-20261030/prd-detailed-inventory.json`, SHA-256 `aa9803fe11dda1fb8c1734b1226c10a1557aa47118598ee5bf78923ade305c83`. Detailed private rows are not copied into Git.

## Controller corrections, continuation and preserved failures

The service-neutral controller source was frozen at `76f57cd05d18fb4b679e6bc565e80cf320c776df` through the operation. The shared public path separates recognition of a closed admission hold from permission to mutate an expired operation. The exact rolled-back predecessor/held-successor relationship remains discoverable after the original evidence ages out; selector changes and per-host supersession still require fresh original evidence or the verified bounded continuation. The original pure supersession API remains freshness-only. Fresh replacement-Linux renewal metadata binds its intentionally absent predecessor bundle to the exact validated fresh-install baseline. Receipt fields and schema were unchanged.

The source-validation manifest records complete Mac `laneinstall`, `releaseflow` and `runner-lane` package tests, vet and three native CLI build/help/version checks; all three workers passed focused continuation/supersession/admission checks and native helpers/vet. The worker regex selected no CLI tests, so these are not described as public-command acceptance. Earlier interrupted/full-suite failures remain NOT PASS. Manifest: `/private/tmp/rsr-prd-resume-shared-flow-20261030/source-validation.json`, SHA-256 `32e8cea4cd958ee3d7629d61e10add10b5b925334a39bc6adbee61e24781f789`.

| Source-check capture | SHA-256 |
| --- | --- |
| `factory-sandbox-source-check.log` | `988e249f54cbc30a38ad14067178be079d9201b4e3fc3dcaa0835a2c71ca954a` |
| `linux-host-source-check.log` | `e9e9bd6ea71834e6a225c555aafa6af824ea82d4bce223838a97349f5ebffd8d` |
| `mac-changed-packages.log` | `9adb52a0c1e04be74c4075e188e5cde9a88ef01a275da29dff0950018ff781cf` |
| `mac-final-focused.log` | `9b372f6b5be2359e17268616090ce5de60e8951d3966a59869e0f363514b14c4` |
| `mac-vet.log` | `e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855` |
| `sandbox-host-source-check.log` | `3f3c99329dd990e876adeabd46b8f9db53d8291f5497904eeae2d7f74cdaecf8` |

The separate public continuation was verified at `2026-10-30T17:21:15.693487Z` and expires at `2026-10-30T21:21:15.693487Z`. Renewal SHA-256 `849bb83fef575925df742662044af8187a3e3e2f0a591f2acef655b2510b2631` was verified on all four hosts; original receipt/approval timestamps remained unchanged. This was inside the owner maintenance window ending `2026-10-30T22:00:00Z`. Renewal capture: `/private/tmp/rsr-prd-resume-shared-flow-20261030/renewal-result.json`, SHA-256 `6527ef337ba897cd3b6c00c89ba70852fef55d49a3c539d651b0d2a5a908c700`.

The supported old-operation closeout preserved `8474c9b8e550bec2a900dc954824e9df` and synchronized its `superseded` head `4689b91979b0d3eee80a56ec14d2ceaa7df9accefa40b55425a2f0bb7072c348` across all four hosts before any sealed successor service started. The successor was then held at head `5ecbf7b0acf682d2f6bbbc562faf47065e38b3c6779f04e6fa6c2b5be5012f40`. Proof: `/private/tmp/rsr-prd-resume-shared-flow-20261030/prepared-four-host-verification.json`, SHA-256 `dbe1c52cb5c611dd84af855d0bc996665b311e6da40a178e9400ad0ef07d579e`.

Earlier failures remain preserved rather than relabeled PASS:

- Factory SSH interruption during predecessor synchronization: `prepare.stderr`, SHA-256 `1be1bb2905a21d4fb8afd46c63cdd5dd08b5d8b5696d3a669611085f3f1a37b2`.
- Sandbox capacity failure: `prepare-20261030T172837Z.stderr`, SHA-256 `649b16717d78ce6f498e087997e270f77d36fc49e5c1f0070f8153a577a2f6be`; `462,667,776` free bytes were below the `567,108,864` minimum. Later preparation succeeded after fresh capacity checks; this record does not infer an unrecorded cleanup.
- All four selectors were switched and verified at 17:51–17:54 UTC, then initial acceptance failed at Linux direct mTLS. The helper returned no stdout; its dynamic stderr did not match the sanitizer's allowlist and was omitted. This was not an allowlisted diagnostic and not a terminal successful job result. Capture: `resume-20261030T174819Z.stderr`, SHA-256 `78ff2ff78230c4f489ea3ae1400680cec570a7860063ed6d0089351e7a026d70`. The prepared scope was unused and no runtime job was admitted by that failed readiness attempt.
- At 18:00 UTC, independent readiness found Linux TCP8443 connection refusal and Sandbox issuer verification failure; Factory and Mac were ready. Capture: `final-readiness-20261030T180012002817Z.json`, SHA-256 `7d6da32132af878f93809ab4feb3b56a16a485080734b1fa145b3cfedfa4d502`.

All capture names above are under `/private/tmp/rsr-prd-resume-shared-flow-20261030`.

## Exact owner-approved firewall and shared-CA corrections

The owner replied `Approve both fixes and continue`; the exact operation/release-bound record is `/private/tmp/rsr-prd-resume-shared-flow-20261030/operational-fixes-owner-approval.json`, SHA-256 `b00e339cfb0f05630000509f5b18bb985a273126b1a3e8b38be8ba24221d36cf`. This authorized only the two following operational corrections, with no window extension or mailbox mutation.

1. Linux firewall: persistently allow new TCP8443 connections only from Mac source `81.95.203.65/32`, preserving every other rule. Firewall backup directory: `/home/ubuntu/.cache/remote-session-runner/prd-firewall-38f-1793383527223383260`. Persistent rules SHA-256 changed from `ea96c5120a666333cb062c06f4b2190198a928a37d1bd466289b234a7260f7e1` to `009ec0e24fddab8e3d163760626de529cad492d99bb7f69cfda50e092c1eafe5`. Result: `/private/tmp/rsr-prd-resume-shared-flow-20261030/firewall-repair.stdout.json`, SHA-256 `182d0085e822c8896455a24c453d0a924074bb4e5c438ce1acadd2561ded297e`.
2. Mac PRD shared CA: retain the current replacement-Linux CA and add the verified original Sandbox/PoC CA, restoring trust lost when bootstrap replaced the shared file with a Linux-only CA. Existing private keys and configuration were preserved. Exact target: `~/Library/Application Support/RemoteSessionRunner/secrets/poc-ca.pem`; installed two-CA SHA-256 `7b26ae9f3a2527dc712bf3c4dc56b5a96433c19483fc17da1a5cb6e1a7fa12bb`, previous Linux-only SHA-256 `e9d900f7d27fe50a6cd5a1170438958fe058042860e5db5f84c68e9f0637bc27`, original PoC CA SHA-256 `40b44cbc79bf548c33b2495535302fd5ca7732465115d340379a0bf554ffd18a`. Public chain/IP proofs and original bytes are archived. The YAML configuration hash remained `2b35d897a7df65b13e77fbcdc0749da6f7df8d654d16a535a33faac5afbf2eb2`.

Applied trust record: `/private/tmp/rsr-prd-resume-shared-flow-20261030/trust-amendment-20261030T180719057291Z/trust-amendment.applied.json`, SHA-256 `a0b805e19a05506fd7edcf026d1d6cbcd5adba45f9342e83de32bd4b20e772c2`. Strict chain/hostname verification remained enabled. The 18:07:23 UTC follow-up passed all five components after these fixes; capture SHA-256 `b226203e8a296f2876f264957108a08cbb64a67439a6a29209c40422e986cd33`.

**Rollback caveat:** existing backups and the completed bootstrap journal retain their historical single-CA hashes. Any subsequent supported rollback or recovery must account for this separately approved trust amendment and must not silently discard either approved CA. Their history was not rewritten. The private closeout/amendment package is preserved durably at `~/projects-runtime/runner-release-operations/operational-amendments/prd/38f966a32a0e4bb094dcbd37d994af0b/` (24 files); its `MANIFEST.json` SHA-256 is `225626a83c542ae78ee9c4f6db1c2db16345175ff623e9d40585216141605b77`.

## Preserved backups and lane/mailbox boundaries

These are the original operation-bound, consistent PRD backups referenced by the final receipt; the replacement Linux predecessor is its explicit fresh-install baseline:

| Host | Preserved backup directory | Manifest SHA-256 |
| --- | --- | --- |
| linux-host | `/home/ubuntu/.local/share/remote-session-runner/backups/prd-catalog-20261030T151425.805945598Z` | `43a7d3442aabca5f1216fbdf3aae21f0714ec971ae742243a2ec6e4955c66ed1` |
| sandbox-host | `/home/ubuntu/.local/share/remote-session-runner/backups/prd-catalog-20261030T151431.043256663Z` | `dcd153c789f01f2d30298cd9e40dfd299dcc024a3b749718dbed55f3d5b778d4` |
| factory-sandbox | `/home/ubuntu/.local/share/remote-session-runner/backups/prd-catalog-20261030T151449.423978710Z` | `cf161c614edf70f946229a179d2bfefabbca62565b11f2cfa5537801103d5522` |
| mac-control-plane | `/Users/tomasz.walczuk/Library/Application Support/RemoteSessionRunner/backups/prd-catalog-20261030T151455.378662000Z` | `cca4f3c709aeb5c1ace4421287ef01fb18b10a01a41870a419fb35243733f6be` |

QAS routing remains unchanged. All eight returned project/default mailbox roots remain assigned to PRD, none to QAS; QAS retains its own internal default root. The postflight verifies configuration assignment only. Filesystem-mailbox acceptance was NOT RUN; no project/default mailbox request or ACK was published. Retained history, earlier uncertain intents and all prior operation records were preserved. External clients were left paused for the owner; normal-load observation starts only after their resumption.

## Delivery

Runtime deployment, eight-gate acceptance, receipt release, public branch synchronization and postflight are complete. The controller source `76f57cd` was delivered through primary Gitea, its exact GitHub mirror and all three clean worker checkouts before operational use. The final documentation commit, primary/mirror refs and every clean worker HEAD are recorded separately in the owner-only durable [documentation delivery receipt](/Users/tomasz.walczuk/projects-runtime/runner-release-operations/operational-amendments/prd/38f966a32a0e4bb094dcbd37d994af0b/documentation-delivery.json). That receipt is written after the actual push, mirror verification and fast-forwards, so it can identify this commit without rewriting its own Git history. `git diff --check` covers these documentation changes; source tests were not rerun.

The preventive shared-CA bootstrap defect is tracked separately as [BUG-028](../050-bugs/028-prd-replacement-bootstrap-overwrites-shared-ca-trust.md). Its operational mitigation is verified above; this closeout does not claim the preventive source fix is implemented.
