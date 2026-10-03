# BUG-014 — Fully idle Mac retained lost capacity has no safe recovery/install path

## Summary

| Field | Value |
| --- | --- |
| Status | `CLOSED` |
| Severity | High |
| Priority | High — retained slots prevent new Mac-local mailbox work from reaching the executor |
| Reported | 2026-10-03 |
| Discovered by | Codex while preparing the approved BUG-013 Mac service refresh and harmless live acceptance test |
| Owner | Codex |
| Affected area | Mac `runner-local`, `runner-locald`, `local.db`, LaunchAgent installation, and Mac-local mailbox execution |
| Installed revision | `476182c819b84c2f813eb41c2a5df2183898ca18` |
| Source correction | Commit `476182c819b84c2f813eb41c2a5df2183898ca18` from baseline `a1a0613fbade5e58a540e37f6a38917152d97d79` |
| Related records | [BUG-011](011-mac-local-executor-lost-command-slots-block-local-execution.md) closed its earlier four-lost-plus-queued controlled restart. [BUG-013](013-mac-local-git-success-output-terminal-lost.md) records the preserved terminal-lost commands whose capacity is now retained. |

## Reported behavior

The normal Mac installer correctly refused to refresh while durable Mac-local
capacity was retained. The current durable inventory had three terminal `lost`
session/command pairs, no queued command, no nonterminal job, and no work that
must survive. It therefore did not match the earlier BUG-011 controlled-restart
shape of four terminal-lost pairs plus one preserved queued one-off.

A normal restart is not a safe remedy. It would change service state without
proving that the old Router and executor have stopped or that the selected lost
runtime cleanup boundary is safe. Direct `launchctl`, `go run`, or SQLite work
would bypass the installed lifecycle boundary and could race a prior executor.

## Expected behavior

When a fresh Mac inventory proves that all retained capacity belongs to a
complete, explicit set of already-terminal `lost` pairs and no work must
survive, an operator needs one bounded offline procedure that:

1. stages the candidate before changing the running services;
2. stops ingress and then the executor, proving the old processes are exited
   or inert before recovery begins;
3. validates the entire durable capacity and ownership inventory;
4. uses the shared proof-before-release recovery transaction only for the
   exact selected pairs;
5. never reads, starts, or replays a stored script; and
6. requires an all-zero postflight before the normal service restart.

If any condition is not proved, the operation must refuse without releasing
capacity or starting a replacement command.

## Confirmed evidence

The read-only Mac authority inventory recorded:

```text
active_session_slots=3
active_command_slots=3
queued_commands=0
resumable_one_off_jobs=0
```

The three retained terminal-lost pairs are:

| Session ID | Command ID |
| --- | --- |
| `sess-ba80eed0c352ea7dc1b77629b2f46209` | `cmd-3de63fa2b336439dda9262f78e52ce61` |
| `sess-38dec515e55974ef62f97e50bf5a2444` | `cmd-7d32326a007ef197b444b38bb6b7916f` |
| `sess-7ceabfb97a62c5df2cb737d98140b316` | `cmd-4e3698e56fd5720a46b4fe0a1751b9c4` |

These records remain historical terminal `lost` results. They are not a request
to retry, acknowledge, cancel, or replay them. The source correction must
preserve them as `lost` while it releases only the proven stale capacity.

## Root cause

The ordinary installer deliberately has a zero-active-work preflight to avoid
starting, closing, or otherwise changing retained work during an upgrade. That
gate correctly refused this state. The earlier BUG-011 installer procedure was
also deliberately narrower: it preserves one exact queued command behind four
lost pairs. It correctly refuses this different fully idle three-pair state.

The Mac shared execution service already has the durable proof/release model
needed for terminal lost-runtime recovery, but there was no installer-mediated
offline entry point that combined it with the Mac-specific process boundary.
Consequently, the only apparent ways to clear capacity were unsafe manual
service or SQLite actions.

## Correction

The source candidate adds an explicit:

