package mailbox

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func bug039ConflictFilesFixture(t *testing.T, requestID string) (*requestConflictFiles, requestConflictRecord, []byte) {
	t.Helper()
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	f, err := newRequestConflictFiles(root)
	if err != nil {
		t.Fatal(err)
	}
	raw := []byte(fmt.Sprintf(`{"request_id":%q,"operation":"run","script":"never run this conflicting input"}`, requestID))
	input := sha256.Sum256(raw)
	binding := sha256.Sum256([]byte("original immutable binding"))
	return f, requestConflictRecord{
		FormatVersion: 1, MailboxID: "mailbox-bug039", RequestID: requestID,
		InputSHA256: hex.EncodeToString(input[:]), OriginalBindingSHA256: hex.EncodeToString(binding[:]),
		Code: requestConflictCode, Message: requestConflictMessage,
	}, raw
}

func bug039ConflictArchive(f *requestConflictFiles, r requestConflictRecord) string {
	return filepath.Join(f.root, r.RequestID, r.InputSHA256)
}

func TestBUG039ConflictFilesPreserveAndFinishAreIdempotent(t *testing.T) {
	ctx := context.Background()
	f, record, raw := bug039ConflictFilesFixture(t, "req-bug039-files-roundtrip")
	if err := f.Preserve(ctx, record, raw); err != nil {
		t.Fatal(err)
	}
	archive := bug039ConflictArchive(f, record)
	before, err := os.Stat(filepath.Join(archive, "request.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Preserve(ctx, record, raw); err != nil {
		t.Fatalf("repeat preservation: %v", err)
	}
	after, err := os.Stat(filepath.Join(archive, "request.json"))
	if err != nil || !os.SameFile(before, after) {
		t.Fatalf("immutable archive was replaced: %v", err)
	}
	for _, name := range []string{"request.json", "request.ready", "diagnostic.json"} {
		st, err := os.Lstat(filepath.Join(archive, name))
		if err != nil || st.Mode().Perm() != 0o600 || !st.Mode().IsRegular() {
			t.Fatalf("private archive %s: %v %v", name, st, err)
		}
	}
	for _, path := range []string{f.root, filepath.Join(f.root, ".pending"), filepath.Join(f.root, ".staging"), filepath.Dir(archive), archive} {
		st, err := os.Lstat(path)
		if err != nil || !st.IsDir() || st.Mode().Perm() != 0o700 {
			t.Fatalf("private directory %s: %v %v", path, st, err)
		}
	}
	got, err := os.ReadFile(filepath.Join(archive, "request.json"))
	if err != nil || !bytes.Equal(got, raw) {
		t.Fatalf("exact newer bytes not preserved: %q %v", got, err)
	}
	var diagnostic map[string]any
	diagnosticBytes, err := os.ReadFile(filepath.Join(archive, "diagnostic.json"))
	if err != nil || json.Unmarshal(diagnosticBytes, &diagnostic) != nil || diagnostic["accepted"] != false || diagnostic["executed"] != false || diagnostic["code"] != requestConflictCode {
		t.Fatalf("conflict receipt: %s %v", diagnosticBytes, err)
	}
	for _, forbidden := range []string{"script", "idempotency_key", "response_revision", "exit_code", "available_event_sequence"} {
		if _, exists := diagnostic[forbidden]; exists {
			t.Fatalf("conflict diagnostic contains %s", forbidden)
		}
	}
	pending, err := f.Pending(ctx, 64)
	if err != nil || len(pending) != 1 || pending[0] != record {
		t.Fatalf("pending=%+v err=%v", pending, err)
	}
	if err := f.Finish(ctx, record); err != nil {
		t.Fatal(err)
	}
	if err := f.Finish(ctx, record); err != nil {
		t.Fatalf("repeat finish: %v", err)
	}
	pending, err = f.Pending(ctx, 64)
	if err != nil || len(pending) != 0 {
		t.Fatalf("finished record still pending: %+v %v", pending, err)
	}
	if err := f.Preserve(ctx, record, raw); err != nil {
		t.Fatalf("same conflicting publication can be retired again: %v", err)
	}
	pending, err = f.Pending(ctx, 64)
	if err != nil || len(pending) != 1 {
		t.Fatalf("republished conflict missing pending boundary: %+v %v", pending, err)
	}
}

func TestBUG039ConflictFilesRejectChangedEvidenceWithoutOverwrite(t *testing.T) {
	for _, name := range []string{"request.json", "request.ready", "diagnostic.json", "pending"} {
		t.Run(name, func(t *testing.T) {
			f, record, raw := bug039ConflictFilesFixture(t, "req-bug039-tamper")
			if err := f.Preserve(context.Background(), record, raw); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(bug039ConflictArchive(f, record), name)
			if name == "pending" {
				path = filepath.Join(f.root, ".pending", requestConflictIndexName(record))
			}
			changed := []byte("changed evidence")
			if err := os.WriteFile(path, changed, 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := f.Pending(context.Background(), 64); err == nil {
				t.Fatal("tampered pending/archive evidence was accepted")
			}
			if err := f.Finish(context.Background(), record); err == nil {
				t.Fatal("tampered evidence was finalized")
			}
			if err := f.Preserve(context.Background(), record, raw); err == nil {
				t.Fatal("tampered evidence was overwritten")
			}
			got, err := os.ReadFile(path)
			if err != nil || !bytes.Equal(got, changed) {
				t.Fatalf("tampered evidence did not remain untouched: %q %v", got, err)
			}
		})
	}
}

func TestBUG039ConflictFilesRefuseUnsafeModesAndSymlinks(t *testing.T) {
	for _, target := range []string{"conflicts", ".pending", "request-directory", "archive-directory", "request.json", "diagnostic.json"} {
		t.Run(target, func(t *testing.T) {
			f, record, raw := bug039ConflictFilesFixture(t, "req-bug039-symlink")
			if err := f.Preserve(context.Background(), record, raw); err != nil {
				t.Fatal(err)
			}
			archive := bug039ConflictArchive(f, record)
			path := filepath.Join(archive, target)
			switch target {
			case "conflicts":
				path = f.root
			case ".pending":
				path = filepath.Join(f.root, target)
			case "request-directory":
				path = filepath.Dir(archive)
			case "archive-directory":
				path = archive
			}
			saved := path + ".original"
			if err := os.Rename(path, saved); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(saved, path); err != nil {
				t.Fatal(err)
			}
			if err := f.Preserve(context.Background(), record, raw); err == nil {
				t.Fatal("symlink preservation accepted")
			}
			if _, err := f.Pending(context.Background(), 64); err == nil {
				t.Fatal("symlink pending/archive accepted")
			}
		})
	}
	f, record, raw := bug039ConflictFilesFixture(t, "req-bug039-mode")
	if err := f.Preserve(context.Background(), record, raw); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(bug039ConflictArchive(f, record), "request.json")
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Pending(context.Background(), 64); err == nil {
		t.Fatal("workspace-readable archive accepted")
	}
}

func TestBUG039ConflictFilesBoundedPendingDoesNotScanHistory(t *testing.T) {
	f, _, _ := bug039ConflictFilesFixture(t, "unused")
	for _, id := range []string{"pending", "req-bug039-next", "req-bug039-last"} {
		_, record, raw := bug039ConflictFilesFixture(t, id)
		if err := f.Preserve(context.Background(), record, raw); err != nil {
			t.Fatal(err)
		}
	}
	// A non-index archive/history name is outside the recovery enumeration.
	if err := os.Symlink("/unrelated/no-such-directory", filepath.Join(f.root, "unindexed-history")); err != nil {
		t.Fatal(err)
	}
	for left := 3; left > 0; left-- {
		pending, err := f.Pending(context.Background(), 1)
		if err != nil || len(pending) != 1 {
			t.Fatalf("bounded pending at %d=%+v %v", left, pending, err)
		}
		if err := f.Finish(context.Background(), pending[0]); err != nil {
			t.Fatal(err)
		}
	}
	pending, err := f.Pending(context.Background(), 1)
	if err != nil || len(pending) != 0 {
		t.Fatalf("finished pending=%+v err=%v", pending, err)
	}
}

func TestBUG039ConflictFilesInterruptionBoundaries(t *testing.T) {
	for _, boundary := range []string{"before-index", "linked-temporary", "after-retired-marker"} {
		t.Run(boundary, func(t *testing.T) {
			f, record, raw := bug039ConflictFilesFixture(t, "req-bug039-interrupted")
			if err := f.Preserve(context.Background(), record, raw); err != nil {
				t.Fatal(err)
			}
			archive := bug039ConflictArchive(f, record)
			switch boundary {
			case "before-index":
				if err := os.Remove(filepath.Join(f.root, ".pending", requestConflictIndexName(record))); err != nil {
					t.Fatal(err)
				}
				if err := f.Preserve(context.Background(), record, raw); err != nil {
					t.Fatalf("preserved archive before index could not resume: %v", err)
				}
			case "linked-temporary":
				for _, path := range []string{filepath.Join(archive, "request.json"), filepath.Join(f.root, ".pending", requestConflictIndexName(record))} {
					if err := os.Link(path, filepath.Join(f.root, ".staging", filepath.Base(path)+".crashed")); err != nil {
						t.Fatal(err)
					}
				}
			case "after-retired-marker":
				if err := os.WriteFile(filepath.Join(archive, "retired"), nil, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			pending, err := f.Pending(context.Background(), 1)
			if err != nil || len(pending) != 1 {
				t.Fatalf("interrupted evidence could not recover: %+v %v", pending, err)
			}
			if err := f.Finish(context.Background(), record); err != nil {
				t.Fatal(err)
			}
			if err := f.Finish(context.Background(), record); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestBUG039ConflictFilesRejectInvalidIdentityAndCanceledWork(t *testing.T) {
	f, record, raw := bug039ConflictFilesFixture(t, "req-bug039-invalid")
	for _, change := range []func(*requestConflictRecord){
		func(r *requestConflictRecord) { r.FormatVersion = 2 },
		func(r *requestConflictRecord) { r.RequestID = "../other" },
		func(r *requestConflictRecord) { r.MailboxID = "../other" },
		func(r *requestConflictRecord) { r.InputSHA256 = strings.ToUpper(r.InputSHA256) },
		func(r *requestConflictRecord) { r.OriginalBindingSHA256 = "invalid" },
		func(r *requestConflictRecord) { r.Accepted = true },
		func(r *requestConflictRecord) { r.Executed = true },
		func(r *requestConflictRecord) { r.Code = "succeeded" },
		func(r *requestConflictRecord) { r.Message = "untrusted request text" },
	} {
		invalid := record
		change(&invalid)
		if err := f.Preserve(context.Background(), invalid, raw); err == nil {
			t.Fatalf("invalid record accepted: %+v", invalid)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := f.Preserve(ctx, record, raw); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled preservation: %v", err)
	}
	if _, err := f.Pending(ctx, 64); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled enumeration: %v", err)
	}
	if err := f.Finish(ctx, record); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled finish: %v", err)
	}
	if _, err := f.Pending(context.Background(), 257); err == nil {
		t.Fatal("unbounded enumeration accepted")
	}
}
