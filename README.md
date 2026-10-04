# Remote Session Runner

Remote Session Runner is a controlled execution proof of concept. Scripts run
locally on the selected Mac account (`tomasz.walczuk`) or on a named, accepted
Ubuntu profile as `ubuntu`. The accepted remote profiles are `linux-host`, with
direct access at `https://129.151.232.40:8443`, and `sandbox-host`, with direct
access at `https://132.226.205.205:8443`. Both direct routes use TLS 1.3 mTLS;
queued work uses a separately authorized restricted SSH bridge for the selected
profile. The PoC has no containers, tunnels, interactive PTYs, arbitrary
account selection, arbitrary host selection, or automatic fallback.

The installed Mac configuration is version 2. It preserves the legacy
`default` mailbox and adds `analytics` plus the external `slidestud-io`
mailbox, each with independent inbox, outbox, events, ACK, and diagnostic
paths. Each inbox has a default execution context and can allow an explicit
complete context override. `default` permits `mac-local` and `ubuntu-current`;
`analytics` additionally permits an explicit `ubuntu-sandbox` override; `slidestud-io` at
`/Users/tomasz.walczuk/projects/slidestud.io/tmp/mailbox-` defaults to
`ubuntu-sandbox` and also permits `mac-local` and `ubuntu-current`. P155
accepted `linux-host`; P157 separately accepted `sandbox-host`; P158 accepted
the native external-mailbox path to that already accepted sandbox host; and
P159 accepted selected-user-owned direct workspace `0644` request/ACK pairs
while retaining private `0600` responses and events. Each mailbox also has a
private `diagnostics/` path: a safely published request that fails safe ingress
validation is rejected there without becoming accepted work or
contacting a remote host. This includes malformed JSON, request-schema,
identity, script-representation, and bounded-size failures. Other new hosts
remain unavailable until they pass their own P157 gate.

BUG-015 corrected one shared persistent-shell boundary. An ordinary nonzero
command under `set -e` or `set -euo pipefail` now produces a complete terminal
`failed` result with its actual exit code; it no longer turns into an invented
`lost` result. An actual shell exit, `exec`, reserved control-file-descriptor
damage, or an unconfirmed output boundary remains conservatively `lost`.
See the [current-host evidence](docs/current-host-evidence.md) for the scoped
installed sandbox control and its limits.

Start with [the post-implementation documentation index](docs/README.md):

- [Architecture](docs/architecture.md)
- [Configuration reference](docs/configuration.md)
- [Setup and upgrade runbook](docs/setup.md)
- [CLI user guide](docs/user-guide.md)
- [LLM client guide](docs/llm-client-guide.md)
- [Mailbox guide](docs/mailbox.md)
- [API reference](docs/api.md)
- [Operations runbook](docs/operations.md)
- [Current-host evidence](docs/current-host-evidence.md)

Software-process-crash recovery is evidenced. Physical power-loss recovery has
not yet been verified.
