# LANES-L0 — full read-only inventory

**State:** NOT STARTED — blocked by `LANES-PREP-00`.

## Authorization scope

The owner authorized direct read-only Mac shell and SSH observations across
`mac-control-plane`, `linux-host`, `sandbox-host`, and `factory-sandbox` for
`LANES-PREP-00` and `LANES-L0`, using the configured explicit SSH identities
and existing access. Neither unit may publish Runner requests or ACKs, run
defect probes, or change installed configuration, services, storage, or
firewall rules. Documentation/evidence edits and their Git delivery are
separately authorized mutations under `AGENTS.md`; they are not read-only host
observations. At the time of this authorization, other execution used the
normal mailbox workflow. Declared operational approval checkpoints remain
mandatory, and the unsuffixed production Runner stays protected.

The owner later authorized incremental implementation: use Runner where it can
execute a required command, and use ordinary SSH on all three Ubuntu hosts for
implementation builds, tests and setup when Runner cannot. Use the configured
explicit identities and verified host keys; record the fallback reason, host,
source revision, command and result in the unit evidence. This does not expand
the read-only observation route for PREP/L0, start L0, change production, or
waive any failed gate or operational approval. The unsuffixed production
Runner, retained work, and held ACKs remain protected.

`LANES-PREP-00` did not pass its access/readiness gate. Therefore this unit's
full inventory, unsigned policy-equation evaluation, active/uncertain-work
classification, ancillary descriptor inventory, and green exit gate have not
been run. Some observations that overlap L0 were captured as PREP discovery;
they are listed in `LANES-PREP-00.md` and must not be represented as L0 PASS or
as a substitute for a fresh complete L0 after its predecessor is unblocked.

No Runner request or ACK, defect probe, configuration/service/storage/firewall
change, or separate live-change approval was made. The direct-observation route
authorization is recorded above. The four fresh BUG-024 verification ACKs
remain recorded as consumed; retained response files do not require another
ACK. The historical `req-asf-ext-20261015-harness-01` response and held ACK
remain preserved.

## Delivery and sequence record

This blocked unit record was included in source S
`44241e8199df7e3882b34194e10d23cbd9e1c468`. S was pushed to primary Gitea,
the exact GitHub mirror SHA was verified before host pulls, and all three
clean Ubuntu `dev` checkouts fast-forwarded to S. These were separately
authorized Git source-delivery mutations under `AGENTS.md`, not read-only host
observations. See `LANES-PREP-00.md` for the per-host checks and delivery
commands. No L0 inventory was started. L0 remains blocked until the PREP
blockers are corrected and its fresh full inventory passes. The later owner
decision permits only independent hermetic/source-only units to proceed while
an unrelated blocker remains; those units must record the exact blocked-gate
evidence and may not claim L0 or another dependent gate passed.

## Owner-decision documentation Source S delivery

Source S `d797d8519fcee0fbf40f0588f2e7393f6b96f74b`
(`docs(LANES): align incremental readiness and L0 authorization`) aligned
`AGENTS.md`, the initial idea and this blocked L0 record. `git diff --check`
passed. No L0 observation, defect probe, mailbox request/ACK, executable test,
or installed-state change was made by this documentation update.

| Delivery check | Guard and result |
| --- | --- |
| Mac / primary / mirror | Mac `dev` clean at Source S; primary Gitea `origin/dev` and automatic `github/dev` both returned exact Source S using their configured explicit identities |
| `linux-host` | Clean `dev` and expected GitHub origin at `95d214052bc746c756744f0150f44917178487e5`; explicit-identity `pull --ff-only origin dev`; clean at Source S |
| `sandbox-host` | Same pre-pull branch, clean-worktree, origin and SHA guards; explicit-identity `pull --ff-only origin dev`; clean at Source S |
| `factory-sandbox` | Same pre-pull branch, clean-worktree, origin and SHA guards; explicit-identity `pull --ff-only origin dev`; clean at Source S |

Ubuntu host Git access used `/home/ubuntu/.ssh/gavrilo123456-github` with
`IdentitiesOnly=yes`, `BatchMode=yes`, and `StrictHostKeyChecking=yes`. Mac SSH
used the configured explicit host keys and strict host-key checks. No tracked
file was copied outside Git. The complete L0 inventory remains NOT STARTED.
