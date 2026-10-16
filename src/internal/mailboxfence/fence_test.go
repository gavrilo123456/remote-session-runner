package mailboxfence

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestAcquireRetirementFencesEmptyIngressAndRejectsNativePublisher(t *testing.T) {
	trustRoot := t.TempDir()
	root := newFenceMailboxTree(t, trustRoot)

	fence, err := AcquireRetirementUnder(context.Background(), trustRoot, root)
	if err != nil {
		t.Fatalf("acquire retirement fence: %v", err)
	}
	defer fence.Close()
	for _, path := range []string{filepath.Join(root, "inbox"), filepath.Join(root, "acks")} {
		info, err := os.Lstat(path)
		if err != nil || info.Mode().Perm() != 0o500 {
			t.Fatalf("fenced ingress path=%s info=%v err=%v, want mode 0500", path, info, err)
		}
	}
	if os.Geteuid() != 0 {
		for _, path := range []string{filepath.Join(root, "inbox", "req-raw-after-fence.json"), filepath.Join(root, "acks", "req-raw-after-fence.json")} {
			if err := os.WriteFile(path, []byte("{}"), 0o600); err == nil {
				t.Fatalf("ordinary raw publisher wrote through frozen ingress path=%s", path)
			}
		}
	}
	info, err := os.Lstat(filepath.Join(root, SentinelFileName))
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 || info.Size() != 0 {
		t.Fatalf("retirement sentinel info=%v err=%v", info, err)
	}
	if err := fence.Close(); err != nil {
		t.Fatal(err)
	}
	if shared, err := AcquireShared(context.Background(), root); !errors.Is(err, ErrRetired) || shared != nil {
		if shared != nil {
			_ = shared.Close()
		}
		t.Fatalf("native publisher lock=%v err=%v, want retired", shared, err)
	}
	// A repair retry accepts the retained sentinel and frozen directory modes.
	retry, err := AcquireRetirementUnder(context.Background(), trustRoot, root)
	if err != nil {
		t.Fatalf("retry retirement fence: %v", err)
	}
	if err := retry.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestAcquireRetirementRestoresFreshFenceWhenIngressHasResidue(t *testing.T) {
	for _, fixture := range []struct {
		name      string
		directory string
		entry     string
	}{
		{name: "inbox JSON draft", directory: "inbox", entry: "req-fence-residue.json"},
		{name: "ACK ready marker", directory: "acks", entry: "req-fence-residue.ready"},
		{name: "arbitrary inbox artifact", directory: "inbox", entry: ".publisher-temporary"},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			trustRoot := t.TempDir()
			root := newFenceMailboxTree(t, trustRoot)
			if err := os.WriteFile(filepath.Join(root, fixture.directory, fixture.entry), []byte("{}"), 0o600); err != nil {
				t.Fatal(err)
			}
			if fence, err := AcquireRetirementUnder(context.Background(), trustRoot, root); !errors.Is(err, ErrResidueRestored) || fence != nil {
				if fence != nil {
					_ = fence.Close()
				}
				t.Fatalf("residual ingress error=%v fence=%v, want %v", err, fence, ErrResidueRestored)
			}
			for _, path := range []string{filepath.Join(root, "inbox"), filepath.Join(root, "acks")} {
				info, err := os.Lstat(path)
				if err != nil || info.Mode().Perm() != 0o700 {
					t.Fatalf("fresh residue rollback path=%s info=%v err=%v, want mode 0700", path, info, err)
				}
			}
			if _, err := os.Stat(filepath.Join(root, SentinelFileName)); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("fresh residue rollback retained sentinel err=%v", err)
			}
			if _, err := os.Stat(filepath.Join(root, fixture.directory, fixture.entry)); err != nil {
				t.Fatalf("fresh residue rollback removed ingress entry: %v", err)
			}
			shared, err := AcquireShared(context.Background(), root)
			if err != nil {
				t.Fatalf("fresh residue rollback still blocked native publisher: %v", err)
			}
			if err := shared.Close(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestAcquireRetirementRetainsPreexistingFenceWhenIngressHasResidue(t *testing.T) {
	trustRoot := t.TempDir()
	root := newFenceMailboxTree(t, trustRoot)
	fence, err := AcquireRetirementUnder(context.Background(), trustRoot, root)
	if err != nil {
		t.Fatal(err)
	}
	if err := fence.Close(); err != nil {
		t.Fatal(err)
	}
	inbox := filepath.Join(root, "inbox")
	if err := os.Chmod(inbox, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(inbox, "req-preexisting-residue.ready"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(inbox, 0o500); err != nil {
		t.Fatal(err)
	}
	if retry, err := AcquireRetirementUnder(context.Background(), trustRoot, root); !errors.Is(err, ErrResidue) || errors.Is(err, ErrResidueRestored) || retry != nil {
		if retry != nil {
			_ = retry.Close()
		}
		t.Fatalf("preexisting fence residue retry=%v err=%v, want retained residue fence", retry, err)
	}
	for _, path := range []string{filepath.Join(root, "inbox"), filepath.Join(root, "acks")} {
		info, err := os.Lstat(path)
		if err != nil || info.Mode().Perm() != 0o500 {
			t.Fatalf("preexisting fence path=%s info=%v err=%v, want mode 0500", path, info, err)
		}
	}
	if shared, err := AcquireShared(context.Background(), root); !errors.Is(err, ErrRetired) || shared != nil {
		if shared != nil {
			_ = shared.Close()
		}
		t.Fatalf("preexisting residue fence let native publisher through: lock=%v err=%v", shared, err)
	}
}

func TestAcquireSharedReturnsBusyForContendedFence(t *testing.T) {
	trustRoot := t.TempDir()
	root := newFenceMailboxTree(t, trustRoot)
	fence, err := AcquireRetirementUnder(context.Background(), trustRoot, root)
	if err != nil {
		t.Fatal(err)
	}
	defer fence.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer cancel()
	if shared, err := AcquireShared(ctx, root); !errors.Is(err, ErrBusy) || shared != nil {
		if shared != nil {
			_ = shared.Close()
		}
		t.Fatalf("contended shared lock=%v err=%v, want %v", shared, err, ErrBusy)
	}
}

func TestAcquireSharedNormalizesRestrictiveOwnerLockMode(t *testing.T) {
	trustRoot := t.TempDir()
	root := newFenceMailboxTree(t, trustRoot)
	lockPath := filepath.Join(root, LockFileName)
	if err := os.WriteFile(lockPath, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(lockPath, 0o400); err != nil {
		t.Fatal(err)
	}
	shared, err := AcquireShared(context.Background(), root)
	if err != nil {
		t.Fatalf("acquire shared through restrictive owner lock: %v", err)
	}
	if err := shared.Close(); err != nil {
		t.Fatal(err)
	}
	info, err := os.Lstat(lockPath)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("normalized lock info=%v err=%v, want mode 0600", info, err)
	}
}

func TestFenceMailboxFixturePrivatizesGroupWritableTrustRoot(t *testing.T) {
	trustRoot := t.TempDir()
	// Go's t.TempDir child inherits the caller's umask. Model the 0002
	// Linux test environment without changing the process-wide test umask.
	if err := os.Chmod(trustRoot, 0o775); err != nil {
		t.Fatal(err)
	}
	root := newFenceMailboxTree(t, trustRoot)
	info, err := os.Lstat(trustRoot)
	if err != nil || info.Mode().Perm() != 0o700 {
		t.Fatalf("fixture trust root info=%v err=%v, want mode 0700", info, err)
	}
	if err := os.Chmod(trustRoot, 0o775); err != nil {
		t.Fatal(err)
	}
	if fence, err := AcquireRetirementUnder(context.Background(), trustRoot, root); !errors.Is(err, ErrUnsafe) || fence != nil {
		if fence != nil {
			_ = fence.Close()
		}
		t.Fatalf("unsafe ancestor fence=%v err=%v, want ErrUnsafe", fence, err)
	}
	if err := os.Chmod(trustRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	fence, err := AcquireRetirementUnder(context.Background(), trustRoot, root)
	if err != nil {
		t.Fatalf("retirement under normalized private fixture: %v", err)
	}
	if err := fence.Close(); err != nil {
		t.Fatal(err)
	}
}

func newFenceMailboxTree(t *testing.T, trustRoot string) string {
	t.Helper()
	// This is a test-owned trust boundary, not an external mailbox ancestor.
	// Make its safety independent of the shell's group-writable umask.
	if err := os.Chmod(trustRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	parent := filepath.Join(trustRoot, "project")
	if err := os.Mkdir(parent, 0o700); err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(parent, "mailbox")
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"inbox", "acks", "outbox", "events", "diagnostics"} {
		if err := os.Mkdir(filepath.Join(root, name), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		for _, name := range []string{"inbox", "acks"} {
			_ = os.Chmod(filepath.Join(root, name), 0o700)
		}
	})
	return root
}
