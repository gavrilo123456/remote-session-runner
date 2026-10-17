package laneinstall

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"remote-session-runner/src/internal/config"
)

func TestSimpleQueuedSSHUsesExistingPort22AndLaneBoundForcedCommand(t *testing.T) {
	for _, host := range []string{"linux-host", "sandbox-host", "factory-sandbox"} {
		for _, lane := range []config.ReleaseLane{config.ReleaseLaneDev, config.ReleaseLaneQAS} {
			plan, err := BuildSimpleQueuedSSHPlan(host, lane, l202PublicKey("simple-dispatcher-"+host+"-"+string(lane)), l202PublicKey("existing-host-key-"+host))
			if err != nil {
				t.Fatalf("BuildSimpleQueuedSSHPlan(%s/%s): %v", host, lane, err)
			}
			if plan.Port != 22 || plan.Profile != host || plan.PublicHost == "" ||
				plan.AuthorizedKeysPath != "/home/ubuntu/.ssh/authorized_keys" ||
				strings.Contains(plan.ForcedWrapperPath, "2224") || strings.Contains(plan.ForcedWrapperPath, "2225") {
				t.Fatalf("simple queued route is not bound to the existing listener: %+v", plan)
			}
			wrapper := plan.LinuxArtifacts[0].Contents
			for _, required := range []string{"runner-ssh-bridge --stdio", "SSH_ORIGINAL_COMMAND", "SSH_TTY", "--authenticated-key", plan.ControllerMapPath, "--runnerd-socket", "current/bin/runner-ssh-bridge"} {
				if !strings.Contains(wrapper, required) {
					t.Errorf("%s/%s forced wrapper omits %q", host, lane, required)
				}
			}
			for _, forbidden := range []string{"/etc/ssh/sshd_config", "systemctl", "sshd -D", "--shell", "Slice="} {
				if strings.Contains(wrapper, forbidden) {
					t.Errorf("%s/%s forced wrapper contains excluded behavior %q", host, lane, forbidden)
				}
			}
			if !strings.HasPrefix(plan.LinuxArtifacts[2].Contents, `restrict,command="`+plan.ForcedWrapperPath+" ") ||
				!strings.HasSuffix(plan.LinuxArtifacts[2].Contents, "\n") ||
				!strings.Contains(plan.LinuxArtifacts[1].Contents, "controller_type: queued_mac") ||
				!strings.Contains(plan.LinuxArtifacts[1].Contents, "controller_id: \""+config.MacAccount+"\"") {
				t.Fatalf("lane key mapping or fixed wrapper command is malformed: %+v", plan.LinuxArtifacts)
			}
			if !strings.HasPrefix(plan.MacArtifacts[0].Contents, "["+plan.PublicHost+"]:22 ssh-ed25519 ") {
				t.Fatalf("known_hosts does not pin existing port 22: %q", plan.MacArtifacts[0].Contents)
			}
		}
	}
}

func TestSimpleQueuedAuthorizedKeyAppendPreservesAndBacksUpExistingEntries(t *testing.T) {
	root := simpleTestTempDir(t)
	sshDirectory := filepath.Join(root, ".ssh")
	backups := filepath.Join(root, "backups")
	if err := os.Mkdir(sshDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(backups, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(sshDirectory, "authorized_keys")
	existing := []byte("# retained production keys\nrestrict,command=\"/existing/prd-wrapper SHA256:old\" ssh-ed25519 AAAAold prd-key\nssh-ed25519 AAAAunrelated unrelated\n")
	if err := os.WriteFile(path, existing, 0o600); err != nil {
		t.Fatal(err)
	}
	plan, err := BuildSimpleQueuedSSHPlan("linux-host", config.ReleaseLaneDev, l202PublicKey("append-dispatcher"), l202PublicKey("existing-ssh-host"))
	if err != nil {
		t.Fatal(err)
	}
	line := plan.LinuxArtifacts[2].Contents
	if err := appendSimpleAuthorizedKeyAt(path, line, backups); err != nil {
		t.Fatal(err)
	}
	updated, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(updated), string(existing)) || !strings.HasSuffix(string(updated), line) {
		t.Fatalf("append changed or dropped existing SSH entries:\n%s", updated)
	}
	entries, err := os.ReadDir(backups)
	if err != nil || len(entries) != 1 {
		t.Fatalf("authorized_keys backup entries=%v err=%v", entries, err)
	}
	backup, err := os.ReadFile(filepath.Join(backups, entries[0].Name()))
	if err != nil || string(backup) != string(existing) {
		t.Fatalf("authorized_keys backup does not preserve original bytes: %q err=%v", backup, err)
	}
	if err := appendSimpleAuthorizedKeyAt(path, line, backups); err != nil {
		t.Fatalf("identical route retry failed: %v", err)
	}
	updatedAgain, _ := os.ReadFile(path)
	if string(updatedAgain) != string(updated) {
		t.Fatal("idempotent retry duplicated the lane key")
	}
}

func TestSimpleQueuedAuthorizedKeyAppendRejectsWidenedSameIdentityAndSymlink(t *testing.T) {
	root := simpleTestTempDir(t)
	sshDirectory := filepath.Join(root, ".ssh")
	backups := filepath.Join(root, "backups")
	if err := os.Mkdir(sshDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(backups, 0o700); err != nil {
		t.Fatal(err)
	}
	plan, err := BuildSimpleQueuedSSHPlan("factory-sandbox", config.ReleaseLaneQAS, l202PublicKey("widened-dispatcher"), l202PublicKey("existing-ssh-host"))
	if err != nil {
		t.Fatal(err)
	}
	line := strings.TrimSuffix(plan.LinuxArtifacts[2].Contents, "\n")
	widened := strings.Replace(line, "restrict,", "restrict,pty,", 1)
	path := filepath.Join(sshDirectory, "authorized_keys")
	if err := os.WriteFile(path, []byte(widened+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := appendSimpleAuthorizedKeyAt(path, plan.LinuxArtifacts[2].Contents, backups); !errors.Is(err, ErrSimpleArtifactConflict) {
		t.Fatalf("widened matching key error=%v", err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(root, "elsewhere")
	if err := os.WriteFile(target, []byte("must not be followed"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, path); err != nil {
		t.Fatal(err)
	}
	if err := appendSimpleAuthorizedKeyAt(path, plan.LinuxArtifacts[2].Contents, backups); !errors.Is(err, ErrSimpleArtifactConflict) {
		t.Fatalf("symlink authorized_keys error=%v", err)
	}
}
