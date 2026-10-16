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
observations. Other execution uses the normal mailbox workflow, and declared
operational approval checkpoints remain mandatory. The unsuffixed production
Runner stays protected.

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
commands. No L0 inventory was started and no later unit may start until the
PREP blockers have been corrected and a fresh PREP/L0 sequence passes.
