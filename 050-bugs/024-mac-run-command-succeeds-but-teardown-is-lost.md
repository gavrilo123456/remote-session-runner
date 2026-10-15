# BUG-024 — Mac one-off command succeeds but job teardown is lost

## Summary

| Field | Value |
| --- | --- |
| Status | `NEW` — retained terminal teardown loss needs owner disposition |
| Severity | High — safe session/capacity release is unproven |
| Priority | High |
| Reported | 2026-10-15 |
| Discovered by | Codex during Agentic Software Factory extended feasibility |
| Owner | Remote Session Runner |
| Affected component/path | Mac one-off ephemeral session close/job reconciliation |
| Affected revision | Installed revision unknown; contemporaneous inspected source `51ec4134df6dce2021936458026caaacb2226fad` |
| Fixed revision / verification | None / pending |

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

A previous owner-route get_session lookup returned `resource_not_found: local intent not found`; it did not supply a recovery disposition. The exact physical teardown cause is unknown. No child-process, capacity leak or universal runtime failure is asserted without authoritative evidence.

Trace the exact close intent, session/job transitions, owned process identity, close/output boundaries, retained capacity and safe error projection at the installed build. Preserve the original response and correlated event prefix through cursor 7. A fresh harmless reproduction may be requested after diagnosis with new identities; do not replay this historical test body.

## Fix and verification

No fix or recovery was attempted by this report. Required gate:

1. Explain the original close failure or preserve its uncertainty explicitly, with a supported owner-scoped disposition for the exact session/command pair.
2. Prove terminal teardown success/failure transitions, exactly-once reconciliation and no body replay in source regressions.
3. Verify the installed fix through a fresh Mac one-off: command result, complete classified output, contiguous terminal cursor and closed teardown.
4. Verify proof-based capacity release through the supported interface. ACK the original only after an approved investigation/recovery disposition; never ACK it merely because later jobs pass.

## Resolution and history

Unresolved. Registered 2026-10-15. Original ACK remains **held**; no replay, deletion, direct API/SQLite access, restart, manual capacity release or state mutation was performed. Registration itself only reread existing evidence.
