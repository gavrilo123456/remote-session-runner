GO_VERSION := 1.27.1
COMMANDS := runner runner-local runner-locald runnerd runner-ssh-bridge runner-session-agent

UNAME_S := $(shell uname -s)
ifeq ($(origin GO),undefined)
ifeq ($(UNAME_S),Darwin)
GO := /Users/tomasz.walczuk/Library/Application Support/RemoteSessionRunner/toolchains/go$(GO_VERSION)/bin/go
else ifeq ($(UNAME_S),Linux)
GO := /home/ubuntu/.local/share/remote-session-runner/toolchains/go$(GO_VERSION)/bin/go
else
GO := go
endif
endif

.PHONY: check-go test test-twohost test-p123-twohost test-p124-cli-twohost vet build smoke check

check-go:
	@test -x "$(GO)" || { printf 'Go toolchain not executable: %s\n' "$(GO)" >&2; exit 1; }
	@actual=$$("$(GO)" version); \
	case "$$actual" in \
	  "go version go$(GO_VERSION) "*) ;; \
	  *) printf 'Expected Go %s, got: %s\n' "$(GO_VERSION)" "$$actual" >&2; exit 1 ;; \
	esac

test: check-go
	GOTOOLCHAIN=local "$(GO)" test ./...

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
	: "$${RUNNER_P124_SSH_KNOWN_HOSTS:?set RUNNER_P124_SSH_KNOWN_HOSTS to the pinned Ubuntu host file}"; \
	RSR_P124_HOST_GATE=1 GOTOOLCHAIN=local "$(GO)" test -tags=p124twohost ./src/internal/localapi -run '^TestP124CommonCLISmokeAcrossRoutes$$' -count=1 -v

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
