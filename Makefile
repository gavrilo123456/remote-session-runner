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

.PHONY: check-go test vet build smoke check

check-go:
	@test -x "$(GO)" || { printf 'Go toolchain not executable: %s\n' "$(GO)" >&2; exit 1; }
	@actual=$$("$(GO)" version); \
	case "$$actual" in \
	  "go version go$(GO_VERSION) "*) ;; \
	  *) printf 'Expected Go %s, got: %s\n' "$(GO_VERSION)" "$$actual" >&2; exit 1 ;; \
	esac

test: check-go
	GOTOOLCHAIN=local "$(GO)" test ./...

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
