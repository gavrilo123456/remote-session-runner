# Initial idea: terminal dashboard for Runner configuration and monitoring

**Status:** proposed improvement — registered 2026-11-14. Idea only; no UI
implementation, installation or live configuration change is assigned here.

## Goal

Provide a simple interactive, text-based console where an operator can see
how Runner is configured and what it is doing. Today, answering basic questions
about machines, mailboxes and work often requires several commands and files.
One dashboard should make that information easy to find and understand.

Start on the existing Mac control plane, using the existing operator account
and supported connections to its three Ubuntu workers. Each environment gets
its own entry point and a clearly separate view.

## Proposed commands and environment separation

| Proposed command | Environment | Mac configuration/data namespace |
| --- | --- | --- |
| `session-runner-dev` | Dev | `RemoteSessionRunner-dev` |
| `session-runner-qas` | QAS | `RemoteSessionRunner-qas` |
| `session-runner` | PRD | Unsuffixed `RemoteSessionRunner` |

These command names are a proposal, not existing functionality. The selected
environment must be prominent on every screen, together with the actual
configuration and release identity. Each command reads only its selected lane's
configuration, sockets, selectors and status sources. It must not silently
fall back to another environment or change the running services when opened.

Use the configured target profile and route to identify a machine. A Git branch
or a legacy environment label such as `sandbox-dev` does not establish which
physical machine or Runner lane is being observed.

## What the dashboard should show

| View | Useful information |
| --- | --- |
| Overview | Selected environment; service readiness; admission open/held; queued and running work; retained cleanup holds; important warnings. |
| Machines | Mac and configured workers; hostname/address and route; configured versus reachable versus ready; selected/running release; capacity and errors. Disk space and other host metrics can follow where a supported source exists. |
| Mailboxes | Mailbox ID, project/repository alias, root, default executor, allowed routes, pending requests, outstanding terminal responses/ACKs and recent activity. Make clear that mailbox files live on the Mac even when execution is remote. |
| Activity and users | Identifiable clients/controllers and projects with active work; their mailbox, target machine and known request/job/session/command state, with correlation IDs when available. |
| Configuration | An understandable, redacted view of effective lane settings, mailbox routing, execution policies, service limits and release/configuration paths. |

“Active users” should mean identifiable clients or controllers with active
work, not the number of logged-in people. Show the execution account separately:
many projects can execute as the same `ubuntu` or `tomasz.walczuk` account.
If no supported identity or activity evidence exists, show that limitation
rather than guessing who is online.

Distinguish **running work**, **queued work**, **uncertain delivery** and
**retained LOST work awaiting cleanup**. An occupied reservation is not proof
that a command is running. Likewise, service readiness does not prove that
admissions are open, capacity is free or a mailbox exchange has completed.

## First version

Keep the first version small and **read-only**:

- Simple keyboard navigation between the overview, machine, mailbox, activity
  and configuration views; refresh and quit controls.
- A configurable, bounded refresh interval, with observation time and data age.
- Clear states for ready, busy, held, stale, unreachable and unavailable.
  Preserve partial results when one worker is unavailable; do not display an
  unknown value as zero or healthy.
- Compact summaries first, with selected-item details where supported. Avoid
  dumping scripts, full logs or large historical result sets into the screen.

This initial scope does not include configuration editing, job submission,
ACK publication, cancellation, service restarts, release promotion or cleanup.
Any later management actions should be separately designed around Runner's
existing authorization, maintenance and recovery procedures.

## Monitoring approach

Reuse supported health, metrics, lifecycle and release-status interfaces.
Current health reports already provide readiness, build identity, aggregate
slots/queues, error counters and per-mailbox backlog. The mailbox lifecycle
interface supplies additional exchange-state information.

Those aggregate interfaces do not currently provide every proposed user/job
list or correlation. The later design must identify missing read-only metadata
interfaces and their access rules; this proposal does not assume they already
exist or authorize a UI to bypass them with direct database access.

Monitoring must remain inexpensive: use bounded queries, short timeouts,
backoff and cached snapshots where appropriate. Do not rescan complete mailbox
or database histories on every redraw, or start repeated unrestricted SSH
sessions for each screen. Reading the dashboard must not interfere with
execution, result delivery or cleanup.

Redact credentials, tokens, private-key contents and sensitive configuration
values. Use the existing owner and lane access boundaries; no new public
listener, privileged account or monitoring service is required by this idea.

## Later design and acceptance

When this improvement is assigned for implementation, prepare its detailed
design and phased plan. Choose the terminal UI library, packaging and any
additional metadata interfaces then; no technology choice is made here.

Acceptance should demonstrate that each proposed command shows the correct
lane, configuration and releases; the displayed route/activity matches its
authoritative source; retained holds are not mislabeled as running work; and
an unavailable worker produces a clear partial/stale view. Opening, refreshing
and closing the UI must leave services, requests, ACKs, holds and selectors
unchanged. Installations should follow the normal release and promotion flow.

Related references: [lane identities](../../docs/lanes.md),
[operations and metrics](../../docs/operations.md),
[mailbox lifecycle](../../docs/mailbox.md#read-only-lifecycle-status), and
[new-project mailbox setup](../../docs/new-project-setup.md).
