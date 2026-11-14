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

Deliver this in two proposed phases: **read-only monitoring first**, then a
**deterministic configuration and new-project setup wizard**.

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

## Phase 1: read-only monitoring

Keep the first version small and **read-only**:

- Simple keyboard navigation between the overview, machine, mailbox, activity
  and configuration views; refresh and quit controls.
- A configurable, bounded refresh interval, with observation time and data age.
- Clear states for ready, busy, held, stale, unreachable and unavailable.
  Preserve partial results when one worker is unavailable; do not display an
  unknown value as zero or healthy.
- Compact summaries first, with selected-item details where supported. Avoid
  dumping scripts, full logs or large historical result sets into the screen.

Phase 1 does not include configuration editing, job submission, ACK publication,
cancellation, service restarts, release promotion or cleanup. Phase 2 adds only
the reviewed onboarding workflow below; other management actions remain outside
these two phases.

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

## Phase 2: deterministic configuration and project setup

Add a **New project** wizard that prepares and applies a complete onboarding
package, including deploying `AGENTS.md` into the canonical Mac project.
Reuse the [onboarding procedure](../../docs/new-project-setup.md) and
[versioned AGENTS.md template](../../docs/templates/project-AGENTS.md).
This phase uses existing machines and lanes; it does not provision new hosts.

The proposed workflow is:

1. **Collect inputs:** project name and path, mailbox ID/root, selected lane,
   existing machine/context, execution-policy preset, Git remote/branch and
   approved credential references. Offer registered choices instead of guessing
   environment labels, branches or SSH identities.
2. **Generate:** a complete candidate based on the selected lane's current
   configuration, the project's `AGENTS.md`, and its mailbox runtime Git
   exclusion. Preserve existing mailboxes and policies. Existing project files
   need a reviewed diff; never silently overwrite custom instructions.
3. **Preview:** show redacted file changes, target lane/machine, required
   maintenance, validation results and output hashes before applying anything.
4. **Apply:** within applicable owner authorization, create verified backups,
   perform the required live guards, install project files and activate the
   candidate through supported maintenance. Record progress so interruption
   produces an explicit partial/blocked state, not a misleading success.
5. **Verify:** use a bounded, scoped mailbox check to confirm the actual executor,
   terminal result, event cursor and teardown, then acknowledge only that exact
   setup exchange where authorized. Show what was verified and what remains
   untested, including the optional Mac Git path and client resumption.

**Deterministic generation** means the same normalized inputs, configuration
snapshot, template/preset versions and generator version produce identical file
bytes and hashes. Do not use AI-generated settings or hidden machine-dependent
defaults. Keep operation IDs, timestamps and execution receipts separate from
the reproducible configuration and instruction files.

Application must also be idempotent: recognize files and completed steps already
installed, avoid duplicate mailbox registrations, and resume the recorded
operation. Preserve an uncertain verification request's identity and observe it;
do not publish another test automatically. A changed configuration or project
file invalidates the preview and requires a new review rather than overwriting
concurrent work.

Reproducible generation does not guarantee successful deployment. Applying still
depends on current ownership, capacity, publisher state and service guards.
After durable mailbox registration, a partial activation follows the existing
forward-repair rules. The wizard must never force past a guard or restore an old
database/configuration to hide its partial progress.

## Later design and acceptance

When this improvement is assigned for implementation, prepare its detailed
design and phased plan. Choose the terminal UI library, packaging and any
additional metadata interfaces then; no technology choice is made here.
Keep Phase 1 acceptance ahead of Phase 2 implementation and rollout.

Acceptance should demonstrate that each proposed command shows the correct
lane, configuration and releases; the displayed route/activity matches its
authoritative source; retained holds are not mislabeled as running work; and
an unavailable worker produces a clear partial/stale view. Opening, refreshing,
previewing and closing the UI without an explicit Apply must leave project files,
services, requests, ACKs, holds and selectors unchanged.

Phase 2 acceptance should additionally prove reproducible output, conflict
detection, preserved existing projects, safe interrupted/resumed application,
and a real scoped onboarding result. Validation, generation, activation and
functional verification must be reported as separate outcomes. Installations
of the UI itself follow the normal release and promotion flow.

Related references: [lane identities](../../docs/lanes.md),
[operations and metrics](../../docs/operations.md),
[mailbox lifecycle](../../docs/mailbox.md#read-only-lifecycle-status), and
[new-project mailbox setup](../../docs/new-project-setup.md).
