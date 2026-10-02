# CLI user guide

The installed CLI is:

```text
/Users/tomasz.walczuk/Library/Application Support/RemoteSessionRunner/bin/runner
```

It submits discrete scripts. It does not open an interactive Bash prompt or
allocate a PTY. A session keeps one Bash process, so shell state can persist
between `exec` requests in that same session.

The CLI selects an ingress endpoint and target profile. It does **not** select
a file inbox; mailbox roots and their default/override policy belong to the
[mailbox integration](mailbox.md).

## Select the route first

Put global options before the command:

```text
runner --endpoint <local|configured-direct-endpoint> [--config PATH] [--wait-timeout DURATION] COMMAND
```

| Intended work | Endpoint and target | Execution account | Current evidence |
| --- | --- | --- | --- |
| Local Mac | `--endpoint local`; `mac-dev`, `local`, `mac-workstation` | `tomasz.walczuk` | Use when both Mac LaunchAgents are ready. |
| Direct current Ubuntu | `--endpoint linux-poc --config <mac.yaml>`; `linux-dev`, `remote`, `linux-host` | `ubuntu` | Direct public TLS 1.3 mTLS route for the accepted current profile. |
| Queued current Ubuntu | `--endpoint local`; `linux-dev`, `remote`, `linux-host` | `ubuntu` | Requires current bridge `status` to be ready. |
| Direct sandbox Ubuntu | `--endpoint sandbox-poc --config <mac.yaml>`; `sandbox-dev`, `remote`, `sandbox-host` | `ubuntu` | P157 accepted its public mTLS Runner readiness route. |
| Queued sandbox Ubuntu | `--endpoint local`; `sandbox-dev`, `remote`, `sandbox-host` | `ubuntu` | P157 accepted its restricted bridge and native mailbox route; verify its bridge `status` before use. |

`linux-poc`/`linux-host` and `sandbox-poc`/`sandbox-host` are configured pairs,
not universal names. A future direct endpoint must be defined in the V2
`remote_hosts` policy, bound to exactly one target profile, and accepted by
P157 before use. The CLI rejects an endpoint/target mismatch before it sends a
mutation.

Use the same endpoint for a resource's complete lifecycle. Direct and queued
remote routes use different controller identities, so a resource created by one
is not visible through the other.

Set convenient variables on the **Mac**:

```sh
RUNNER='/Users/tomasz.walczuk/Library/Application Support/RemoteSessionRunner/bin/runner'
CONFIG='/Users/tomasz.walczuk/Library/Application Support/RemoteSessionRunner/config/mac.yaml'
```

## Command inventory

```text
session create --environment NAME --target local|remote --profile NAME [--no-wait] [--idempotency-key KEY]
session status SESSION_ID
session close [--policy POLICY] [--idempotency-key KEY] SESSION_ID
exec [--idempotency-key KEY] SESSION_ID -- SCRIPT
events COMMAND_ID [--after SEQUENCE] [--follow]
cancel [--idempotency-key KEY] COMMAND_ID
run --environment NAME --target local|remote --profile NAME [--idempotency-key KEY] -- SCRIPT
```

`--wait-timeout` accepts `1s` through `10m` and defaults to one minute. It
bounds the CLI wait, not an accepted command's lifetime. A timeout does not
cancel accepted work.

The CLI generates and prints an idempotency key for each mutation when you omit
`--idempotency-key`. Preserve it if delivery is uncertain. Retry the same
semantic mutation with the same key; do not create a replacement mutation.

## Run a one-off command

A one-off `run` creates an ephemeral session, runs one script, emits its events,
and closes it.

### Mac local

```sh
# Mac — tomasz.walczuk
"$RUNNER" --endpoint local run \
  --environment mac-dev \
  --target local \
  --profile mac-workstation \
  -- 'printf "LOCAL_OK\\n" && id -un && hostname'
```

### Direct current Ubuntu

```sh
# Mac — tomasz.walczuk
"$RUNNER" --endpoint linux-poc --config "$CONFIG" run \
  --environment linux-dev \
  --target remote \
  --profile linux-host \
  -- 'printf "REMOTE_OK\\n" && id -un && hostname'
```

The output should identify `ubuntu`. This verifies the direct current-host
route. It does not test a mailbox or the queued bridge.

### Direct sandbox Ubuntu

```sh
# Mac — tomasz.walczuk
"$RUNNER" --endpoint sandbox-poc --config "$CONFIG" run \
  --environment sandbox-dev \
  --target remote \
  --profile sandbox-host \
  -- 'printf "SANDBOX_OK\\n" && id -un && hostname && uname -m'
```

P157 separately accepted this profile's Runner readiness and native mailbox
route. This command is an operator check for the direct controller; it does
not test mailbox delivery or the queued bridge.

### Queued current Ubuntu

First verify `deploy/ssh/install-queued-bridge.sh status` on the current Ubuntu
host. Then run:

```sh
# Mac — tomasz.walczuk
"$RUNNER" --endpoint local run \
  --environment linux-dev \
  --target remote \
  --profile linux-host \
  -- 'printf "QUEUED_REMOTE_OK\\n" && id -un && hostname'
```

