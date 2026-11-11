# BUG-031 — Dev deployment closeout, 2026-11-11

**Dev deployment and verification are complete on all four hosts. QAS and PRD were not deployed.**

## Owner scope and immutable release

The owner confirmed **“Dev is unused; proceed”**; the confirmation was recorded at
`2026-11-11T07:42:27.808746+00:00`. Publisher owner is
`tomasz.walczuk`; the recorded control is
“owner confirms Dev unused; no Dev clients or publishers submitting”. This is owner evidence; the toolkit's fresh
work inventories, backups, admission fencing and selector guards remain
separate requirements. [Authorization](/private/tmp/rsr-bug031-dev-20261111-oh7weo6s/authorization.json).

| Identity | Value |
| --- | --- |
| Service release | `rr-20261111-bug031-0b7c031` |
| Sealed source | `0b7c031d7025b79e712bc52e73367a41bdefcf4e` |
| Captured primary/mirror tip | `354953f4e5928ea9510d053c9652eff61222b195` |
| Coordinator/controller revision | `354953f4e5928ea9510d053c9652eff61222b195` |
| Build | `b7a5d5685d69387e4aaf0125389f8c14` |
| Release SHA-256 | `627870fe5245493742dbfb676ae9a9d41aaf95589fdc734c7a6390e34dfcddb8` |
| Provenance SHA-256 | `b66accf3195da25366e978d4b3ba9b1248aef3d973ff38ba79721065575fae93` |
| Dev operation | `aaef98e9a1974c1ba0de0b5f8ea8dde7` |

The source build completed with CLI exit 0 and outcome
`ready_without_activation` at `2026-11-11T07:50:11.305066+00:00`.
Its durable build stage is `ready` and the build record lists all
four hosts published. This is artifact publication, not installed-lane
acceptance. [Invocation](/private/tmp/rsr-bug031-dev-20261111-oh7weo6s/native-build-command.json),
[build/release result](/private/tmp/rsr-bug031-dev-20261111-oh7weo6s/native-build.stdout.json),
[exit record](/private/tmp/rsr-bug031-dev-20261111-oh7weo6s/native-build-result.json).

| Host | Native platform | Sealed bundle SHA-256 |
| --- | --- | --- |
| `linux-host` | `linux/amd64` | `6be19a45e39b5eca80898dfdacca9ecffb9eec5a247111bfe44eb5ee951b9262` |
| `sandbox-host` | `linux/arm64` | `4f3f47306d708f437dd6e33927156e65edaeb26b046c6fda3f9b6779c667d5b0` |
| `factory-sandbox` | `linux/amd64` | `4506cecf15c22fff04521eba0056dff2c1c2975ee410c71f28b5842d86743cb9` |
| `mac-control-plane` | `darwin/arm64` | `97e93588e2ea7f82c1145d2c9f66fbfb0ccf3e6d9f777799a696347c6529b10e` |

The catalog builder recorded `-trimpath`, `-buildvcs=false` and the exact
source-revision linker injection in every bundle. Its Linux service bundles
use **CGO0**, while the original native Linux source tests used **CGO1**;
Mac native tests and bundle use **CGO1**. This is the established catalog build
policy, not a claim that different build settings are identical. Source
receipts bind their actual host/platform/toolchain and original evidence;
the immutable build provenance records the actual bundle flags and CGO
settings. No candidate was rebuilt during promotion.

## Native source qualification and receipts

The four receipts bind passing native source test/vet/build/
smoke records at source `0b7c031d7025b79e712bc52e73367a41bdefcf4e` with Go1.27.1.
The latest Factory full retry passed; its first full-suite failure remains
**NOT PASS**, separately preserved in the source evidence. No new source
suite was run to generate these four owner-only `native-full-v1` receipts.

| Host | Canonical source-test receipt SHA-256 | Original successful test timestamp |
| --- | --- | --- |
| `linux-host` | `8902952ca9037e68da249ebed58e0905777bd4b9c8ef3e6fa743ff02714ee4f1` | `2026-11-11T07:33:39.089824Z` |
| `sandbox-host` | `5483653f5a270e3b78ec2bece676dff282d2d861a3f78ee8ed5ad53e4ee1d945` | `2026-11-11T07:33:53.483404Z` |
| `factory-sandbox` | `4eb3d86fbce791624cd6681bfdfed5c8c99f8421cbea9f1460897be3502ffdfb` | `2026-11-11T07:34:50.063949Z` |
| `mac-control-plane` | `08762f6b946d81f0cd7489ceded0b9d8313d90e9f902610a8e53924311e8e3b1` | `2026-11-11T07:18:41.561669Z` |