```text
deploy/macos/install-launchagents.sh --recover-stalled \
  --lost-pair SESSION_ID:COMMAND_ID [...]
```

route. It is valid only with one or more unique exact pairs and cannot be
combined with the BUG-011 controlled-restart mode or a candidate-config
activation. It stages candidate binaries before touching the active services,
captures both old LaunchAgent process identities, stops the Router before
locald, and proves each old process exited or is inert before recovery.

The staged `runner-locald recover-stalled` command uses an exclusive lifecycle
lock and the existing shared recovery service. It requires the two Mac
LaunchAgents and private sockets to be stopped, verifies the exact complete
idle inventory and runtime ownership, then retains the conservative `lost`
command/session result while releasing only proven selected capacity. It does
not execute a stored request body or script. Its postflight rejects any
remaining active/running capacity or nonterminal job; only then does the
ordinary installer restart the candidate services.

If the paired durable release commits but owner-only marker/workspace
finalization does not, the same exact recovery command is retryable only when
every pending finalization pair is explicitly supplied again and the fresh idle
inventory still passes. That retry finalizes the retained cleanup only: it
does not send a process signal or replay a script. Any omitted, extra, or
unrelated pending pair is refused. The current three-pair evidence has no
pending finalization.

## Verification completed

1. Focused parser, inventory, lifecycle-lock, process-boundary, pending
   finalization retry, and no-execution tests passed; full `make check` passed.
2. The intended change was committed as
   `476182c819b84c2f813eb41c2a5df2183898ca18`, pushed to GitHub `dev`, and the
   clean primary Ubuntu checkout fast-forwarded to the same SHA.
3. Immediately before maintenance, both Mac LaunchAgents were ready at old
   revision `697939806b18a560e0992a43b7ce1c1558523219`; their health reports
   showed exactly 3 active session slots, 3 active command slots, and zero
   queued commands, intents, and mailbox backlog.
4. The approved installer command named exactly the three pairs above. Its
   staged recovery reported `recovered_lost_pairs=3` and its normal restart
   preflight reported local execution quiescent.
5. Both restarted services reported ready at
   `476182c819b84c2f813eb41c2a5df2183898ca18` with zero active session slots,
   command slots, queued commands, intents, and mailbox backlog. The
   read-only authority check retained all three historical commands as
   `lost`, with `output_complete=false` and final event type `command_lost`.
6. Fresh native file-only request `req-bug014-live-18db1d4633c6fbf8` through
   the default Mac mailbox completed successfully on `mac-dev` /
   `local/mac-workstation`: complete non-truncated output, closed teardown,
   terminal event history, and a consumed marker-last ACK.

## Safety limits

- This is not an online recovery procedure and cannot preserve queued work.
- A partial inventory, an unlisted retained pair, a pending nonterminal job, a
  running process, an uncertain owner marker, an unreadable database, or an
  unproven old-process boundary must stop the procedure.
- A pending finalization is permitted only when every such pair is named again
  in the same recovery command; it is never a reason to infer a successful
  cleanup or run a partial retry.
- Do not use direct `launchctl`, `go run`, an installed `runner-locald`
  executable, SQLite tools, direct file changes, or manual signals to work
  around a refusal.
- Physical power-loss durability remains unverified. This is a controlled
  software-process recovery path only.

## History

| Date | Change |
| --- | --- |
| 2026-10-03 | Created from the read-only fully idle three-pair retained-capacity inventory. No historical request, command, outbox, event file, or ACK was changed. |
| 2026-10-03 | Added the installer-mediated source candidate and runbook. Commit, cross-host source handoff, installed-service recovery, and fresh mailbox acceptance remain pending. |
| 2026-10-03 | Commit `476182c819b84c2f813eb41c2a5df2183898ca18` passed source gates, was pushed and fast-forwarded to primary Ubuntu, then was installed through the exact three-pair recovery. Both Mac services are ready at that revision with zero live work; the fresh native mailbox acceptance passed. |
