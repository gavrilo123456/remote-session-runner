# gpu-services — completed Sandbox mailbox route switch

## Scope and result

The owner authorized changing only the existing `gpu-services` Mac PRD mailbox
default to Sandbox and confirmed current PRD clients/publishers were paused.
The route change and fresh mailbox/SSH acceptance completed on 2026-11-25.
**Team A may resume fresh requests.** Clients were not resumed by maintenance.

| Setting | Verified value |
| --- | --- |
| Authority | Unsuffixed Mac PRD, `mac-control-plane`, UID502 |
| Mailbox ID / repository alias | `gpu-services` / `gpu-services` |
| Preserved root | `/Users/tomasz.walczuk/projects/gpu-services/runner/mailbox-` |
| New default | `ubuntu-sandbox` → `sandbox-dev` → `remote/sandbox-host` |
| Allowed contexts | `ubuntu-sandbox`, `mac-local` |
| Observed executor | `ubuntu`, `oracle-gustaw-janecki-ubuntu-flex-02`, `aarch64` |
| API alias / observed API host | `gpu-services-api-runtime` / `oracle-bazyl-szama-ubuntu-flex-02` |

The existing `ubuntu-current` context and `linux-host` profile remain unchanged
globally for historical work and other mailboxes. New explicit old-worker
overrides are no longer allowed for this inbox. `mac-local` Git is preserved.
The mailbox root and all five stream-directory identities/modes are unchanged.

## Preparation and ingress-only activation

Only `mailboxes.gpu-services.default_execution` and `allowed_execution`
changed. Installed directory/retained-mailbox candidate validation passed.

| Identity | SHA256 |
| --- | --- |
| Original config | `22f16457147c3d8f0c3e4b2781edc04f4caeca7374d5fcde6d5cb59151d5cd1a` |
| Activated config | `44d49258c380faa61de131bd4b704ca75414c9b07f13f4903949b5e904dbdbdc` |
| Consistent backup manifest | `45125a80f21155d7d726cdf989dc19733201dab13d9cd28fde66d07b76cc0b53` |
| Database backup | `a796c51bd47193111d03108c267349c8a253fb95c7cc9b032bc9a6da841531a0` |

Backup: Mac PRD `backups/prd-catalog-20261125T064944.447853000Z`, schema 40,
14,526,029,824 bytes available afterward. Fresh supported work/admission and
retained-mailbox checks preceded the change. No ownership handoff was performed;
historical uncertain intents were preserved.

The selected service remains `rr-20261119-bug034035-7b11dbc`, source
`7b11dbc08ad09eb5783c8ab3e5b33917ad2901d6`, Mac bundle
`fdf83edc3b1c9ce8276116199b144505c34bb0a5228e87fa5781f8542df8fe57`.
This was configuration maintenance, not a service-release promotion or rebuild.

Only `gui/502/com.remote-session-runner.local` was stopped: bootout at
**06:51:32 UTC**, stopped-process/socket proof at **06:51:45**, and ready
again by **06:51:51** after atomic mode0600 candidate handoff and the existing
ingress LaunchAgent bootstrap. Executor incarnations PID1844/PID1859 stayed
unchanged. Worker services, peer-lane configurations/selectors, keys, global
profiles and all other mailbox stanzas were untouched. Released cohort
history was preserved; no new cohort operation or receipt editing was needed.

The native operator SHA256 was
`1683e218367be71202d5f4396474380ed130c7294c31f06c57a901969999daff`.
It was rebuilt with Go1.27.1 and `c0ec516` attestation; its Go files are identical
to qualified `0bb2e4f`. Missing shared module-cache files were restored only
from H1/go.sum-verified archives, then verified; no shared cache was deleted.
Compilation used `GODEBUG=goindex=0`. No source change or new test-suite result
is claimed from that compilation. An initial candidate-preparation indentation
error had no candidate-file or runtime effect. A final restricted-context
process recheck was denied; its approved native-context repeat passed.
Initial failures remain distinguished from the completed checks.

## Original Linux request preserved and delivered

- Request: `req-gpu-t7-f01-api-process-check-20261125-astra01`.
- Command: `cmd-0fc58f2d7f9ad4b784fbc77ed8cc5f0d`.
- Session: `sess-55d7ea764ab481d492736e8ab0b7dd87`.
- Job: `job-a8fba71946bd7f48199ab9768fcdeca3`.

The old Linux worker's supported GET returned complete/succeeded, exit0,
cursor4, complete output and CLOSED teardown while Mac still projected
accepted/revision2. After the ingress refresh, normal GET-only reconciliation
delivered revision3 with the **same IDs and original `remote/linux-host`
target**, complete/untruncated output, exit0/cursor4/CLOSED. It remains unACKed.
No replay, replacement, cancellation, recovery or capacity release occurred.

## Fresh Sandbox acceptance

- Test request: `req-gpu-services-sandbox-route-20261125-23a01b248e`.
- Command: `cmd-f7c58ca5ff68066cad5fdaa720a3f845`.
- Session: `sess-fe7d59d8199461a9e709e924b1e42205`.
- Job: `job-d280cd838c03b3ee1202229ee91f7b91`.

The fresh request omitted both selection fields. Its response proved
`inbox_default`, `sandbox-dev`, `remote/sandbox-host`, the expected Sandbox
hostname/user/architecture, and authenticated SSH through the existing
`gpu-services-api-runtime` alias to the API hostname above. BatchMode,
IdentitiesOnly and strict existing host-key verification were used; alias,
keys and pins were unchanged. This proves SSH connectivity, not API application
readiness, source synchronization, or project test results.

Terminal revision2/cursor6 passed with exit0, complete/untruncated output and
CLOSED teardown. The exact response and contiguous event prefix were preserved;
only this maintenance probe was ACKed. An initial 12-second ACK observation
ended before consumption; a later GET confirmed `terminal_acknowledged`,
without republishing the ACK. Final verification at **06:57:06 UTC** found
Mac active work empty. No historical exchange was ACKed.

Project `AGENTS.md` and the two mailbox reference documents were updated with
guarded original-byte hashes for the new route while retaining Git-only source
delivery and Mac-local Git. Project `AGENTS.md` is left uncommitted for Team A's
normal reviewed workflow; maintenance performed no `gpu-services` Git mutation.

## Evidence and delivery

Raw task records: `/private/tmp/rsr-gpu-sandbox-switch-20261125-wat628xz/`,
including `MAINTENANCE.md`, `PREPARED.json`, `ACTIVATED.json` and
`FINAL-VERIFIED.json` (SHA256
`60689d3cdfe480d224c273735f401f8af14af7d5876efe6de5ef937e1597aaa9`).
The activation record's earlier “fresh execution NOT RUN YET” remains its
historical checkpoint; the final record supplies actual acceptance.

Durable private package:
`~/projects-runtime/runner-release-operations/maintenance/gpu-services-sandbox-route-20261125-23a01b248e/`.
Its 92-file / 24,323,842-byte `ARCHIVE-MANIFEST.json` has SHA256
`b99474aa667b49a99e6eb02ba542d4af259108666ba1c4fc3414590d5ebf43be`.
It preserves backup/configuration, process/directory/peer proofs, old/new
responses/events, ACK proof, operator/cache records and documentation copies.
`FINAL-GIT-DELIVERY.json` records the subsequent Runner documentation commit,
primary/mirror equality and clean Ubuntu fast-forwards. No Go source changed;
documentation delivery does not activate or restart services.
