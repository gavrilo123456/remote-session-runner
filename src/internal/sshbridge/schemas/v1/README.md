# SSH bridge v1 wire contract

`request.schema.json` and `reply.schema.json` freeze the JSON objects carried as
newline-delimited records on the restricted SSH bridge's stdin and stdout. Each
decoded record is limited to 1 MiB by the eventual adapter. Stderr remains
diagnostic-only; SSH process exit status is not a command result.

Requests carry protocol major 1, a request ID, an operation, and a payload.
The ten forwarded operations are the detailed design's fixed vocabulary.
Mutation frames also carry a stable resource ID and idempotency key. `hello` is
a protocol-control operation and `ping` is an empty health request; neither is
forwarded as an execution mutation. Payload schemas require the operation's
identity and input fields while leaving later optional policy fields open.

Replies use `response_type` values `hello`, `result`, `error`, `event`, and
`stream_end`, and echo the request ID. A result payload contains the returned
resource snapshot or ping result. An error payload uses the shared structured
error members. An event frame carries one P002 command event with base64 output;
each raw output chunk is at most 16 KiB. An event stream ends with a
`stream_end` payload containing its last sequence.
This wrapper spelling freezes a versioned transport shape without implementing
SSH forwarding, persistence, scheduling, authentication, or a process runner.

The protocol-version const in each schema makes an unsupported major invalid
before forwarding. The fixture tests prove contract validation only; they do
not exercise an SSH client, forced command, or live host.