The result can initially report `view: local_intent` or `view: projection` and
`is_stale: true` while the Mac reconciles with the Ubuntu authority.

### Queued sandbox Ubuntu

First verify `deploy/ssh/install-queued-bridge.sh status` on the sandbox
Ubuntu host. Then run:

```sh
# Mac — tomasz.walczuk
"$RUNNER" --endpoint local run \
  --environment sandbox-dev \
  --target remote \
  --profile sandbox-host \
  -- 'printf "QUEUED_SANDBOX_OK\\n" && id -un && hostname && uname -m'
```

Use this queued CLI route for a resource's complete lifecycle. For file
mailbox work, `analytics` permits the corresponding `ubuntu-sandbox` override,
and the external `slidestud-io` inbox uses it as its default when both selection
fields are omitted; the `default` inbox does not permit it.

## Use a persistent session

Create a direct current-host session:

```sh
# Mac — tomasz.walczuk
"$RUNNER" --endpoint linux-poc --config "$CONFIG" session create \
  --environment linux-dev \
  --target remote \
  --profile linux-host
```

The CLI prints `session_id`, endpoint, target, and idempotency key. Save the
session ID. The default waits for readiness; `--no-wait` returns after durable
acceptance, then `session status` can observe readiness.

Run a first script, replacing `SESSION_ID`:

```sh
"$RUNNER" --endpoint linux-poc --config "$CONFIG" exec SESSION_ID -- \
  'export RSR_DEMO_VALUE=kept-in-this-session; printf "SET_OK\\n"'
```

Run a second script in the same session:

```sh
"$RUNNER" --endpoint linux-poc --config "$CONFIG" exec SESSION_ID -- \
  'printf "VALUE=%s\\n" "$RSR_DEMO_VALUE"'
```

A new session starts a new Bash process, so it does not inherit this variable.

Inspect and close the session through the same endpoint:

```sh
"$RUNNER" --endpoint linux-poc --config "$CONFIG" session status SESSION_ID
"$RUNNER" --endpoint linux-poc --config "$CONFIG" session close \
  --policy graceful SESSION_ID
```

The CLI close policy defaults to `graceful`. A close response is accepted first,
then the CLI reads the teardown outcome.

## Read or resume command output

Each command has ordered events. Read retained history once:

```sh
"$RUNNER" --endpoint linux-poc --config "$CONFIG" events COMMAND_ID --after 0
```

Follow a running command from a known cursor:

```sh
"$RUNNER" --endpoint linux-poc --config "$CONFIG" events COMMAND_ID --after 12 --follow
```

The CLI writes command stdout/stderr to their matching streams and lifecycle
fields separately. A complete read has all of:

- a read starting at `--after 0`;
- `event_cursor` equal to `final_event_sequence`;
- `output_complete: true`;
- `output_truncated: false`; and
- `event_history_complete_this_read: true`.

If output retention expired or history has an irrecoverable gap, the command
reports `output_unavailable_reason`. It must not be described as complete.

## Cancel a command

```sh
"$RUNNER" --endpoint linux-poc --config "$CONFIG" cancel COMMAND_ID
"$RUNNER" --endpoint linux-poc --config "$CONFIG" events COMMAND_ID --after 0 --follow
```

`cancel` confirms a cancellation request, not a final cancelled state. Read
status/events through the same endpoint and preserve the idempotency key if
transport becomes uncertain.

## Mailbox and profile choices

- `--profile` names a configured execution target. It never selects an inbox.
- The CLI has no interactive terminal and no mailbox selector.
- Mailbox `run` and `create_session` can omit both selection fields to use
  that inbox's default, or supply an exact allowed pair as an override. See
  [mailbox](mailbox.md#new-work-target-resolution).
- A context name such as `ubuntu-current` is configuration, never a scalar
  `execution_target` wire value. A mailbox override needs both an environment
  and an object target such as `{"kind":"remote","profile":"linux-host"}`.
  A safe ingress-validation failure receives a private diagnostic rather than
  an ordinary CLI or outbox result; see the [mailbox correction
  flow](mailbox.md#safe-invalid-input-diagnostics).
- The `slidestud-io` mailbox root is
  `/Users/tomasz.walczuk/projects/slidestud.io/tmp/mailbox-`; its omitted pair
  resolves to `sandbox-dev` / `remote/sandbox-host`. The CLI does not publish
  mailbox files; use the native publisher or the documented direct workspace
  file path.
- Direct HTTPS accepts remote targets only. A Mac-local command uses
  `--endpoint local`.
- `run` has no standalone CLI job-status command. Preserve its IDs and use the
  same route's events for output. An observed one-off job snapshot can print
  `queue_blocked_reason: lost_capacity_recovery_pending`; see the mailbox and
  operations guides before acting on that nonterminal status.
- Run `"$RUNNER" --help` or a command-specific `--help` for installed syntax.
  The help text's `linux-poc` spelling is one configured direct endpoint
  example; V2 config can register more endpoint names after their P157 gates.

See [current-host evidence](current-host-evidence.md) before treating a remote
profile as available.
