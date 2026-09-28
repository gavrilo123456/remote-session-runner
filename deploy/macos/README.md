# macOS launchd deployment

The Mac runs `runner-local` (Unix API, file mailbox, and Router) and
`runner-locald` (local execution authority) as `tomasz.walczuk` in the GUI
launchd domain. Commands use that account's operating-system permissions; the
workspace is not confinement.

From the repository root, run:

```sh
deploy/macos/install-launchagents.sh
```

The first installer run creates the selected host config from
`mac.yaml.example` when it is absent and exits without starting services.
Review that config, then rerun the installer. It contains selected paths and
controller/environment choices; private key contents remain in the external
`secrets/` directory. The installer builds the CLI and both Mac services with
the selected Go 1.27.1 toolchain, places executables under the service root,
validates and installs the versioned LaunchAgent plists, then bootstraps them
in the GUI launchd domain. The plists use explicit executable/config paths, an
owner-only umask, a fixed `PATH`, and logs beneath the private service root.

Stop or unload the jobs with:

```sh
launchctl bootout gui/501 "$HOME/Library/LaunchAgents/com.remote-session-runner.local.plist"
launchctl bootout gui/501 "$HOME/Library/LaunchAgents/com.remote-session-runner.locald.plist"
```

On SIGTERM, each service stops accepting new work, drains within an eight-second
budget, uses the normal runtime cancellation/close path if work remains, checks
the committed audit tail, then closes event streams and removes its Unix socket.
The shared cleanup stage is bounded to five seconds; the combined 13-second
limit fits inside each plist's 15-second `ExitTimeOut`. Event records committed
before shutdown remain available after restart, and clients resume after the
last sequence they received. Run `make test-p131-macos-shutdown` on the selected
Mac account to verify the real LaunchAgent stop and restart path.
