package laneinstall

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"remote-session-runner/src/internal/config"
	"remote-session-runner/src/internal/mailboxhandoff"
)

func TestMailboxHandoffArchiveAndReturnPreserveBothEpochs(t *testing.T) {
	op, entry, contents := handoffDirectoryFixture(t)
	if err := handoffArchive(op, entry); err != nil {
		t.Fatal(err)
	}
	prdArchive := filepath.Join(entry.Root, entry.ArchiveRelative)
	qasArchive := filepath.Join(entry.Root, ".runner-mailbox-handoffs", op.OperationID, "qas")
	handoffTestMkdir(t, qasArchive)
	current := make(map[string]handoffDirectory)
	for _, original := range op.Directories {
		archived := filepath.Join(prdArchive, original.Name)
		handoffTestIdentity(t, archived, original)
		handoffTestContents(t, filepath.Join(archived, "req-original.json"), contents[original.Name])
		live := filepath.Join(entry.Root, original.Name)
		files, err := os.ReadDir(live)
		if err != nil || len(files) != 0 {
			t.Fatalf("fresh %s is not empty: %v", original.Name, err)
		}
		dev, ino, err := handoffIdentity(live, true)
		if err != nil || dev == original.Device && ino == original.Inode {
			t.Fatalf("fresh %s reused archived identity: %v", original.Name, err)
		}
		current[original.Name] = handoffDirectory{Root: entry.Root, Name: original.Name, Device: dev, Inode: ino}
	}
	// A retry after all directory renames must neither replace an archive nor
	// rotate another new epoch. Each fresh directory retains its identity.
	if err := handoffArchive(op, entry); err != nil {
		t.Fatalf("retry archive: %v", err)
	}
	for _, original := range op.Directories {
		live := filepath.Join(entry.Root, original.Name)
		handoffTestIdentity(t, live, current[original.Name])
		qasBytes := []byte("qas/" + original.Name + "\n\x00retained\n")
		if err := os.WriteFile(filepath.Join(live, "req-qas.json"), qasBytes, 0o644); err != nil {
			t.Fatal(err)
		}
		if err := handoffRestoreDirectory(entry.Root, prdArchive, qasArchive, original, current[original.Name]); err != nil {
			t.Fatalf("restore %s: %v", original.Name, err)
		}
		handoffTestIdentity(t, live, original)
		handoffTestContents(t, filepath.Join(live, "req-original.json"), contents[original.Name])
		handoffTestIdentity(t, filepath.Join(qasArchive, original.Name), current[original.Name])
		handoffTestContents(t, filepath.Join(qasArchive, original.Name, "req-qas.json"), qasBytes)
		if err := handoffRestoreDirectory(entry.Root, prdArchive, qasArchive, original, current[original.Name]); err != nil {
			t.Fatalf("retry restored %s: %v", original.Name, err)
		}
	}
}

func TestMailboxHandoffMoveResumesAfterOriginalRename(t *testing.T) {
	op, entry, contents := handoffDirectoryFixture(t)
	original := op.Directories[0]
	archive := filepath.Join(entry.Root, entry.ArchiveRelative)
	handoffTestMkdir(t, archive)
	// Simulate interruption after the durable original rename but before
	// creation of the fresh ingress directory.
	if err := os.Rename(filepath.Join(entry.Root, original.Name), filepath.Join(archive, original.Name)); err != nil {
		t.Fatal(err)
	}
	if err := handoffMoveDirectory(entry.Root, archive, original); err != nil {
		t.Fatalf("resume after rename: %v", err)
	}
	handoffTestIdentity(t, filepath.Join(archive, original.Name), original)
	handoffTestContents(t, filepath.Join(archive, original.Name, "req-original.json"), contents[original.Name])
	files, err := os.ReadDir(filepath.Join(entry.Root, original.Name))
	if err != nil || len(files) != 0 {
		t.Fatalf("resume did not create an empty ingress: %v", err)
	}
}

