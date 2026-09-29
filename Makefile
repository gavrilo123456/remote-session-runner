GO_VERSION := 1.27.1
COMMANDS := runner runner-local runner-locald runnerd runner-ssh-bridge runner-session-agent

UNAME_S := $(shell uname -s)
ifeq ($(UNAME_S),Darwin)
GOCACHE ?= /private/tmp/remote-session-runner-gocache
GOMODCACHE ?= /private/tmp/remote-session-runner-gomodcache
export GOCACHE GOMODCACHE
endif
ifeq ($(origin GO),undefined)
ifeq ($(UNAME_S),Darwin)
GO := /Users/tomasz.walczuk/Library/Application Support/RemoteSessionRunner/toolchains/go$(GO_VERSION)/bin/go
else ifeq ($(UNAME_S),Linux)
GO := /home/ubuntu/.local/share/remote-session-runner/toolchains/go$(GO_VERSION)/bin/go
else
GO := go
endif
endif

.PHONY: check-go test test-p133-barrier-harness test-twohost test-p123-twohost test-p124-cli-twohost test-p125-macos-services test-p126-linux-services test-p127-audit-twohost test-p127-host-audit-read test-p128-ops-mac test-p128-host-status test-p128-host-readonly-health test-p128-ops-ubuntu test-p129-ops-mac test-p129-ops-ubuntu test-p131-macos-shutdown test-p132-linux-shutdown test-p132-host-read test-p132-host-cleanup vet build smoke check

check-go:
	@test -x "$(GO)" || { printf 'Go toolchain not executable: %s\n' "$(GO)" >&2; exit 1; }
	@actual=$$("$(GO)" version); \
	case "$$actual" in \
	  "go version go$(GO_VERSION) "*) ;; \
	  *) printf 'Expected Go %s, got: %s\n' "$(GO_VERSION)" "$$actual" >&2; exit 1 ;; \
	esac

test: check-go
	GOTOOLCHAIN=local "$(GO)" test ./...

# Hermetic P133 subprocess barriers, WAL-aware logical database snapshots,
# exact child kill/reap, and restart assertions.
test-p133-barrier-harness: check-go
	GOTOOLCHAIN=local "$(GO)" test ./src/internal/testfixture -run '^TestP133' -count=1 -v

# Public endpoint validation is deliberately opt-in and requires owner-only
# credential fixtures outside the repository. The test is pinned to the
# selected public IP and never substitutes a loopback listener.
test-twohost: check-go
	@set -eu; \
	: "$${RUNNER_P117_SERVER_CA:?set RUNNER_P117_SERVER_CA to the trusted server CA file}"; \
	: "$${RUNNER_P117_CLIENT_CERT:?set RUNNER_P117_CLIENT_CERT}"; \
	: "$${RUNNER_P117_CLIENT_KEY:?set RUNNER_P117_CLIENT_KEY}"; \
	: "$${RUNNER_P117_OTHER_CLIENT_CERT:?set RUNNER_P117_OTHER_CLIENT_CERT}"; \
	: "$${RUNNER_P117_OTHER_CLIENT_KEY:?set RUNNER_P117_OTHER_CLIENT_KEY}"; \
	: "$${RUNNER_P117_EXPIRED_CLIENT_CERT:?set RUNNER_P117_EXPIRED_CLIENT_CERT}"; \
	: "$${RUNNER_P117_EXPIRED_CLIENT_KEY:?set RUNNER_P117_EXPIRED_CLIENT_KEY}"; \
	: "$${RUNNER_P117_UNTRUSTED_CLIENT_CERT:?set RUNNER_P117_UNTRUSTED_CLIENT_CERT}"; \
	: "$${RUNNER_P117_UNTRUSTED_CLIENT_KEY:?set RUNNER_P117_UNTRUSTED_CLIENT_KEY}"; \
	: "$${RUNNER_P117_UNMAPPED_CLIENT_CERT:?set RUNNER_P117_UNMAPPED_CLIENT_CERT}"; \
	: "$${RUNNER_P117_UNMAPPED_CLIENT_KEY:?set RUNNER_P117_UNMAPPED_CLIENT_KEY}"; \
	GOTOOLCHAIN=local "$(GO)" test -tags=p117twohost ./src/internal/runnerd -run '^TestP117PublicEndpointPNET02$$' -count=1 -v

# Real Mac/Ubuntu disconnect and durable-cursor replay gate. Credentials and
# the temporary Ubuntu runner/forced-command fixture are provisioned outside
# Git; the two test packages run sequentially against the same Linux authority.
test-p123-twohost: check-go
	@set -eu; \
	: "$${RUNNER_P123_SERVER_CA:?set RUNNER_P123_SERVER_CA to the trusted server CA file}"; \
	: "$${RUNNER_P123_CLIENT_CERT:?set RUNNER_P123_CLIENT_CERT}"; \
	: "$${RUNNER_P123_CLIENT_KEY:?set RUNNER_P123_CLIENT_KEY}"; \
	: "$${RUNNER_P123_SSH_IDENTITY:?set RUNNER_P123_SSH_IDENTITY to the dedicated dispatcher key}"; \
	: "$${RUNNER_P123_SSH_KNOWN_HOSTS:?set RUNNER_P123_SSH_KNOWN_HOSTS to the pinned host file}"; \
	RSR_P123_HOST_GATE=1 GOTOOLCHAIN=local "$(GO)" test -tags=p123twohost ./src/internal/runnerd -run '^TestP123DirectDisconnectReplaysWithoutRerun$$' -count=1 -v; \
	RSR_P123_HOST_GATE=1 GOTOOLCHAIN=local "$(GO)" test -tags=p123twohost ./src/internal/localapi -run '^TestP123QueuedProjectionReconcilesAfterSSHOutage$$' -count=1 -v

