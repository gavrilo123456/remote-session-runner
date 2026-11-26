package mailboxretire

import (
	"crypto/sha256"
	"encoding/hex"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"testing"
)

type retireFixture struct {
	root    string
	archive string
	spec    incident
}

func newRetireFixture(t *testing.T) retireFixture {
	t.Helper()
	parent, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(parent, 0700); err != nil {
		t.Fatal(err)
	}
	f := retireFixture{
		root:    filepath.Join(parent, "mailbox"),
		archive: filepath.Join(parent, "archive"),
		spec:    incident{requestID: "req-test-merge", commandID: "cmd-test-merge"},
	}
	for _, dir := range []string{"inbox", "outbox", "events", "acks", "diagnostics"} {
		if err := os.MkdirAll(filepath.Join(f.root, dir), 0700); err != nil {
			t.Fatal(err)
		}
	}
	write := func(path, content string, mode fs.FileMode) string {
		t.Helper()
		if err := os.WriteFile(path, []byte(content), mode); err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256([]byte(content))
		return hex.EncodeToString(sum[:])
	}
	f.spec.inputHash = write(f.input(), "{\"request_id\":\"req-test-merge\",\"operation\":\"run\",\"script\":\"new\"}\n", 0644)
	write(f.marker(), "", 0644)
	f.spec.outboxHash = write(f.outbox(), "{\"request_id\":\"req-test-merge\",\"request_state\":\"complete\",\"response_revision\":2}\n", 0600)
	f.spec.eventsHash = write(f.events(), "{\"command_id\":\"cmd-test-merge\",\"event_type\":\"command_succeeded\"}\n", 0600)
	write(filepath.Join(f.root, "inbox", "req-unrelated.json"), "{\"request_id\":\"req-unrelated\"}", 0600)
	write(filepath.Join(f.root, "inbox", "req-unrelated.ready"), "", 0600)
	write(filepath.Join(f.root, "acks", "req-unrelated.ready"), "", 0600)
	return f
}

func (f retireFixture) input() string {
	return filepath.Join(f.root, "inbox", f.spec.requestID+".json")
}
func (f retireFixture) marker() string {
	return filepath.Join(f.root, "inbox", f.spec.requestID+".ready")
}
func (f retireFixture) outbox() string {
	return filepath.Join(f.root, "outbox", f.spec.requestID+".json")
}
func (f retireFixture) events() string {
	return filepath.Join(f.root, "events", f.spec.commandID+".ndjson")
}

type fixtureFile struct {
	mode    fs.FileMode
	content string
}

// snapshot includes links as links, so a rejected invocation cannot replace a
// sentinel or alter a symlink target without the comparison detecting it.
func snapshot(t *testing.T, root string) map[string]fixtureFile {
	t.Helper()
	files := make(map[string]fixtureFile)
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		file := fixtureFile{mode: info.Mode()}
		switch {
		case info.Mode().IsRegular():
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			file.content = string(data)
		case info.Mode()&os.ModeSymlink != 0:
			file.content, err = os.Readlink(path)
			if err != nil {
				return err
			}
		}
		files[path] = file
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return files
}

func TestRetirePairDryRun(t *testing.T) {
	f := newRetireFixture(t)
	before := snapshot(t, filepath.Dir(f.root))
	if status, err := retire(f.root, f.archive, false, f.spec); err != nil || status != "ready" {
		t.Fatalf("preview = %q, %v; want ready", status, err)
	}
	if !maps.Equal(snapshot(t, filepath.Dir(f.root)), before) {
		t.Fatal("dry run modified the mailbox or archive")
	}
}

func TestRetirePairRejectsUnsafeOrDifferentInputs(t *testing.T) {
	for _, tc := range []string{"changed request", "nonempty marker", "changed result", "changed events", "unsafe marker mode", "request symlink", "marker hardlink", "result symlink", "missing result", "archive symlink", "archive contains unrelated evidence"} {
		t.Run(tc, func(t *testing.T) {
			f := newRetireFixture(t)
			var err error
			switch tc {
			case "changed request":
				err = os.WriteFile(f.input(), []byte("another request"), 0644)
			case "nonempty marker":
				err = os.WriteFile(f.marker(), []byte("not empty"), 0644)
			case "changed result":
				err = os.WriteFile(f.outbox(), []byte("different historical result"), 0600)
			case "changed events":
				err = os.WriteFile(f.events(), []byte("different events"), 0600)
			case "unsafe marker mode":
				err = os.Chmod(f.marker(), 0666)
			case "request symlink":
				target := filepath.Join(f.root, "sentinel")
				if err = os.Rename(f.input(), target); err == nil {
					err = os.Symlink(target, f.input())
				}
			case "marker hardlink":
				err = os.Link(f.marker(), filepath.Join(f.root, "sentinel"))
			case "result symlink":
				target := filepath.Join(f.root, "sentinel")
				if err = os.Rename(f.outbox(), target); err == nil {
					err = os.Symlink(target, f.outbox())
				}
			case "missing result":
				err = os.Remove(f.outbox())
			case "archive symlink":
				err = os.Symlink(filepath.Join(f.root, "outbox"), f.archive)
			case "archive contains unrelated evidence":
				if err = os.Mkdir(f.archive, 0700); err == nil {
					err = os.WriteFile(filepath.Join(f.archive, "other-run"), []byte("keep"), 0600)
				}
			}
			if err != nil {
				t.Fatal(err)
			}
			before := snapshot(t, filepath.Dir(f.root))
			if _, err := retire(f.root, f.archive, true, f.spec); err == nil {
				t.Fatal("retirement accepted invalid evidence")
			}
			if !maps.Equal(snapshot(t, filepath.Dir(f.root)), before) {
				t.Fatal("refusal changed the mailbox or archive")
			}
		})
	}
}