func TestMailboxHandoffRestoreResumesAfterQASRename(t *testing.T) {
	op, entry, contents := handoffDirectoryFixture(t)
	if err := handoffArchive(op, entry); err != nil {
		t.Fatal(err)
	}
	original := op.Directories[0]
	live := filepath.Join(entry.Root, original.Name)
	dev, ino, err := handoffIdentity(live, true)
	if err != nil {
		t.Fatal(err)
	}
	current := handoffDirectory{Root: entry.Root, Name: original.Name, Device: dev, Inode: ino}
	qasBytes := []byte("preserve qas output\n")
	if err := os.WriteFile(filepath.Join(live, "req-qas.json"), qasBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	qasArchive := filepath.Join(entry.Root, ".runner-mailbox-handoffs", op.OperationID, "qas")
	handoffTestMkdir(t, qasArchive)
	if err := os.Rename(live, filepath.Join(qasArchive, original.Name)); err != nil {
		t.Fatal(err)
	}
	if err := handoffRestoreDirectory(entry.Root, filepath.Join(entry.Root, entry.ArchiveRelative), qasArchive, original, current); err != nil {
		t.Fatalf("resume return after QAS rename: %v", err)
	}
	handoffTestIdentity(t, live, original)
	handoffTestContents(t, filepath.Join(live, "req-original.json"), contents[original.Name])
	handoffTestIdentity(t, filepath.Join(qasArchive, original.Name), current)
	handoffTestContents(t, filepath.Join(qasArchive, original.Name, "req-qas.json"), qasBytes)
}

func TestMailboxHandoffMoveRejectsIdentityDriftAndUnexpectedEntries(t *testing.T) {
	for _, scenario := range []string{"source identity changed", "archive collision", "new live entry", "source symlink", "archive symlink"} {
		t.Run(scenario, func(t *testing.T) {
			op, entry, contents := handoffDirectoryFixture(t)
			original := op.Directories[0]
			archive := filepath.Join(entry.Root, entry.ArchiveRelative)
			handoffTestMkdir(t, archive)
			live := filepath.Join(entry.Root, original.Name)
			archived := filepath.Join(archive, original.Name)
			originalAt := live
			var conflictPath string
			switch scenario {
			case "source identity changed":
				originalAt = filepath.Join(entry.Root, "original-held")
				if err := os.Rename(live, originalAt); err != nil {
					t.Fatal(err)
				}
				handoffTestMkdir(t, live)
				conflictPath = filepath.Join(live, "new-entry.json")
			case "archive collision":
				handoffTestMkdir(t, archived)
				conflictPath = filepath.Join(archived, "new-entry.json")
			case "new live entry":
				if err := handoffMoveDirectory(entry.Root, archive, original); err != nil {
					t.Fatal(err)
				}
				originalAt = archived
				conflictPath = filepath.Join(live, "new-entry.json")
			case "source symlink":
				originalAt = filepath.Join(entry.Root, "original-held")
				if err := os.Rename(live, originalAt); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(originalAt, live); err != nil {
					t.Fatal(err)
				}
			case "archive symlink":
				if err := os.Symlink(live, archived); err != nil {
					t.Fatal(err)
				}
			}
			if conflictPath != "" {
				if err := os.WriteFile(conflictPath, []byte("do not overwrite"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if err := handoffMoveDirectory(entry.Root, archive, original); err == nil {
				t.Fatal("unsafe directory mutation succeeded")
			}
			handoffTestContents(t, filepath.Join(originalAt, "req-original.json"), contents[original.Name])
			if conflictPath != "" {
				handoffTestContents(t, conflictPath, []byte("do not overwrite"))
			}
		})
	}
}

func TestMailboxHandoffRestoreRejectsArchiveIdentityDrift(t *testing.T) {
	op, entry, contents := handoffDirectoryFixture(t)
	if err := handoffArchive(op, entry); err != nil {
		t.Fatal(err)
	}
	original := op.Directories[0]
	live := filepath.Join(entry.Root, original.Name)
	dev, ino, err := handoffIdentity(live, true)
	if err != nil {
		t.Fatal(err)
	}
	current := handoffDirectory{Root: entry.Root, Name: original.Name, Device: dev, Inode: ino}
	prdArchive := filepath.Join(entry.Root, entry.ArchiveRelative)
	qasArchive := filepath.Join(entry.Root, ".runner-mailbox-handoffs", op.OperationID, "qas")
	handoffTestMkdir(t, qasArchive)
	old := filepath.Join(prdArchive, original.Name)
	held := filepath.Join(prdArchive, "original-held")
	if err := os.Rename(old, held); err != nil {
		t.Fatal(err)
	}
	handoffTestMkdir(t, old)
	if err := handoffRestoreDirectory(entry.Root, prdArchive, qasArchive, original, current); err == nil {
		t.Fatal("return accepted an archive directory with a different identity")
	}
	handoffTestIdentity(t, live, current)
	handoffTestContents(t, filepath.Join(held, "req-original.json"), contents[original.Name])
}

func TestMailboxHandoffConfigCASNeverOverwritesUnrelatedBytes(t *testing.T) {
	path := filepath.Join(handoffTestTempDir(t), "mac.yaml")
	original, candidate, changed := []byte("original\n"), []byte("candidate\n"), []byte("unrelated operator change\n")
	if err := os.WriteFile(path, changed, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := handoffReplace(path, candidate, handoffSHA(original), handoffSHA(candidate)); err == nil {
		t.Fatal("config CAS overwrote an unexpected configuration")
	}
	handoffTestContents(t, path, changed)
	if err := os.WriteFile(path, original, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := handoffReplace(path, candidate, handoffSHA(original), handoffSHA(candidate)); err != nil {
		t.Fatal(err)
	}
	if err := handoffReplace(path, candidate, handoffSHA(original), handoffSHA(candidate)); err != nil {
		t.Fatalf("retry of completed config replacement: %v", err)
	}
	handoffTestContents(t, path, candidate)
	alias := filepath.Join(filepath.Dir(path), "symlink.yaml")
	if err := os.Symlink(path, alias); err != nil {
		t.Fatal(err)
	}
	if err := handoffReplace(alias, original, handoffSHA(candidate), handoffSHA(original)); err == nil {
		t.Fatal("config CAS followed a symlink")
	}
	handoffTestContents(t, path, candidate)
}

func TestMailboxHandoffArtifactReaderAllowsBoundedReadonlyClientFiles(t *testing.T) {
	root := handoffTestTempDir(t)
	path := filepath.Join(root, "req-original.json")
	raw := []byte(`{"request_id":"req-original","idempotency_key":"preserve-key"}`)
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	actual, err := handoffReadArtifact(path, int64(len(raw)))
	if err != nil || !bytes.Equal(actual, raw) {
		t.Fatalf("bounded owner-written 0644 artifact rejected: %v", err)
	}
	if _, err := handoffRead(path); err == nil {
		t.Fatal("private config/journal reader accepted 0644")
	}
	if _, err := handoffReadArtifact(path, int64(len(raw)-1)); err == nil {
		t.Fatal("artifact reader accepted an oversized record")
	}
	alias := filepath.Join(root, "symlink.json")
	if err := os.Symlink(path, alias); err != nil {
		t.Fatal(err)
	}
	if _, err := handoffReadArtifact(alias, int64(len(raw))); err == nil {
		t.Fatal("artifact reader followed a symlink")
	}
	if err := os.Chmod(path, 0o666); err != nil {
		t.Fatal(err)
	}
	if _, err := handoffReadArtifact(path, int64(len(raw))); err == nil {
		t.Fatal("artifact reader accepted a group/other-writable record")
	}
	handoffTestContents(t, path, raw)
}

func TestMailboxHandoffOperationRejectsUnsafeDirectoryJournal(t *testing.T) {
	for name, mutate := range map[string]func(*MailboxHandoffOperation){
		"traversal":         func(op *MailboxHandoffOperation) { op.Directories[0].Name = "../../outside" },
		"absolute name":     func(op *MailboxHandoffOperation) { op.Directories[0].Name = "/tmp/outside" },
		"unknown name":      func(op *MailboxHandoffOperation) { op.Directories[0].Name = "workspaces" },
		"duplicate name":    func(op *MailboxHandoffOperation) { op.Directories[1].Name = op.Directories[0].Name },
		"missing directory": func(op *MailboxHandoffOperation) { op.Directories = op.Directories[:len(op.Directories)-1] },
		"foreign root":      func(op *MailboxHandoffOperation) { op.Directories[0].Root = filepath.Dir(op.Directories[0].Root) },
		"missing identity":  func(op *MailboxHandoffOperation) { op.Directories[0].Inode = 0 },
		"foreign archive epoch": func(op *MailboxHandoffOperation) {
			op.Receipt.Mailboxes[0].ArchiveRelative = filepath.Join(".runner-mailbox-handoffs", strings.Repeat("b", 32), "prd")
		},
	} {
		t.Run(name, func(t *testing.T) {
			op, _, _ := handoffDirectoryFixture(t)
			prd, err := BuildPRDUpgradePlan("mac-control-plane")
			if err != nil {
				t.Fatal(err)
			}
			qas, err := BuildCatalogPlan("mac-control-plane", config.ReleaseLaneQAS)
			if err != nil {
				t.Fatal(err)
			}
			op.Directory = filepath.Join(prd.Namespace.Backups, "mailbox-handoff-"+op.OperationID)
			op.SourceConfig, op.TargetConfig = prd.Namespace.ConfigFile, qas.Namespace.ConfigFile
			op.OriginalSourceSHA256, op.OriginalTargetSHA256, op.DisabledSourceSHA256 = strings.Repeat("1", 64), strings.Repeat("2", 64), strings.Repeat("3", 64)
			op.ReceiptPath = filepath.Join(qas.Namespace.ServiceRoot, "staging", "mailbox-handoffs", op.OperationID+".json")
			op.Receipt.SourceServiceRoot, op.Receipt.TargetServiceRoot = prd.Namespace.ServiceRoot, qas.Namespace.ServiceRoot
			op.Receipt.SourceConfigSHA256, op.Receipt.TargetConfigSHA256 = op.OriginalSourceSHA256, strings.Repeat("4", 64)
			if err := validateMailboxHandoffOperation(op); err != nil {
				t.Fatalf("valid operation fixture rejected before mutation: %v", err)
			}
			mutate(&op)
			if err := validateMailboxHandoffOperation(op); err == nil {
				t.Fatal("unsafe operation directory journal accepted")
			}
		})
	}
}

func handoffDirectoryFixture(t *testing.T) (MailboxHandoffOperation, mailboxhandoff.Mailbox, map[string][]byte) {
	t.Helper()
	root := filepath.Join(handoffTestTempDir(t), "mailbox")
	handoffTestMkdir(t, root)
	dev, ino, err := handoffIdentity(root, true)
	if err != nil {
		t.Fatal(err)
	}
	id := strings.Repeat("a", 32)
	entry := mailboxhandoff.Mailbox{SourceID: "default", TargetID: "prd-default", Root: root, Device: dev, Inode: ino,
		ArchiveRelative: filepath.Join(".runner-mailbox-handoffs", id, "prd")}
	op := MailboxHandoffOperation{Version: 1, OperationID: id, Phase: "prepared", CreatedAt: time.Now().UTC().Format(time.RFC3339Nano), Owner: "fixture owner",
		Receipt: mailboxhandoff.Receipt{Version: 1, OperationID: id, SourceLane: "prd", TargetLane: "qas", Phase: "active", Mailboxes: []mailboxhandoff.Mailbox{entry}}}
	contents := make(map[string][]byte)
	for _, name := range handoffDirectories {
		path := filepath.Join(root, name)
		handoffTestMkdir(t, path)
		contents[name] = []byte("prd/" + name + "\n\x00preserve exact bytes\n")
		if err := os.WriteFile(filepath.Join(path, "req-original.json"), contents[name], 0o600); err != nil {
			t.Fatal(err)
		}
		d, i, err := handoffIdentity(path, true)
		if err != nil {
			t.Fatal(err)
		}
		op.Directories = append(op.Directories, handoffDirectory{Root: root, Name: name, Device: d, Inode: i})
	}
	return op, entry, contents
}

func handoffTestTempDir(t *testing.T) string {
	t.Helper()
	root := simpleTestTempDir(t)
	// testing.TempDir's numbered child uses 0777 subject to the host umask.
	// Match the private production roots even on hosts whose umask is 0002;
	// the production guard must keep rejecting writable parent directories.
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	return root
}

func handoffTestMkdir(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o700); err != nil {
		t.Fatal(err)
	}
}

func handoffTestIdentity(t *testing.T, path string, want handoffDirectory) {
	t.Helper()
	dev, ino, err := handoffIdentity(path, true)
	if err != nil || dev != want.Device || ino != want.Inode {
		t.Fatalf("identity changed at %s: %d/%d, want %d/%d, err=%v", path, dev, ino, want.Device, want.Inode, err)
	}
}

func handoffTestContents(t *testing.T, path string, want []byte) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(got, want) {
		t.Fatalf("contents changed at %s: got=%q want=%q err=%v", path, got, want, err)
	}
}