# Real Mac CLI smoke through local, queued SSH, and public direct mTLS routes.
# The Ubuntu Runner and temporary queued-key authorization are host fixtures.
test-p124-cli-twohost: check-go
	@set -eu; \
	: "$${RUNNER_P124_SERVER_CA:?set RUNNER_P124_SERVER_CA to the trusted server CA file}"; \
	: "$${RUNNER_P124_CLIENT_CERT:?set RUNNER_P124_CLIENT_CERT to the direct mTLS certificate}"; \
	: "$${RUNNER_P124_CLIENT_KEY:?set RUNNER_P124_CLIENT_KEY to the direct mTLS private key}"; \
	: "$${RUNNER_P124_SSH_IDENTITY:?set RUNNER_P124_SSH_IDENTITY to the dedicated dispatcher key}"; \
	: "$${RUNNER_P124_INSPECT_SSH_IDENTITY:?set RUNNER_P124_INSPECT_SSH_IDENTITY to the separate host-inspection identity}"; \
	: "$${RUNNER_P124_SSH_KNOWN_HOSTS:?set RUNNER_P124_SSH_KNOWN_HOSTS to the pinned Ubuntu host file}"; \
	RSR_P124_HOST_GATE=1 GOTOOLCHAIN=local "$(GO)" test -tags=p124twohost ./src/internal/localapi -run '^TestP124CommonCLISmokeAcrossRoutes$$' -count=1 -v

# Real Mac local/queued and Ubuntu direct mTLS audit rows/logs. It
# inspects only safe audit columns through a separate Ubuntu inspection identity.
test-p127-audit-twohost: check-go
	@set -eu; \
	: "$${RUNNER_P124_SERVER_CA:?set RUNNER_P124_SERVER_CA}"; \
	: "$${RUNNER_P124_CLIENT_CERT:?set RUNNER_P124_CLIENT_CERT}"; \
	: "$${RUNNER_P124_CLIENT_KEY:?set RUNNER_P124_CLIENT_KEY}"; \
	: "$${RUNNER_P124_SSH_IDENTITY:?set RUNNER_P124_SSH_IDENTITY}"; \
	: "$${RUNNER_P124_INSPECT_SSH_IDENTITY:?set RUNNER_P124_INSPECT_SSH_IDENTITY}"; \
	: "$${RUNNER_P124_SSH_KNOWN_HOSTS:?set RUNNER_P124_SSH_KNOWN_HOSTS}"; \
	: "$${RUNNER_P117_SERVER_CA:?set RUNNER_P117_SERVER_CA}"; \
	: "$${RUNNER_P117_CLIENT_CERT:?set RUNNER_P117_CLIENT_CERT}"; \
	: "$${RUNNER_P117_CLIENT_KEY:?set RUNNER_P117_CLIENT_KEY}"; \
	: "$${RUNNER_P117_OTHER_CLIENT_CERT:?set RUNNER_P117_OTHER_CLIENT_CERT}"; \
	: "$${RUNNER_P117_OTHER_CLIENT_KEY:?set RUNNER_P117_OTHER_CLIENT_KEY}"; \
	RSR_P124_HOST_GATE=1 GOTOOLCHAIN=local "$(GO)" test -tags=p124twohost ./src/internal/localapi -run '^TestP124CommonCLISmokeAcrossRoutes$$' -count=1 -v; \
	RSR_P127_AUDIT_HOST_GATE=1 GOTOOLCHAIN=local "$(GO)" test -tags=p117twohost ./src/internal/runnerd -run '^TestP127UbuntuAuditRowsAndJournalForDirectIngress$$' -count=1 -v

# Read-only, bounded audit inspection on Ubuntu using the Runner Go SQLite
# driver. This avoids depending on the distro Python SQLite version.
test-p127-host-audit-read: check-go
	@set -eu; \
	: "$${RSR_P127_HOST_AUDIT_MODE:?set to highwater or rows}"; \
	: "$${RSR_P127_HOST_AUDIT_AFTER_ID:?set to a nonnegative audit ID}"; \
	RSR_P127_HOST_AUDIT_READ=1 GOTOOLCHAIN=local "$(GO)" test -tags=p127hostreader ./src/internal/store -run '^TestP127HostAuditReadHelper$$' -count=1 -v

# Actual Mac account plus pinned Ubuntu bridge probe, followed by an injected
# SSH outage while durable local and queued-remote ingress remain available.
test-p128-ops-mac: check-go
	@GO='$(GO)' deploy/macos/test-ops-health.sh