func TestRetirePairArchivesOnlyDuplicateAndRepeats(t *testing.T) {
	f := newRetireFixture(t)
	before := snapshot(t, f.root)
	request := before[f.input()].content
	delete(before, f.input())
	delete(before, f.marker())
	if status, err := retire(f.root, f.archive, true, f.spec); err != nil || status != "retired" {
		t.Fatalf("apply = %q, %v; want retired", status, err)
	}
	if !maps.Equal(snapshot(t, f.root), before) {
		t.Fatal("retirement changed more than the duplicate pair")
	}
	after := snapshot(t, f.archive)
	wantArchive := map[string]fixtureFile{
		f.archive: {mode: os.ModeDir | 0700},
		filepath.Join(f.archive, filepath.Base(f.input())):  {mode: 0644, content: request},
		filepath.Join(f.archive, filepath.Base(f.marker())): {mode: 0644},
	}
	if !maps.Equal(after, wantArchive) {
		t.Fatalf("unexpected archive contents or permissions: %#v", after)
	}
	if _, err := retire(f.root, f.archive, true, f.spec); err != nil {
		t.Fatal(err)
	}
	if !maps.Equal(snapshot(t, f.root), before) || !maps.Equal(snapshot(t, f.archive), after) {
		t.Fatal("repeated retirement changed the mailbox or evidence")
	}
}

func TestRetirePairRefusesUnexplainedMissingInput(t *testing.T) {
	for _, missing := range []string{"marker", "request", "both"} {
		t.Run(missing, func(t *testing.T) {
			f := newRetireFixture(t)
			if missing != "request" {
				if err := os.Remove(f.marker()); err != nil {
					t.Fatal(err)
				}
			}
			if missing != "marker" {
				if err := os.Remove(f.input()); err != nil {
					t.Fatal(err)
				}
			}
			before := snapshot(t, f.root)
			if _, err := retire(f.root, f.archive, true, f.spec); err == nil {
				t.Fatal("missing input was interpreted as a successful retirement")
			}
			if !maps.Equal(snapshot(t, f.root), before) {
				t.Fatal("refusal modified the mailbox")
			}
		})
	}
}

func TestRetirePairResumesFromEachDurableStep(t *testing.T) {
	for _, step := range []string{"archive created", "request linked", "marker linked", "marker withdrawn"} {
		t.Run(step, func(t *testing.T) {
			f := newRetireFixture(t)
			want := snapshot(t, f.root)
			delete(want, f.input())
			delete(want, f.marker())
			if err := os.Mkdir(f.archive, 0700); err != nil {
				t.Fatal(err)
			}
			if step != "archive created" {
				if err := os.Link(f.input(), filepath.Join(f.archive, filepath.Base(f.input()))); err != nil {
					t.Fatal(err)
				}
			}
			if step == "marker linked" || step == "marker withdrawn" {
				if err := os.Link(f.marker(), filepath.Join(f.archive, filepath.Base(f.marker()))); err != nil {
					t.Fatal(err)
				}
			}
			if step == "marker withdrawn" {
				if err := os.Remove(f.marker()); err != nil {
					t.Fatal(err)
				}
			}
			before := snapshot(t, filepath.Dir(f.root))
			if _, err := retire(f.root, f.archive, false, f.spec); err != nil {
				t.Fatalf("preview: %v", err)
			}
			if !maps.Equal(snapshot(t, filepath.Dir(f.root)), before) {
				t.Fatal("preview changed a partial retirement")
			}
			if status, err := retire(f.root, f.archive, true, f.spec); err != nil || status != "retired" {
				t.Fatalf("resume = %q, %v; want retired", status, err)
			}
			if !maps.Equal(snapshot(t, f.root), want) {
				t.Fatal("resume changed history or an unrelated input")
			}
		})
	}
}

func TestRetirePairRefusesIdenticalBytesFromAnotherPublication(t *testing.T) {
	f := newRetireFixture(t)
	if _, err := retire(f.root, f.archive, true, f.spec); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{f.input(), f.marker()} {
		b, err := os.ReadFile(filepath.Join(f.archive, filepath.Base(path)))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, b, 0644); err != nil {
			t.Fatal(err)
		}
	}
	before := snapshot(t, filepath.Dir(f.root))
	if _, err := retire(f.root, f.archive, true, f.spec); err == nil {
		t.Fatal("a separate publication was treated as a resumable retirement")
	}
	if !maps.Equal(snapshot(t, filepath.Dir(f.root)), before) {
		t.Fatal("retirement modified a separate publication or existing archive")
	}
}
