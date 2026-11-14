# BUG-032 — Detailed retained-work failures omit their safe diagnostic boundary

## Summary

| Field | Value |
| --- | --- |
| Status | `FIXED — NATIVE/OPERATOR VERIFIED` — historical failures remain unclassified |
| Severity | Medium — obstructs supported recovery diagnosis |
| Priority | High for the exact R168 investigation |
| Reported | 2026-11-14 |
| Discovered by | Factory R168/R169 handoff and Runner maintainer review |
| Owner | Remote Session Runner |
| Affected component/path | `runner-lane catalog-work-inventory --details`, selected-authority read-only diagnostics |
| Affected revision | Installed standalone diagnostic `883e2af0373b7de94f172df31909434f9a23c38d`; source inspected at `b07e199ef85b27c027ef8e2fbb728a4d409347b1` |
| Fixed revision | `cedf7100bcad20f84ab1e085f686450ce15b7720` |
| Verification | Changed-package tests, vet and native builds passed on Mac and all three Ubuntu hosts; installed operator returned a complete schema-40 inventory in 4.425 seconds. Historical failure cause is still unproven; no service release or recovery was performed. |

## Reported behavior

Two Factory reads of the supported detailed PRD inventory failed with only
`retained_work_inventory_unavailable`. The reader and CLI deliberately erased
the failed boundary along with the private underlying error. Operators could
not distinguish a deadline, metadata bound, schema problem, filesystem guard
or query failure without speculation or an unsafe diagnostic bypass.

The original failures remain evidence. Their roughly eleven-second durations
are consistent with the ten-second read budget but do not prove timeout.
A direct maintainer read on 2026-11-14 succeeded in about 2.3 seconds with the
same installed diagnostic, schema 40 and a complete approximately 2.45 MB report.
No historical cause is inferred from that later success.

## Expected behavior

Keep raw errors and incomplete rows private, while returning an allowlisted
failure stage/category and monotonic elapsed timing. Refusal must still exit
nonzero with `inventory_complete=false`, `recovery_authorized=false` and
`safe_to_resume=not_established`. Generic/unrecognized errors must remain
explicitly unknown. Do not extend the read deadline or loosen identity guards.

## Reproduction evidence

Machine/account: Mac `mac-control-plane`, `tomasz.walczuk` / UID 502.

```sh
# Mac — supported owner-only read; no recovery, replay or ACK
"$HOME/Library/Application Support/RemoteSessionRunner/bin/runner-lane" \
  catalog-work-inventory --host mac-control-plane --lane prd --details
```

The recorded failures occurred during investigation of command
`cmd-877df533a8e0a90b0b8c38fa4bc740a8`, session
`sess-e5d66620e61ddaa66f92968828390803`. The command's truthful failed exit 1,
revision 4/event 8 and LOST teardown are separate facts; that retained state
is already supported by the inventory's `completed_teardown` classification.

## Impact and scope

This is a diagnostic operability defect, not proof of database corruption,
inventory incompleteness on every read, or a recurrence of the zombie-only
cleanup defect. It neither authorizes release of this reservation nor changes
the installed whole-set recovery contract. See
[the R168 investigation](../040-implementation-evidence/R168-runner-inventory-and-teardown-2026-11-14.md).

## Fix and verification

The reader classifies failures into fixed stages/categories before discarding
their raw causes. The CLI exports only recognized safe metadata and never
publishes rows from a refused cut. Existing error-sentinel compatibility and
all read-only, schema, WAL/SHM, selector, configuration and completeness checks
are preserved. Tests cover private-error and partial-row suppression,
deadline/cancellation, size limits, schema/history, sidecar refusal and elapsed
metadata. Native qualification and installed diagnostic results are recorded
separately from the original exchange's unresolved recovery.

The qualified standalone operator was installed owner-only at the documented
Mac path, preserving its exact prior executable. Its SHA-256 is
`3f151e1f59cd89cd63298d3a3933f966e7c8e01f1eddecf98b4ce1a96358e4c8`.
The selected service release and configuration remained unchanged. Live
acceptance covered a complete successful read; refusal stage/category and
redaction were proven on disposable fixtures. No naturally failing live read
was observed, and no production guard was deliberately broken to manufacture one.

## History

| Date | Change |
| --- | --- |
| 2026-11-14 | Registered from the exact R168 inventory failure handoff; added safe bounded diagnostic fields without changing recovery or service bytes. |
