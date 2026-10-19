# Initial idea: configurable log levels per release lane

**Status:** proposed improvement — registered 2026-10-19. Documentation only;
no logging configuration, service or runtime behavior changes are assigned.

## Goal

Control Runner service log verbosity from each lane's configuration. Dev can
record routine operational information, while PRD normally records only the
selected high-severity messages. During a production investigation, the operator
can temporarily increase PRD verbosity and restore its usual level afterward,
without rebuilding binaries or changing Dev/QAS logging.

## Requested behavior

| Situation | Example configured level | Intended result |
| --- | --- | --- |
| Normal Dev use | `info` | Routine operational events, warnings, errors and critical failures |
| Normal PRD use | `critical` | Critical service failures only in the ordinary service log |
| Investigating PRD | Temporarily `info`, optionally `debug` | Additional diagnostic context for events occurring after the change |
| Investigation finished | Restore the previous PRD level | Return to normal bounded log volume |

Levels should have explicit severity ordering, for example
`debug < info < warn < error < critical`. The selected level is a minimum
threshold: `info` includes info and higher severities; finer debug details
require `debug`. Neither level means logging secrets or every raw payload.
Changing verbosity cannot reconstruct messages that were previously suppressed.

The exact `critical` mapping must be defined consistently across components;
it is a severity, not an instruction to terminate the process. Operators may
choose `error` or `warn` for PRD when they want those messages as well.

## Configuration and scope

Proposed configuration shape, **not currently an implemented configuration key**:

```yaml
logging:
  level: info
```

Use the independently installed configurations for Dev, QAS and unsuffixed PRD
on the existing Mac and three Ubuntu workers. Apply the selected lane's policy
consistently to its ingress, executor and bridge service diagnostics. One lane's
change must not change another lane's logging or require a binary rebuild.

Reject invalid levels before activation and expose the effective level through
a supported status/configuration view. Prefer validated configuration reload
without interrupting work; if the first implementation requires a restart,
document that fact and restrict the restart to the selected lane. A malformed
reload keeps the previous valid setting. Do not silently change installed PRD
verbosity when the new setting is omitted.

Provide a simple operator workflow to inspect the current level, raise PRD to
`info` or `debug`, verify the effective setting, then restore the recorded prior
level. Consider an optional expiry that automatically restores temporary
verbosity. Record level changes with lane/component and time so incident
evidence shows when detailed logging was enabled.

## Constraints

- Apply severity filtering at log generation using consistently classified
  events, including existing direct stderr messages; do not guess severity by
  filtering arbitrary text after capture.
- Keep mandatory audit/security evidence and durable execution/recovery state
  independent of ordinary service-log verbosity. Keep health checks and error
  counters accurate even when a message is suppressed.
- Keep command stdout/stderr, mailbox responses/events and bridge protocol
  output intact. Service log levels must not filter execution results or inject
  diagnostic messages into a machine-readable transport stream.
- Preserve redaction at every level: no private keys, credentials, raw scripts
  or unbounded request/response bodies. Include bounded correlation identifiers
  and useful operation/error-stage information instead.
- Preserve existing [log capture and rotation](../../src/internal/logcapture/logcapture.go)
  limits when verbosity increases. Temporary `info`/`debug` must not create an
  unlimited disk-growth path. Coordinate log retention with the
  [retention-policy proposal](../050-retention-policy-improvements/005-initial-idea.md).

## Acceptance direction

Verify threshold behavior for every severity, invalid values, effective-setting
visibility, lane isolation and the documented reload/restart behavior. Prove
PRD can change from its normal threshold to `info` and back without rebuilding
or affecting another lane. Check redaction, bounded rotation, preserved audit
evidence, unchanged command/protocol output and truthful error counters at all
levels. If temporary expiry is implemented, test automatic restoration and
restart/reload interaction.

This registers a future improvement only. It does not extend the active BUG-025
repair, retention implementation or service-restart scope.