test-p128-host-status: check-go
	RSR_P128_HOST_STATUS=1 GOTOOLCHAIN=local "$(GO)" test -tags=p128hoststatus ./src/internal/store -run '^TestP128UbuntuNoActiveWorkBeforeRestart$$' -count=1 -v

test-p128-host-readonly-health: check-go
	RSR_P128_HOST_STATUS=1 GOTOOLCHAIN=local "$(GO)" test -tags=p128hoststatus ./src/internal/store -run '^TestP128UbuntuHealthWriteProbeRejectsReadOnlyDatabase$$' -count=1 -v

test-p128-ops-ubuntu: check-go
	@deploy/linux/test-ops-health.sh

# P129 operational metrics host gates, including the P128 host regressions.
test-p129-ops-mac: check-go
	@GO='$(GO)' deploy/macos/test-ops-health.sh

test-p129-ops-ubuntu: check-go
	@deploy/linux/test-ops-health.sh

# Real Mac launchd lifecycle, owner-only path, Unix socket, and mailbox smoke.
test-p125-macos-services: check-go
	@deploy/macos/test-launchagents.sh

# Real Mac launchd process gate for graceful drain, audit/event persistence, and cursor resume.
test-p131-macos-shutdown: check-go
	@GO='$(GO)' deploy/macos/test-graceful-shutdown.sh

# Real Ubuntu systemd graceful shutdown with a live host-process command.
# The test runs on the Mac using the selected public mTLS identity and pinned
# Ubuntu inspection key; Ubuntu source is consumed only after Git fast-forward.
test-p132-linux-shutdown: check-go
	@set -eu; \
	: "$${RUNNER_P117_SERVER_CA:?set RUNNER_P117_SERVER_CA}"; \
	: "$${RUNNER_P117_CLIENT_CERT:?set RUNNER_P117_CLIENT_CERT}"; \
	: "$${RUNNER_P117_CLIENT_KEY:?set RUNNER_P117_CLIENT_KEY}"; \
	: "$${RUNNER_P124_INSPECT_SSH_IDENTITY:?set RUNNER_P124_INSPECT_SSH_IDENTITY}"; \
	: "$${RUNNER_P124_SSH_KNOWN_HOSTS:?set RUNNER_P124_SSH_KNOWN_HOSTS}"; \
	RSR_P132_HOST_GATE=1 RSR_P132_EXPECTED_COMMIT=$$(git rev-parse HEAD) GOTOOLCHAIN=local "$(GO)" test -tags=p117twohost ./src/internal/runnerd -run '^TestP132LinuxGracefulShutdownProcessHost$$' -count=1 -v

# Bounded read-only Runner state capture while runnerd.service is stopped.
test-p132-host-read: check-go
	@set -eu; \
	: "$${RSR_P132_HOST_SESSION_ID:?set RSR_P132_HOST_SESSION_ID}"; \
	: "$${RSR_P132_HOST_COMMAND_ID:?set RSR_P132_HOST_COMMAND_ID}"; \
	: "$${RSR_P132_HOST_AUDIT_AFTER_ID:?set RSR_P132_HOST_AUDIT_AFTER_ID}"; \
	RSR_P132_HOST_READ=1 GOTOOLCHAIN=local "$(GO)" test -tags=p132hostreader ./src/internal/store -run '^TestP132UbuntuHostCommandReader$$' -count=1 -v

# Narrow cleanup for a P132 test fixture only after the old service cgroup and
# the fixture's recorded command process have both been confirmed empty.
test-p132-host-cleanup: check-go
	@set -eu; \
	: "$${RSR_P132_HOST_SESSION_ID:?set RSR_P132_HOST_SESSION_ID}"; \
	: "$${RSR_P132_HOST_COMMAND_ID:?set RSR_P132_HOST_COMMAND_ID}"; \
	RSR_P132_HOST_CLEANUP=1 GOTOOLCHAIN=local "$(GO)" test -tags=p132hostcleanup ./src/internal/store -run '^TestP132UbuntuCleanupConfirmedLostFixture$$' -count=1 -v

# Real Ubuntu systemd lifecycle and owner-only service-path gate.
test-p126-linux-services: check-go
	@deploy/linux/test-systemd-service.sh

vet: check-go
	GOTOOLCHAIN=local "$(GO)" vet ./...

build: check-go
	GOTOOLCHAIN=local "$(GO)" build ./src/cmd/...

smoke: check-go
	@set -eu; \
	tmpdir=$$(mktemp -d "$${TMPDIR:-/tmp}/remote-session-runner-p001.XXXXXX"); \
	trap 'rm -rf "$$tmpdir"' EXIT HUP INT TERM; \
	for name in $(COMMANDS); do \
	  GOTOOLCHAIN=local "$(GO)" build -o "$$tmpdir/$$name" "./src/cmd/$$name"; \
	  "$$tmpdir/$$name" --help >/dev/null; \
	  "$$tmpdir/$$name" --version >/dev/null; \
	done

check: test vet smoke
