package runnerlocal

import (
	"path/filepath"
	"testing"

	"remote-session-runner/src/internal/config"
	"remote-session-runner/src/internal/store"
)

func TestSimpleMacServiceRootAcceptsOnlyCanonicalReleaseLanes(t *testing.T) {
	for _, lane := range []config.ReleaseLane{config.ReleaseLanePRD, config.ReleaseLaneDev, config.ReleaseLaneQAS} {
		namespace, err := config.DeriveSimpleLaneNamespace(config.HostKindMac, lane, "")
		if err != nil {
			t.Fatal(err)
		}
		if !isCanonicalMacServiceRoot(namespace.ServiceRoot) {
			t.Errorf("canonical %s root %q was rejected", lane, namespace.ServiceRoot)
		}
	}
	for _, path := range []string{
		"/tmp/RemoteSessionRunner-dev",
		config.MacServiceRoot + "-other",
		config.MacServiceRoot + "/../RemoteSessionRunner-dev",
	} {
		if isCanonicalMacServiceRoot(path) {
			t.Errorf("noncanonical service root %q was accepted", path)
		}
	}
}

func TestSimpleMacMailboxBaselineUsesSelectedLaneRoot(t *testing.T) {
	for _, lane := range []config.ReleaseLane{config.ReleaseLanePRD, config.ReleaseLaneDev, config.ReleaseLaneQAS} {
		namespace, err := config.DeriveSimpleLaneNamespace(config.HostKindMac, lane, "")
		if err != nil {
			t.Fatal(err)
		}
		definitions := []config.MailboxDefinition{
			{ID: store.DefaultMailboxID, Root: namespace.MailboxRoot},
			{ID: "acceptance", Root: filepath.Join(namespace.ServiceRoot, "mailboxes", "acceptance")},
		}
		got := configuredMailboxBaseline(definitions)
		if len(got) != 1 || got[0].ID != store.DefaultMailboxID || got[0].Root != namespace.MailboxRoot {
			t.Errorf("%s mailbox baseline=%+v, want selected default root %q", lane, got, namespace.MailboxRoot)
		}
	}
	if got := legacyDefaultMailboxBaseline(); len(got) != 1 || got[0].Root != filepath.Join(config.MacServiceRoot, "mailbox") {
		t.Fatalf("legacy mailbox baseline=%+v no longer preserves PRD", got)
	}
}