[Receipt index](/private/tmp/rsr-bug031-dev-20261111-oh7weo6s/native-receipts.json) and the four private receipt evidence
envelopes bind the raw logs, source, actual platform, toolchain binary hash,
settings and timestamps. The build verified those toolchain/platform bindings
against the native bundle settings. Current pinned Go file metadata predates
testing; [toolchain read observations](/private/tmp/rsr-bug031-dev-20261111-oh7weo6s/go-toolchain-readonly.json) preserve
all four hashes and file identities. [Source verification](/private/tmp/rsr-bug031-source-20261111-zbgjp2lx/source-verification.json).

The local coordinator executables embed the full delivered controller
revision and have measured executable hashes/version output in
[controller attestation](/private/tmp/rsr-bug031-dev-20261111-oh7weo6s/controller-attestation.json). The canonical
checkouts remained clean during helper execution. Only documentation was
changed after runtime verification completed.

## Linux source-fetch metadata restoration

Linux's origin was still an HTTPS URL after source delivery, despite its
restored required SSH identity. Because native `host-build` fetches origin
using the pinned SSH command, that host-local Git
remote was restored to the same canonical SSH GitHub mirror. The exact old URL is preserved
mode0600; strict pinned SSH proved exact `354953f` before and after the change.
HEAD and the clean worktree did not change. No key, host profile, tracked file,
service or runtime configuration was changed by this metadata correction.
[Before record](/private/tmp/rsr-bug031-dev-20261111-oh7weo6s/linux-origin-before-normalization.json),
[normalization proof](/private/tmp/rsr-bug031-dev-20261111-oh7weo6s/linux-origin-normalization.json).

## Network-path diagnosis — no firewall changes

The initial default agent execution context used source
**31.61.253.252** and failed to reach Linux/Factory Dev direct routes. The
owner subsequently approved adding only that `/32` to TCP8444 on those two
hosts, preserving existing rules, PRD8443, QAS8445, IPv6 and cloud firewall.

The prepared apply driver then observed actual escalated SSH source
**81.95.203.65**, different from the exact approved address, and **refused
before any write**. No firewall update was performed. Prepared rules were
not applied and must not be described as a repair or a successful write.
The actual native deployment context already uses the existing allowed
81.95.203.65 path. In that context all three worker Dev readiness reads passed
HTTP200/TLS1.3 with configured CA, client certificate and hostname/pin
verification enabled. Those pre-activation reads do not count as acceptance
of the new release. The initial default-context failure remains preserved,
separate from the native-context success.

[Exact owner approval](/private/tmp/rsr-bug031-dev-20261111-oh7weo6s/firewall-owner-approval.json),
[source observation](/private/tmp/rsr-bug031-dev-20261111-oh7weo6s/current-egress-after-approval.json),
[resolution](/private/tmp/rsr-bug031-dev-20261111-oh7weo6s/firewall-resolution.json),
[native-context readiness](/private/tmp/rsr-bug031-dev-20261111-oh7weo6s/network-native-preactivation/readiness.json).

## Initial read-only Dev plan

The initial plan observed all four Dev selectors on predecessor
`rr-20261108-bug029-1263205`, existing active services, schema40 and empty
restart work inventories. Capacity was measured against the separate
500,000,000-byte operating buffer:

| Host | Initial schema | Work inventory empty | Available bytes at plan cut | Required bytes |
| --- | --- | --- | ---: | ---: |
| `linux-host` | 40 | true | 35855388672 | 500000000 |
| `sandbox-host` | 40 | true | 3145334784 | 500000000 |
| `factory-sandbox` | 40 | true | 33660469248 | 500000000 |
| `mac-control-plane` | 40 | true | 43701690368 | 500000000 |

These are initial planning snapshots, not final capacity or backup proofs.
The plan's `backup_verified=false`, `selector_journal_verified=false` and
`can_switch=false` correctly reflect that a read-only plan did not create
operational backups/journals. Fresh write-time preflights remain the toolkit's
responsibility. [Plan](/private/tmp/rsr-bug031-dev-20261111-oh7weo6s/dev-plan.stdout.json).

## Completed promotion and acceptance

