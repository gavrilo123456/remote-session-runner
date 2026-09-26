package sshdeploy

import (
	"errors"
	"strings"
	"testing"
)

const p055PublicKey = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIBYMjC61Pqv/NVxaR86CMLlNBmFw8Oy3TBpIQoBObzka runner-mac-dispatcher"

func p055Entry() Entry {
	return Entry{PublicKey: p055PublicKey, BridgePath: "/home/ubuntu/.local/share/remote-session-runner/bin/runner-ssh-bridge"}
}

func TestP055RenderRestrictedAuthorizedKeyAndExactOriginalCommand(t *testing.T) {
	entry := p055Entry()
	rendered, err := entry.Render()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(rendered, `restrict,command="/home/ubuntu/.local/share/remote-session-runner/bin/runner-ssh-bridge --stdio" ssh-ed25519 `) || !strings.HasSuffix(rendered, " runner-mac-dispatcher") {
		t.Fatalf("rendered entry = %q", rendered)
	}
	if err := ValidateRendered(rendered, entry); err != nil {
		t.Fatal(err)
	}
	command, err := entry.FixedCommand()
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateOriginalCommand(command, entry); err != nil {
		t.Fatal(err)
	}
	for _, forged := range []string{"sh", "runner-ssh-bridge --stdio --shell", command + " extra", ""} {
		if !errors.Is(ValidateOriginalCommand(forged, entry), ErrOriginalCommand) {
			t.Fatalf("forged command %q was accepted", forged)
		}
	}
}

func TestP055RejectsOptionWideningMalformedKeysAndUnsafePaths(t *testing.T) {
	entry := p055Entry()
	rendered, err := entry.Render()
	if err != nil {
		t.Fatal(err)
	}
	for _, widened := range []string{
		strings.Replace(rendered, "restrict,", "no-pty,", 1),
		strings.Replace(rendered, "restrict,", "restrict,permitopen=example:22,", 1),
		strings.Replace(rendered, "restrict,", "restrict,pty,", 1),
		rendered + " extra",
	} {
		if !errors.Is(ValidateRendered(widened, entry), ErrInvalidEntry) {
			t.Fatalf("widened entry accepted: %q", widened)
		}
	}
	for _, invalid := range []Entry{
		{PublicKey: "ssh-rsa AAAA key", BridgePath: entry.BridgePath},
		{PublicKey: "ssh-ed25519 !!! key", BridgePath: entry.BridgePath},
		{PublicKey: p055PublicKey, BridgePath: "runner-ssh-bridge"},
		{PublicKey: p055PublicKey, BridgePath: "/home/ubuntu/bin/bridge;sh"},
		{PublicKey: p055PublicKey, BridgePath: "/home/ubuntu/bin/bridge,other"},
	} {
		if _, err := invalid.Render(); err == nil {
			t.Fatalf("invalid entry rendered: %+v", invalid)
		}
	}
}
