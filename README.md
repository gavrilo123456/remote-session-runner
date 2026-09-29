# Remote Session Runner

Remote Session Runner is a two-host execution proof of concept: scripts run
locally on the selected Mac account (`tomasz.walczuk`) or remotely on the
selected Ubuntu account (`ubuntu`). Direct remote access uses public TLS 1.3
mTLS at `https://129.151.232.40:8443`; the optional queued route uses a
separately authorized restricted SSH bridge. There are no containers, tunnels,
or interactive PTYs in this PoC.

The post-implementation documentation is indexed in [docs/README.md](docs/README.md):

- [Architecture](docs/architecture.md)
- [Configuration reference](docs/configuration.md)
- [Setup runbook](docs/setup.md)
- [CLI user guide](docs/user-guide.md)
- [Mailbox guide](docs/mailbox.md)
- [API reference](docs/api.md)
- [Operations runbook](docs/operations.md)