The promotion CLI exited **0** at `2026-11-11T08:31:38.931740+00:00`; its
outcome is `completed`. Durable operation `aaef98e9a1974c1ba0de0b5f8ea8dde7`
is **`complete`**, last updated `2026-11-11T08:31:38.903836Z`, with
branch synchronization `not_applicable` (expected for Dev). No rollback
was recorded. CLI wall time from invocation to exit was **1153.572 seconds**
(**19 minutes 13.572 seconds**).

The release-bound `catalog-functional-v1` acceptance passed at
`2026-11-11T08:28:18.777258Z`. All eight gates passed: capacity, direct mTLS,
isolation, lifecycle, limits, Mac-local, queued SSH and teardown. Its evidence
SHA-256 is `7f038181a7b35c02499453859b1dc7a9238f4d0a6161ae0efde1a55d7c61b01a`; the canonical
[acceptance file](/Users/tomasz.walczuk/projects-runtime/runner-release-operations/acceptances/dev/rr-20261111-bug031-0b7c031.json) SHA-256 is
`28b81d4df6d38d5b8ea3816832f5ed3fcda071b3e115eaff61b228435e781402`. The acceptance binds the exact release digest
and all four native bundle hashes already listed above.

| Host | Selector-step intent UTC | Selector-step verified UTC | Seconds |
| --- | --- | --- | ---: |
| `linux-host` | `2026-11-11T08:17:14.46292Z` | `2026-11-11T08:18:02.227027Z` | 47.764 |
| `sandbox-host` | `2026-11-11T08:18:11.23641Z` | `2026-11-11T08:18:58.281797Z` | 47.045 |
| `factory-sandbox` | `2026-11-11T08:19:07.930257Z` | `2026-11-11T08:20:11.422351Z` | 63.492 |
| `mac-control-plane` | `2026-11-11T08:20:14.417538Z` | `2026-11-11T08:20:47.043176Z` | 32.626 |

These per-host durations cover the toolkit's selector-step intent through
verified result. They exclude preparation, inter-host synchronization and
whole-cohort functional acceptance, and must not be presented as total host
outage or complete deployment duration.

The complete ordered event history remains in the canonical operation and
linked snapshot below.

[Promotion invocation](/private/tmp/rsr-bug031-dev-20261111-oh7weo6s/dev-promotion-command.json),
[CLI result](/private/tmp/rsr-bug031-dev-20261111-oh7weo6s/dev-promotion-result.json),
[CLI stdout](/private/tmp/rsr-bug031-dev-20261111-oh7weo6s/dev-promotion.stdout.json).
The durable journal was read locally without RPC or lock at `2026-11-11T08:34:00.691457+00:00`.
Its exact bytes are preserved in [snapshot](/private/tmp/rsr-bug031-dev-20261111-oh7weo6s/dev-operation-closeout-draft-snapshot.json),
SHA-256 `0d57b2070b747d26a1b19a51558085303e17fc849b371c1a974dc657b01a39c1`. Independent four-host released-receipt and runtime/peer postflight evidence
is recorded below; it is not inferred from this journal alone.

## Released cohort and final read-only postflight

Four independent supported receipt captures are byte-identical, each with
SHA-256 `01e4f112e11491e643f290daf5912da22576d1e77069817ec49d6f7f2e0b727a`. They report stage **`released`**, **16 transitions**,
operation `aaef98e9a1974c1ba0de0b5f8ea8dde7` and the exact sealed release.
See [capture manifest](/private/tmp/rsr-bug031-dev-20261111-oh7weo6s/released-cohort-receipt-captures.json).

The four-host postflight issued at `2026-11-11T08:36:08.470112+00:00` is **PASS**:
all Dev services and Mac local executor are active/ready, select the exact
new release and native bundle, report source `0b7c031`, remain on schema40,
and retain their pre-rollout configuration hashes. Complete execution
inventories report zero sessions, commands, jobs, reservations, command slots,
retained pairs and recovery finalizations.

| Host | Dev readiness | Schema | Active execution/capacity counts | Root/catalog-volume free bytes | Configuration |
| --- | --- | --- | --- | ---: | --- |
| `linux-host` | ready | 40 | 0 | 35853492224 | unchanged |
| `sandbox-host` | ready | 40 | 0 | 2997514240 | unchanged |
| `factory-sandbox` | ready | 40 | 0 | 33645948928 | unchanged |
| `mac-control-plane` | ready | 40 | 0 | 41627156480 | unchanged |

