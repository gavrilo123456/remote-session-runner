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
mailbox, each with independent inbox, outbox, events, and ACK paths. Each inbox
has a default execution context and can allow an explicit complete context
override. `default` permits `mac-local` and `ubuntu-current`; `analytics`
additionally permits an explicit `ubuntu-sandbox` override; `slidestud-io` at
`/Users/tomasz.walczuk/projects/slidestud.io/tmp/mailbox-` defaults to
`ubuntu-sandbox` and also permits `mac-local` and `ubuntu-current`. P155
accepted `linux-host`; P157 separately accepted `sandbox-host`; and P158
accepted the native external-mailbox path to that already accepted sandbox
host. Other new hosts remain unavailable until they pass their own P157 gate.

Start with [the post-implementation documentation index](docs/README.md):

- [Architecture](docs/architecture.md)
- [Configuration reference](docs/configuration.md)
- [Setup and upgrade runbook](docs/setup.md)
- [CLI user guide](docs/user-guide.md)
- [Mailbox guide](docs/mailbox.md)
- [API reference](docs/api.md)
- [Operations runbook](docs/operations.md)
- [Current-host evidence](docs/current-host-evidence.md)

Software-process-crash recovery is evidenced. Physical power-loss recovery has
not yet been verified.
