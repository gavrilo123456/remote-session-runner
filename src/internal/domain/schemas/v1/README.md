# v1 domain schemas

These JSON Schemas freeze shared resource and command-event shapes for P002.
They do not define HTTP routes, acceptance responses, SSH bridge frames, or
mailbox requests and responses; those belong to P003 and P004.

Resource objects identify their immutable execution target, authority,
controller, observation time, environment, effective source, host class, OS
user permission boundary, and service limits actually advertised. A Mac
projection may include `is_stale`. Source fields allow a requested Git
revision before resolution and its exact resolved commit afterward.

The command-event type names are `command_queued`, `command_started`,
`stdout`, `stderr`, `output_truncated`, and one terminal type matching the
outcome: `command_succeeded`, `command_failed`, `command_cancelled`,
`command_timed_out`, `command_rejected`, or `command_lost`. Every accepted
command's first event is `command_queued`; output events use base64 bytes and
the original byte count on HTTP/SSH transports. This mapping gives the
documented terminal-event requirement a stable v1 vocabulary.

The resource, state, event, and error objects reject unknown top-level fields.
`capabilities.service_limits` and error `details` remain open objects because
their members depend on later environment and error-specific contracts.