All capacity readings exceed the separate 500,000,000-byte operating buffer.
These are dated postflight readings, not promises about later free space.
The detailed diagnostic retains local delivery-intent records and correctly
reports `safe_to_resume=not_established`, `recovery_authorized=false`;
zero execution counts do not authorize recovery or establish every delivery
intent's terminal outcome.

Peer QAS/PRD selector targets, configuration hashes, reported builds and service
identity/readiness comparisons match the baseline; those lanes remain on
`rr-20261108-bug029-1263205` / source `1263205` and were not deployed.
The comparison retains two precise limits: the original Mac baseline captured
launchctl PID/runs but no kernel process-start identity, so exact prior-start
identity equality is not asserted (current start times precede the baseline);
and Sandbox PRD's previously incomplete detailed-work export remains a
preserved gap, never interpreted as empty or repaired. Peer isolation is not
a claim that PRD retained work has been settled.

[Postflight summary](/private/tmp/rsr-bug031-dev-20261111-oh7weo6s/postflight-readonly/postflight-summary.json) SHA-256
`a390db1af37ead70da3e5a40fb8e2acb4f7afd5b902d344f877327782c2ced96`.

After the final receipt-sync restart and the extra regression below, all three
Dev worker direct endpoints again returned HTTP200/ready/source `0b7c031`
over TLS1.3 with configured CA and client material, hostname verification,
and certificate/SPKI/URI pins enabled. No functional job was submitted by
these readiness GETs, and **no firewall changes were made**.
[Strict native-context readiness](/private/tmp/rsr-bug031-dev-20261111-oh7weo6s/network-native-postflight/readiness.json) SHA-256
`53b78dbfc6d900f7cfead691e23072dfbf7f256f3ba954a8957c5eadc3c21a8f`.

## Additional ordinary failed-exit2 regression — PASS

Exactly one fresh owner-local Dev API job was submitted after canonical
promotion completion, all eight passing gates and four released-receipt
captures. It used source mode `empty`, printed the exact marker
`BUG031_DEV_FAILED_EXIT2` with its newline, and exited2. It performed no
Factory action, historical replay, mailbox publication or ACK.

- Job: `job-d68df947f68021b84f7b503ddd51bd5e`.
- Session: `sess-1723804976555b11982be7122e38d77f`.
- Command: `cmd-a2a65fcb5ea65d63ab9affa3e36703e6`.
- Preserved command outcome: **failed, exit2**; job phase **complete**.
- Finite event cursor: **4**, ending in `command_failed`; complete,
  untruncated exact output and no stderr.
- Teardown: **closed** with `runtime_closed`; post-probe execution/capacity
  inventory empty.
- Ingress and executor storage/cleanup-failure counters remained0; active
  session/command slots, queued commands/intents and mailbox backlog remained0.
- Fresh submission attempts: **1**; mailbox requests **0**, ACKs **0**.

[Probe result](/private/tmp/rsr-bug031-dev-20261111-oh7weo6s/ordinary-dev-exit2-probe/result.json) SHA-256
`2bf4e64688640e1cc7e1e89a148de43e97e3201cffbb694ecc3b3ee70b2e1c95`; exact job/command/session/event and
before/after inventory/health captures remain in that private directory.
This is an ordinary failed-command cleanup regression, not live injected-zombie
mechanism acceptance. Live zombie injection remains **NOT RUN**; the native
disposable reproduction supplies the mechanism-specific evidence.

## Delivery and remaining scope

The Dev runtime deployment, all eight cohort gates, released receipt captures,
strict TLS readiness, peer comparisons and additional ordinary exit2 regression
are complete. **Filesystem-mailbox acceptance remains NOT RUN**; this task
published no mailbox requests or ACKs. QAS/PRD deployment was not assigned.

The final evidence-only Git delivery is recorded in the private
[delivery manifest](/private/tmp/rsr-bug031-dev-20261111-oh7weo6s/dev-closeout-git-delivery.json):
Mac commit, primary Gitea, exact automatic GitHub mirror and all three clean
Ubuntu fast-forwards. This documentation update does not change the sealed
service bytes or require another source-suite run.

The native disposable zombie reproduction proves the corrected mechanism; it
does not retrospectively establish the physical trigger of historical R100 or
R102. No historical failed qualification was replayed, relabelled successful or
ACKed by this rollout. Factory's reservations, eligibility and independent
administrative mechanisms remain outside this Runner Dev deployment.
