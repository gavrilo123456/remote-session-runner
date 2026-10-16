# BUG-024 — Mac one-off command succeeds but job teardown is lost

## Summary

| Field | Value |
| --- | --- |
| Status | `RESOLVED` — approved original disposition and fresh installed mailbox acceptance passed |
| Severity | High — lost teardown can retain session capacity |
| Priority | High |
| Reported | 2026-10-15 |
| Discovered by | Codex during Agentic Software Factory extended feasibility |
| Owner | Remote Session Runner |
| Affected component/path | Mac one-off ephemeral session close/job reconciliation |
| Affected revision | Incident-time installed revision unknown; contemporaneous inspected source `51ec4134df6dce2021936458026caaacb2226fad`; retained state verified on installed Mac `b71aaef0859bd0266c343e638e62e268999a388b` on 2026-10-16 |
| Fixed revision / verification | Source `ca79ddea9a9010cdbf083710653a8db6dae1e600`; installed Mac `77910358a149d6e8fcdf4694498ebdf6c1f07f58`, verified 2026-10-16 |

## Reported and expected behavior

A local controller-test/version request completed its command with exit zero and complete untruncated output, but the terminal response retained `job_phase=lost` and `teardown_outcome=lost`. Command success does not establish that its ephemeral session closed or capacity was safely released.

A successful one-off should close its owned session with verifiable disposition. If teardown cannot be proved, preserve the truthful lost result and provide an owner-scoped diagnostic/recovery path without replaying the body. Do not rewrite a conservative lost result as success without evidence.

## Exact incident evidence

Client/execution machine: Mac, client account tomasz.walczuk. Inbox `agentic-software-factory-runner-direct`, root `/Users/tomasz.walczuk/projects/agentic-software-factory/runner/mailbox-`, alias `agentic-software-factory`. Explicit target `mac-dev` / `local,mac-workstation`.

| Identity / result | Value |
| --- | --- |
| Request | `req-asf-ext-20261015-harness-01` |
| Key | `asf-ext-20261015-harness-01` |
| Job | `job-f0917cc13fe809c27d626401a952f5ed` |
| Session | `sess-5b9be3c1c9186dc2a78edee42238927b` |
| Command | `cmd-bd164c1d3a600569a58baddfe9336ed4` |
| Response observation | `2026-10-15T16:24:33.434794Z` |
| Request / command | complete / succeeded, exit 0 |
| Job / teardown | lost / lost |
| Revision / final and available cursor | 3 / 7 / 7 |
| Output | complete=true, truncated=false |
| Payload result | 13 controller unit tests passed; Cursor version 2026.10.01-e373342 |

The [first-round ledger](/Users/tomasz.walczuk/projects/agentic-software-factory/runner/evidence/2026-10-15-extended-provider-run/README.md) and [Runner index](/Users/tomasz.walczuk/projects/agentic-software-factory/runner/evidence/2026-10-15-extended-provider-run/runner-index.json) retain its identity and HELD_UNRESOLVED ACK state. The retained [outbox response](/Users/tomasz.walczuk/projects/agentic-software-factory/runner/mailbox-/outbox/req-asf-ext-20261015-harness-01.json) was reread during registration and still had the above terminal identity/revision. This managed file is not permanent evidence; use the retained ledger/index as the durable public pointer.

## Scope and investigation

This differs from BUG-019/020's originally running command: here command success and output are present, while ephemeral teardown is lost. Their historical resolution is not proof this later request recovered. Subsequent fresh jobs closed normally; they establish neither recovery nor absence of retained capacity for this session.

A previous owner-route get_session lookup returned `resource_not_found: local intent not found`; it did not supply a recovery disposition. The exact original physical teardown cause remains unknown. The later guarded investigation confirmed the exact retained session reservation and matching owned runtime marker, with no process/group 80710 present. This establishes retained capacity without asserting an original child-process failure or universal runtime defect.

Source inspection reproduced a cleanup observation race: a redirected descendant can briefly remain after Bash reap and complete command output. Cleanup now permits a bounded 500 ms observation period. A persistent group, inspection error or cancellation still retains evidence/capacity. This source regression does not prove the historical incident had that cause.

## Fix and verification

Registration did not attempt recovery. Repair and acceptance are tracked in
[`040-implementation-evidence/BUG-024.md`](../040-implementation-evidence/BUG-024.md).
Completed gates:

1. **PASS:** original physical cause remains explicitly unknown; approved installer-mediated recovery proved cleanup for the exact owner/session/command and released only its retained session reservation.
2. **PASS:** source regressions cover success, retained uncertainty, whole-set proof, atomic rollback, crash/finalization retry, GC protection, immutable outcomes/events and no body replay. Full suites and vet pass on Mac and all three Ubuntu hosts; race checks pass on Mac, primary AMD64 Linux and ARM64 sandbox. The extra factory race check is unavailable without a C compiler.
3. **PASS:** fresh native Mac request `req-bug024-20261016-mac-01` succeeded with exit 0, complete untruncated output, cursor 6 and closed teardown; its archived response/events and consumed ACK are recorded in the evidence.
4. **PASS:** the guarded installer reported `recovered_teardown_pairs=1`, finalized the original owned marker/workspace, and passed its zero-capacity postflight. Original response/event hashes are unchanged. The original ACK remains held as explicitly approved.

## Resolution and history

Registered 2026-10-15. On 2026-10-16 the user authorized fixing this blocker
before activating the repository mailbox. The exact original physical cleanup
failure remains unknown. The source repair adds a bounded Mac process-group
disappearance check and a separate installer-mediated successful-command /
lost-teardown recovery route. This route preserves the historical successful
command and lost job, and releases only the session reservation after exact
ownership proof. It does not make the old lost response successful.

Approved live recovery and mailbox activation passed on 2026-10-16. Both Mac
services are ready at installed revision `77910358a149d6e8fcdf4694498ebdf6c1f07f58`.
The repository mailbox is active directly at
`/Users/tomasz.walczuk/projects/remote-session-runner/runner/mailbox-`, with all
four fresh Mac/remote route checks passed and ACKed. Final local capacity,
queues and resumable work are zero. The original successful command and lost
historical job remain unchanged; its original ACK is intentionally **held**.
No historical script was replayed, no direct database or private API mutation
was used, and no forced restart was needed.
