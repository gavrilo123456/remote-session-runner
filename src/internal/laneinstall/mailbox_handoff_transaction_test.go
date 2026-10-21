package laneinstall

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"remote-session-runner/src/internal/mailboxhandoff"
	"remote-session-runner/src/internal/store"
)

var errHandoffFixtureInterrupted = errors.New("fixture interrupted after durable transaction phase")

type handoffTransactionFixture struct {
	op                       MailboxHandoffOperation
	deps                     mailboxHandoffTransactionDeps
	definitions              []mailboxhandoff.Definition
	prdContents, qasContents map[string][]byte
	databaseBytes            map[string][]byte
}

func newHandoffTransactionFixture(t *testing.T) handoffTransactionFixture {
	t.Helper()
	base := handoffTestTempDir(t)
	prdRoot, qasRoot := filepath.Join(base, "prd"), filepath.Join(base, "qas")
	for _, root := range []string{prdRoot, qasRoot} {
		for _, name := range []string{"config", "state", "backups", "staging", "mailbox"} {
			handoffTestMkdir(t, filepath.Join(root, name))
		}
	}
	id := strings.Repeat("a", 32)
	source, target, disabled, candidate := []byte("original PRD configuration\n"), []byte("original QAS configuration\n"), []byte("PRD ingress disabled\n"), []byte("QAS handoff configuration\n")
	op := MailboxHandoffOperation{
		Version: 1, OperationID: id, Phase: "prepared", CreatedAt: time.Now().UTC().Format(time.RFC3339Nano), Owner: "fixture owner",
		Directory: filepath.Join(prdRoot, "backups", "mailbox-handoff-"+id), SourceConfig: filepath.Join(prdRoot, "config", "mac.yaml"), TargetConfig: filepath.Join(qasRoot, "config", "mac.yaml"),
		OriginalSourceSHA256: handoffSHA(source), OriginalTargetSHA256: handoffSHA(target), DisabledSourceSHA256: handoffSHA(disabled),
		ReceiptPath: filepath.Join(qasRoot, "staging", "mailbox-handoffs", id+".json"),
		Receipt: mailboxhandoff.Receipt{Version: 1, OperationID: id, SourceLane: "prd", TargetLane: "qas", Phase: "active", SourceServiceRoot: prdRoot, TargetServiceRoot: qasRoot,
			SourceConfigSHA256: handoffSHA(source), TargetConfigSHA256: handoffSHA(candidate), EnvironmentAliases: map[string]string{"linux-dev": "linux-host-qas"}},
	}
	handoffTestMkdir(t, op.Directory)
	for path, raw := range map[string][]byte{op.SourceConfig: source, op.TargetConfig: target, filepath.Join(op.Directory, "prd-original.yaml"): source,
		filepath.Join(op.Directory, "qas-original.yaml"): target, filepath.Join(op.Directory, "prd-disabled.yaml"): disabled, filepath.Join(op.Directory, "qas-handoff.yaml"): candidate} {
		if err := handoffWriteNew(path, raw); err != nil {
			t.Fatal(err)
		}
	}
	f := handoffTransactionFixture{op: op, prdContents: map[string][]byte{}, qasContents: map[string][]byte{}, databaseBytes: map[string][]byte{},
		definitions: []mailboxhandoff.Definition{{ID: "default", Root: filepath.Join(qasRoot, "mailbox")}}}
	for _, item := range []struct{ sourceID, targetID, root string }{{"default", "prd-default", filepath.Join(prdRoot, "mailbox")}, {"project", "project", filepath.Join(base, "project", "mailbox-")}} {
		handoffTestMkdir(t, item.root)
		dev, ino, err := handoffIdentity(item.root, true)
		if err != nil {
			t.Fatal(err)
		}
		entry := mailboxhandoff.Mailbox{SourceID: item.sourceID, TargetID: item.targetID, Root: item.root, Device: dev, Inode: ino, ArchiveRelative: filepath.Join(".runner-mailbox-handoffs", id, "prd")}
		f.op.Receipt.Mailboxes = append(f.op.Receipt.Mailboxes, entry)
		f.definitions = append(f.definitions, mailboxhandoff.Definition{ID: item.targetID, Root: item.root})
		for _, name := range handoffDirectories {
			path := filepath.Join(item.root, name)
			handoffTestMkdir(t, path)
			dev, ino, err := handoffIdentity(path, true)
			if err != nil {
				t.Fatal(err)
			}
			f.op.Directories = append(f.op.Directories, handoffDirectory{Root: item.root, Name: name, Device: dev, Inode: ino})
			filename := filepath.Join(path, "req-old-"+item.sourceID+".json")
			raw := []byte("original/" + item.sourceID + "/" + name + "\n\x00preserved\n")
			if name == "inbox" {
				raw = []byte(`{"request_id":"req-old-` + item.sourceID + `","idempotency_key":"old-key-` + item.sourceID + `"}`)
			}
			if err := os.WriteFile(filename, raw, 0o600); err != nil {
				t.Fatal(err)
			}
			f.prdContents[filename] = raw
		}
	}
	if err := mailboxhandoff.ValidateReceipt(f.op.Receipt); err != nil {
		t.Fatal(err)
	}
	f.deps = mailboxHandoffTransactionDeps{
		sourceDatabase: filepath.Join(prdRoot, "state", "local.db"), targetDatabase: filepath.Join(qasRoot, "state", "local.db"),
		stopped: func(context.Context) error { return nil }, idle: handoffIdle, history: handoffCollectHistory, reload: readMailboxHandoffJournal,
		checkpoint: func(string) error { return nil },
	}
	// Real schema-current SQLite authorities exercise the production idle,
	// history and return-ACK queries. Only service observation is synthetic.
	for _, path := range []string{f.deps.sourceDatabase, f.deps.targetDatabase} {
		db, err := store.Open(context.Background(), path)
		if err != nil {
			t.Fatal(err)
		}
		if err := db.Close(); err != nil {
			t.Fatal(err)
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		f.databaseBytes[path] = raw
	}
	if err := handoffSave(f.op); err != nil {
		t.Fatal(err)
	}
	return f
}

func (f handoffTransactionFixture) interrupted(point string) mailboxHandoffTransactionDeps {
	deps := f.deps
	deps.checkpoint = func(at string) error {
		if at == point {
			return errHandoffFixtureInterrupted
		}
		return nil
	}
	return deps
}

func (f handoffTransactionFixture) assertDatabasesPreserved(t *testing.T) {
	t.Helper()
	for path, raw := range f.databaseBytes {
		handoffTestContents(t, path, raw)
	}
}

func (f handoffTransactionFixture) assertPRDArchives(t *testing.T, restored bool) {
	t.Helper()
	for _, d := range f.op.Directories {
		entryRoot := filepath.Join(d.Root, ".runner-mailbox-handoffs", f.op.OperationID, "prd", d.Name)
		if restored {
			entryRoot = filepath.Join(d.Root, d.Name)
		}
		handoffTestIdentity(t, entryRoot, d)
		originalPath := filepath.Join(d.Root, d.Name)
		for path, raw := range f.prdContents {
			if filepath.Dir(path) == originalPath {
				handoffTestContents(t, filepath.Join(entryRoot, filepath.Base(path)), raw)
			}
		}
	}
}

func (f *handoffTransactionFixture) addQASEpoch(t *testing.T) {
	t.Helper()
	for _, entry := range f.op.Receipt.Mailboxes {
		for _, name := range handoffDirectories {
			path := filepath.Join(entry.Root, name, "req-qas-"+entry.TargetID+".json")
			raw := []byte("QAS/" + entry.TargetID + "/" + name + "\n\x00preserved\n")
			if err := os.WriteFile(path, raw, 0o600); err != nil {
				t.Fatal(err)
			}
			f.qasContents[path] = raw
		}
	}
}

func (f handoffTransactionFixture) assertSingleQASOwner(t *testing.T, op MailboxHandoffOperation) {
	t.Helper()
	lease, err := mailboxhandoff.Acquire(op.ReceiptPath, "qas", op.Receipt.TargetServiceRoot, op.Receipt.TargetConfigSHA256, f.definitions)
	if err != nil {
		t.Fatalf("qualified QAS owner could not acquire: %v", err)
	}
	defer lease.Close()
	if another, err := mailboxhandoff.Acquire(op.ReceiptPath, "qas", op.Receipt.TargetServiceRoot, op.Receipt.TargetConfigSHA256, f.definitions); !errors.Is(err, mailboxhandoff.ErrBusy) {
		if another != nil {
			_ = another.Close()
		}
		t.Fatalf("second QAS runtime ownership error=%v", err)
	}
	for _, entry := range op.Receipt.Mailboxes {
		if err := lease.CheckRequest(entry.TargetID, "req-fresh", "key-fresh"); err != nil {
			t.Fatal(err)
		}
		if !errors.Is(lease.CheckRequest(entry.TargetID, "req-old-"+entry.SourceID, "key-fresh"), mailboxhandoff.ErrReplay) {
			t.Fatal("historical request ID was lost across transaction resume")
		}
		if !errors.Is(lease.CheckRequest(entry.TargetID, "req-fresh", "old-key-"+entry.SourceID), mailboxhandoff.ErrReplay) {
			t.Fatal("historical key was lost across transaction resume")
		}
	}
}

func TestMailboxHandoffTransactionResumesEveryActivationBoundary(t *testing.T) {
	for _, point := range []string{"activate-source-config", "activate-target-config", "activate-receipt", "activate-first-owner-marker", "activate-active-journal"} {
		t.Run(point, func(t *testing.T) {
			f := newHandoffTransactionFixture(t)
			_, err := activateMailboxHandoffLocked(context.Background(), f.op, f.interrupted(point))
			if !errors.Is(err, errHandoffFixtureInterrupted) {
				t.Fatalf("expected interrupted %s: %v", point, err)
			}
			journal, err := readMailboxHandoffJournal(filepath.Join(f.op.Directory, "operation.json"))
			if err != nil {
				t.Fatal(err)
			}
			wantPhase := "activating"
			if point == "activate-active-journal" {
				wantPhase = "active"
			}
			if journal.Phase != wantPhase {
				t.Fatalf("durable phase=%s want=%s", journal.Phase, wantPhase)
			}
			f.assertPRDArchives(t, false)
			if wantPhase != "active" {
				lease, err := mailboxhandoff.Acquire(journal.ReceiptPath, "qas", journal.Receipt.TargetServiceRoot, journal.Receipt.TargetConfigSHA256, f.definitions)
				if err == nil {
					_ = lease.Close()
					t.Fatal("partial transaction admitted a QAS runtime")
				}
			}
			active, err := activateMailboxHandoffLocked(context.Background(), journal, f.deps)
			if err != nil || active.Phase != "active" {
				t.Fatalf("resume activation phase=%s err=%v", active.Phase, err)
			}
			if err := handoffExpectedConfigs(active, true); err != nil {
				t.Fatal(err)
			}
			f.assertPRDArchives(t, false)
			f.assertSingleQASOwner(t, active)
			f.assertDatabasesPreserved(t)
		})
	}
}

func TestMailboxHandoffTransactionResumesMixedReturnStates(t *testing.T) {
	for _, point := range []string{"restore-returning-journal", "restore-directory-inbox", "restore-source-config", "restore-target-config", "restore-first-marker-removed", "restore-returned-journal"} {
		t.Run(point, func(t *testing.T) {
			f := newHandoffTransactionFixture(t)
			active, err := activateMailboxHandoffLocked(context.Background(), f.op, f.deps)
			if err != nil {
				t.Fatal(err)
			}
			f.op = active
			f.addQASEpoch(t)
			_, err = restoreMailboxHandoffLocked(context.Background(), active, f.interrupted(point))
			if !errors.Is(err, errHandoffFixtureInterrupted) {
				t.Fatalf("expected interrupted %s: %v", point, err)
			}
			journal, err := readMailboxHandoffJournal(filepath.Join(f.op.Directory, "operation.json"))
			if err != nil {
				t.Fatal(err)
			}
			wantPhase := "returning"
			if point == "restore-returned-journal" {
				wantPhase = "returned"
			}
			if journal.Phase != wantPhase {
				t.Fatalf("durable phase=%s want=%s", journal.Phase, wantPhase)
			}
			returned, err := restoreMailboxHandoffLocked(context.Background(), journal, f.deps)
			if err != nil || returned.Phase != "returned" {
				t.Fatalf("resume return phase=%s err=%v", returned.Phase, err)
			}
			if err := handoffExpectedConfigs(returned, false); err != nil {
				t.Fatal(err)
			}
			f.assertPRDArchives(t, true)
			for path, raw := range f.qasContents {
				for _, entry := range f.op.Receipt.Mailboxes {
					if strings.HasPrefix(path, entry.Root+string(filepath.Separator)) {
						rel, err := filepath.Rel(entry.Root, path)
						if err != nil {
							t.Fatal(err)
						}
						handoffTestContents(t, filepath.Join(entry.Root, ".runner-mailbox-handoffs", returned.OperationID, "qas", rel), raw)
					}
				}
			}
			for _, entry := range returned.Receipt.Mailboxes {
				if exists, err := mailboxhandoff.MarkerExists(entry.Root); err != nil || exists {
					t.Fatalf("returned root marker exists=%v error=%v", exists, err)
				}
			}
			f.assertDatabasesPreserved(t)
		})
	}
}

func TestMailboxHandoffTransactionRefusesChangedConfigAndUnprovenStop(t *testing.T) {
	for _, point := range []string{"changed-source", "changed-target", "service-not-stopped"} {
		t.Run(point, func(t *testing.T) {
			f := newHandoffTransactionFixture(t)
			deps := f.deps
			if point == "service-not-stopped" {
				deps.stopped = func(context.Context) error { return ErrMailboxHandoff }
			} else {
				path := f.op.SourceConfig
				if point == "changed-target" {
					path = f.op.TargetConfig
				}
				if err := os.WriteFile(path, []byte("unrelated operator edit\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := activateMailboxHandoffLocked(context.Background(), f.op, deps); err == nil {
				t.Fatal("unsafe transaction started")
			}
			for path, raw := range f.prdContents {
				handoffTestContents(t, path, raw)
			}
			f.assertDatabasesPreserved(t)
		})
	}
}

func TestMailboxHandoffFilesystemHelpersRejectWritableParent(t *testing.T) {
	base := handoffTestTempDir(t)
	parent := filepath.Join(base, "writable")
	handoffTestMkdir(t, parent)
	path := filepath.Join(parent, "private.json")
	if err := os.WriteFile(path, []byte("preserved"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(parent, 0o775); err != nil {
		t.Fatal(err)
	}
	if _, err := handoffRead(path); !errors.Is(err, ErrMailboxHandoff) {
		t.Fatalf("writable parent was not rejected: %v", err)
	}
	if err := handoffAtomic(path, []byte("replace")); !errors.Is(err, ErrMailboxHandoff) {
		t.Fatalf("writer accepted writable parent: %v", err)
	}
	handoffTestContents(t, path, []byte("preserved"))
}

func TestMailboxHandoffFilesystemHelpersRejectParentSymlinks(t *testing.T) {
	base := handoffTestTempDir(t)
	actual, alias := filepath.Join(base, "actual"), filepath.Join(base, "alias")
	handoffTestMkdir(t, actual)
	private := filepath.Join(actual, "private.json")
	artifact := filepath.Join(actual, "artifact.json")
	if err := os.WriteFile(private, []byte("private"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(artifact, []byte("artifact"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(actual, alias); err != nil {
		t.Fatal(err)
	}
	if _, _, err := handoffIdentity(filepath.Join(alias, "private.json"), false); err == nil {
		t.Fatal("identity followed parent symlink")
	}
	if _, err := handoffRead(filepath.Join(alias, "private.json")); err == nil {
		t.Fatal("private reader followed parent symlink")
	}
	if _, err := handoffReadArtifact(filepath.Join(alias, "artifact.json"), 100); err == nil {
		t.Fatal("artifact reader followed parent symlink")
	}
	if err := handoffEnsurePrivate(filepath.Join(alias, "created")); err == nil {
		t.Fatal("directory creation followed parent symlink")
	}
	if err := handoffWriteNew(filepath.Join(alias, "new.json"), []byte("new")); err == nil {
		t.Fatal("writer followed parent symlink")
	}
	if err := handoffAtomic(filepath.Join(alias, "private.json"), []byte("replace")); err == nil {
		t.Fatal("atomic writer followed parent symlink")
	}
	got, err := os.ReadFile(private)
	if err != nil || !bytes.Equal(got, []byte("private")) {
		t.Fatal("symlink refusal changed original private bytes")
	}
	if _, err := os.Stat(filepath.Join(actual, "created")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("symlink refusal created an outside directory")
	}
}

func TestMailboxHandoffTransactionPreservesForeignRootOwnership(t *testing.T) {
	f := newHandoffTransactionFixture(t)
	entry := f.op.Receipt.Mailboxes[len(f.op.Receipt.Mailboxes)-1]
	foreign := mailboxhandoff.Marker{OperationID: strings.Repeat("b", 32), OwnerLane: "none", ReceiptSHA256: strings.Repeat("c", 64), Device: entry.Device, Inode: entry.Inode}
	if err := mailboxhandoff.WriteMarker(entry.Root, foreign); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(filepath.Join(entry.Root, mailboxhandoff.MarkerFileName))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := activateMailboxHandoffLocked(context.Background(), f.op, f.deps); !errors.Is(err, ErrMailboxHandoff) {
		t.Fatalf("foreign operation ownership error=%v", err)
	}
	handoffTestContents(t, filepath.Join(entry.Root, mailboxhandoff.MarkerFileName), before)
	for path, raw := range f.prdContents {
		handoffTestContents(t, path, raw)
	}
	if err := handoffExpectedConfigs(f.op, false); err != nil {
		t.Fatal(err)
	}
}
