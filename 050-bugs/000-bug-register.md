# Bug register

This register records confirmed or investigated defects in Remote Session
Runner. It is the index for bug records; implementation evidence remains in
`040-implementation-evidence/`.

## Status values

| Status | Meaning |
| --- | --- |
| `NEW` | Reported but not yet triaged. |
| `TRIAGED` | Scope, impact, and reproduction evidence are understood. |
| `IN PROGRESS` | A corrective change is being prepared. |
| `BLOCKED` | Progress needs an external decision, host change, or missing evidence. |
| `FIXED — PENDING VERIFICATION` | A candidate fix exists and needs its stated verification gate. |
| `CLOSED` | The fix and required verification evidence passed. |
| `NOT A BUG` | Investigation found expected behavior. |
| `DUPLICATE` | The record is tracked by another bug ID. |

## Open and tracked defects

| ID | Title | Status | Severity | Affected area | Affected revision | Next action | Updated | Record |
| --- | --- | --- | --- | --- | --- | --- | --- | --- |
| BUG-002 | Accepted remote run may remain accepted after an unverified terminal boundary | `IN PROGRESS` | High | Linux lost-result persistence and workspace mailbox reconciliation for `sandbox-host` | Mac `b2b4d6398985059bb5c4341b03fd9d10b3ba6450`; sandbox SHA to be confirmed at deployment | Complete automated checks, deploy through Git, then verify the existing request without replaying it | 2026-09-30 | [BUG-002](002-mailbox-accepted-without-remote-session-allocation.md) |

## Sample format

| ID | Title | Status | Severity | Affected area | Affected revision | Next action | Updated | Record |
| --- | --- | --- | --- | --- | --- | --- | --- |
| BUG-001 | Sample record only — not a reported defect | `NOT A BUG` | N/A | N/A | N/A | None | N/A | [sample bug](001-sample-bug.md) |

## Adding a bug

1. Copy `001-sample-bug.md` to the next numbered bug file and replace every
   sample value with evidence from the observed failure.
2. Add one row here with the new ID, current status, affected area, next
   action, update date, and link.
3. Record exact reproduction commands, host/account, source revision, and
   safe error evidence. Do not include private keys, certificate material, or
   secret values.
4. Link the correction commit and verification evidence before changing the
   status to `CLOSED`.

Do not use a passing test, a transport probe, or a configuration review as
proof that a reported runtime defect is fixed. Record the required live or
host gate in the bug record.
