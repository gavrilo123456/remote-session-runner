# Remote Session Runner

Remote Session Runner is a controlled two-target execution proof of concept.
Scripts run locally on the selected Mac account (`tomasz.walczuk`) or on a
named, accepted Ubuntu profile as `ubuntu`. The current remote profile is
`linux-host`; direct access uses TLS 1.3 mTLS at
`https://129.151.232.40:8443`, while queued work uses a separately authorized
restricted SSH bridge. The PoC has no containers, tunnels, interactive PTYs,
arbitrary account selection, arbitrary host selection, or automatic fallback.

The installed Mac configuration is version 2. It preserves the legacy
`default` mailbox and adds `analytics`, each with independent inbox, outbox,
events, and ACK paths. Each inbox has a default execution context and can allow
an explicit complete context override. The `linux-host` profile is the only
remote host with live acceptance evidence; the user-supplied `sandbox.env`
candidate is **NOT RUN** pending its own P157 host gate.

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
