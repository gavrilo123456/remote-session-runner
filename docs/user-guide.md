# CLI user guide

The installed CLI is:

```text
/Users/tomasz.walczuk/Library/Application Support/RemoteSessionRunner/bin/runner
```

It submits discrete scripts. It does **not** open an interactive Bash prompt or
allocate a PTY. A session does keep a Bash process, so shell state can persist
between `exec` requests in that same session.

## Select the route first

Put global options before the command:

```text
runner --endpoint <local|profile> [--config PATH] [--wait-timeout DURATION] COMMAND
```

| Intended work | Endpoint and target | Execution account | Current condition |
| --- | --- | --- | --- |
| Local Mac | `--endpoint local`; `mac-dev`, `local`, `mac-workstation` | `tomasz.walczuk` | Available when the two Mac LaunchAgents are healthy. |
| Remote Ubuntu, direct | `--endpoint linux-poc --config <mac.yaml>`; `linux-dev`, `remote`, `linux-host` | `ubuntu` | Uses direct public TLS 1.3 mTLS. This is the currently verified remote route. |
| Remote Ubuntu, queued | `--endpoint local`; `linux-dev`, `remote`, `linux-host` | `ubuntu` | Requires the separately installed permanent restricted SSH bridge. Verify it on Ubuntu with `deploy/ssh/install-queued-bridge.sh status`. |

Use the same endpoint for the complete resource lifecycle. The queued and
direct remote routes use different controller identities, so a resource made
by one is not visible or controllable through the other.

Set convenient shell variables on the **Mac**:

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

`--wait-timeout` accepts `1s` through `10m` and defaults to one minute.
It bounds CLI waiting, not the accepted command's lifetime. A wait timeout
does not cancel accepted work.

The CLI generates and prints an idempotency key for each mutation if you omit
`--idempotency-key`. Keep that key if delivery becomes uncertain. Retry the
same semantic mutation with the same key; do not generate a new key until the
outcome is known.

## Run a one-off command

A one-off `run` creates an ephemeral session, runs one script, emits its
events, and closes the session.

### Mac local

```sh
"$RUNNER" --endpoint local run \
  --environment mac-dev \
  --target local \
  --profile mac-workstation \
  -- 'printf "LOCAL_OK\\n" && id -un && hostname'
```

### Direct Ubuntu

```sh
"$RUNNER" --endpoint linux-poc --config "$CONFIG" run \
  --environment linux-dev \
  --target remote \
  --profile linux-host \
  -- 'printf "REMOTE_OK\\n" && id -un && hostname'
```

The remote output should identify `ubuntu`. Treat a returned command ID as the
handle for later event replay if output streaming is interrupted.

## Use a persistent session

Create a direct remote session:

```sh
"$RUNNER" --endpoint linux-poc --config "$CONFIG" session create \
  --environment linux-dev \
  --target remote \
  --profile linux-host
```

The command prints `session_id`, `endpoint`, `execution_target`, and its
idempotency key. Save the session ID. The default command waits until the
session is ready; add `--no-wait` to return after durable acceptance and then
use `session status` to observe readiness.

Run a first script, replacing `SESSION_ID` with the printed value:

```sh
"$RUNNER" --endpoint linux-poc --config "$CONFIG" exec SESSION_ID -- \
  'export RSR_DEMO_VALUE=kept-in-this-session; printf "SET_OK\\n"'
```

Run a second script in the same session:

```sh
"$RUNNER" --endpoint linux-poc --config "$CONFIG" exec SESSION_ID -- \
  'printf "VALUE=%s\\n" "$RSR_DEMO_VALUE"'
```

This demonstrates persistent Bash state without turning the CLI into an
interactive terminal. A new session starts a new Bash process and does not
inherit this shell variable.

Inspect the session at any time:

```sh
"$RUNNER" --endpoint linux-poc --config "$CONFIG" session status SESSION_ID
```

Status includes the state, target, authority view, effective account,
isolation model, staleness flag, and applied service limits.

Close the session once its work is complete:

```sh
"$RUNNER" --endpoint linux-poc --config "$CONFIG" session close \
  --policy graceful SESSION_ID
```

The CLI close policy defaults to `graceful`. The close response is accepted
first, followed by a status observation of the teardown outcome.

## Read or resume command output

Every command has an ordered event sequence. Read retained history once:

```sh
"$RUNNER" --endpoint linux-poc --config "$CONFIG" events COMMAND_ID --after 0
```

Follow a running command from a known cursor:

```sh
"$RUNNER" --endpoint linux-poc --config "$CONFIG" events COMMAND_ID --after 12 --follow
```

The CLI writes command stdout and stderr to their matching streams and prints
lifecycle/status fields separately. A complete history read has all of these:

- started at `--after 0`
- `event_cursor` equal to `final_event_sequence`
- `output_complete: true`
- `output_truncated: false`
- `event_history_complete_this_read: true`

If history has expired or has an irrecoverable gap, the command reports
`output_unavailable_reason`. It must not be described as complete output.

## Cancel a command

```sh
"$RUNNER" --endpoint linux-poc --config "$CONFIG" cancel COMMAND_ID
"$RUNNER" --endpoint linux-poc --config "$CONFIG" events COMMAND_ID --after 0 --follow
```

`cancel` confirms a cancellation request, not an eventual `cancelled` terminal
state. Read events or status after it. Preserve the printed idempotency key if
the cancellation transport result is uncertain.

## Queued remote session after bridge setup

After the restricted SSH bridge is deliberately installed and verified, the
same session workflow uses the Mac endpoint:

```sh
"$RUNNER" --endpoint local session create \
  --environment linux-dev \
  --target remote \
  --profile linux-host
```

Use `--endpoint local` for later `session status`, `exec`, `events`, `cancel`,
and `session close` on that session. A queued response can show
`view: local_intent` or `view: projection` and `is_stale: true` while Mac
reconciles with Ubuntu. The direct mTLS endpoint should not be substituted.

## Useful behavior to remember

- The CLI does not expose source selection, custom service limits, or a
  per-command timeout request. Current CLI requests use the configured empty
  source mode and service defaults.
- Direct HTTPS accepts only the remote target. A local command always uses
  `--endpoint local`.
- `run` has no standalone CLI job-status command. Save the printed IDs and
  use the same route's command events for output.
- Output data can be large or retained only for the configured period. Save
  important results outside Runner before retention cleanup.
- Run `"$RUNNER" --help` or a command-specific `--help` for the installed
  command syntax.
