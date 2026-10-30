# BUG-028 — Replacement bootstrap overwrites a CA file shared by another worker

| Field | Value |
| --- | --- |
| Status | `FIXED — DEV/QAS VERIFIED; PRD PENDING` — release `rr-20261030-bug028-7043c51`; PRD unchanged |
| Severity | High — breaks authenticated access to an unchanged peer worker |
| Reported | 2026-10-30 |
| Component | `src/internal/laneinstall/prd_replacement_mac_route.go` |
| Evidence | [BUG-027 PRD closeout](../040-implementation-evidence/BUG-027-prd-closeout-2026-10-30.md) |

Linux replacement bootstrap used the configured Linux `server_ca` pathname,
which was also used by Sandbox. It replaced the original shared PoC CA with
the new Linux-only CA. YAML hashes stayed unchanged, while Sandbox direct
TLS failed with `unable to get local issuer certificate`.

The original CA bytes were verified against the consistent pre-bootstrap
backup and the completed bootstrap journal. The owner explicitly approved
an additive bundle containing only that original CA and the current Linux
CA. Both worker chains and IP identities were verified. All worker routes
subsequently passed authenticated TLS 1.3 readiness, and PRD functional
acceptance completed. No leaf certificate, private key or host key changed.
This is an operational mitigation, not a preventive source fix.

The owner has now assigned the preventive correction and deployment through
Dev then QAS only. Stop after QAS acceptance and report the immutable release
ready for PRD; the owner will stop PRD clients before separately deploying it.
Do not repeat live replacement bootstrap to test this defect. Implementation
and rollout evidence is recorded in [BUG-028 evidence](../040-implementation-evidence/BUG-028.md).

The corrective source work must detect shared trust paths before replacing
them, preserve each peer's approved trust, and use either a distinct host CA
path or a validated exact CA bundle. Changed configuration and trust must be
backed up and bound to supported transition/recovery evidence. Do not restore
the historical single-CA backup blindly: it would discard an approved CA.

Add disposable fixtures for two workers sharing a trust path, unchanged peer
validation after replacement, interruption/retry, and rollback that preserves
the approved trust amendment. Use the recorded incident as reproduction
evidence; do not repeat live bootstrap or replay project work.

The missing Linux 8443 firewall rule and the helper's opaque dynamic-error
diagnostic are separately recorded in the closeout. They are not evidence
that the diagnostic-inventory fix itself failed. Those observations do not
expand this assignment into a broader firewall or diagnostic repair campaign.

## Preventive correction and rollout result

Source `7043c5100adcc4b9da239074fff3d55addcf67fa` preserves validated shared CA trust, deduplicates the replacement root, rejects unsafe filesystem aliases and binds retry planning to the immutable original backup. Four-host native qualification and the disposable certificate/interruption regressions passed.

Release `rr-20261030-bug028-7043c51` passed all eight cohort acceptance gates in Dev and then QAS with unchanged per-host bundle hashes. Both operations completed and their admissions are open; postflight readiness, empty inventories and zero sampled storage/cleanup errors passed. The live PRD two-CA repair and all eight project mailbox assignments remain unchanged. PRD promotion of this new release and filesystem-mailbox acceptance remain NOT RUN. See [BUG-028 closeout](../040-implementation-evidence/BUG-028.md) for the exact qualification, release, operations and postflight evidence.
