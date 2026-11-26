package mailboxclient

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBUG039ClientRefusesRetainedRequestIDWithoutChangingOriginalResponse(t *testing.T) {
	root, client := newBUG039Client(t)
	const requestID = "req-bug039-retained"
	response := []byte(`{"request_id":"req-bug039-retained","request_state":"complete","response_revision":2,"command_state":"succeeded","exit_code":0,"final_event_sequence":7}`)
	path := filepath.Join(root, "outbox", requestID+".json")
	if err := os.WriteFile(path, response, fileMode); err != nil {
		t.Fatal(err)
	}
	err := client.WriteRequest(requestID, []byte(`{"request_id":"req-bug039-retained","operation":"run","script":"different operation"}`))
	if !errors.Is(err, ErrRequestIDRetained) || !errors.Is(err, ErrRequest) {
		t.Fatalf("WriteRequest error=%v, want retained-ID and request errors", err)
	}
	if !strings.Contains(err.Error(), "fresh request ID") {
		t.Fatalf("missing correction guidance: %v", err)
	}
	assertBUG039InboxEmpty(t, root)
	got, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(got, response) {
		t.Fatalf("retained response changed: %q, %v", got, err)
	}
	read, err := client.WaitResponse(context.Background(), requestID)
	if err != nil || read.ResponseRevision != 2 || read.ExitCode == nil || *read.ExitCode != 0 {
		t.Fatalf("original response no longer readable: %+v, %v", read, err)
	}
	// Reading and acknowledging an existing result remains supported; the new
	// guard applies only to publishing another request with that identity.
	if err := client.WriteAcknowledgment(requestID, read); err != nil {
		t.Fatalf("ACK publication was incorrectly blocked: %v", err)
	}
}

func TestBUG039ClientRefusesAnyRetainedArtifactWithoutFollowingIt(t *testing.T) {
	for _, directory := range []string{"outbox", "diagnostics"} {
		for _, kind := range []string{"regular", "unsafe-mode", "directory", "symlink", "dangling-symlink"} {
			t.Run(directory+"/"+kind, func(t *testing.T) {
				root, client := newBUG039Client(t)
				const requestID = "req-bug039-occupied"
				path := filepath.Join(root, directory, requestID+".json")
				target := filepath.Join(root, "unrelated-target")
				const sentinel = "UNRELATED_OR_RETAINED_BYTES"
				if err := os.WriteFile(target, []byte(sentinel), fileMode); err != nil {
					t.Fatal(err)
				}
				switch kind {
				case "regular", "unsafe-mode":
					if err := os.WriteFile(path, []byte(sentinel), fileMode); err != nil {
						t.Fatal(err)
					}
					if kind == "unsafe-mode" {
						if err := os.Chmod(path, 0o666); err != nil {
							t.Fatal(err)
						}
					}
				case "directory":
					if err := os.Mkdir(path, directoryMode); err != nil {
						t.Fatal(err)
					}
				case "symlink", "dangling-symlink":
					link := target
					if kind == "dangling-symlink" {
						link = filepath.Join(root, "absent-target")
					}
					if err := os.Symlink(link, path); err != nil {
						t.Fatal(err)
					}
				}
				before, err := os.Lstat(path)
				if err != nil {
					t.Fatal(err)
				}
				err = client.WriteRequest(requestID, []byte(`{"request_id":"req-bug039-occupied","operation":"run","script":"must not be published"}`))
				if !errors.Is(err, ErrRequestIDRetained) {
					t.Fatalf("WriteRequest error=%v, want retained-ID refusal", err)
				}
				assertBUG039InboxEmpty(t, root)
				after, err := os.Lstat(path)
				if err != nil || !os.SameFile(before, after) || before.Mode() != after.Mode() {
					t.Fatalf("retained entry changed: %v", err)
				}
				if got, err := os.ReadFile(target); err != nil || string(got) != sentinel {
					t.Fatalf("symlink target changed: %q, %v", got, err)
				}
			})
		}
	}
}

func TestBUG039ClientPublishesFreshIDWithoutRequiringResultDirectories(t *testing.T) {
	root, client := newBUG039Client(t)
	for _, directory := range []string{"outbox", "diagnostics"} {
		if err := os.Remove(filepath.Join(root, directory)); err != nil {
			t.Fatal(err)
		}
	}
	const requestID = "req-bug039-fresh"
	raw := []byte(`{"request_id":"req-bug039-fresh","operation":"get_command","command_id":"cmd-existing"}`)
	if err := client.WriteRequest(requestID, raw); err != nil {
		t.Fatalf("fresh request refused: %v", err)
	}
	if got, err := os.ReadFile(filepath.Join(root, "inbox", requestID+".json")); err != nil || !bytes.Equal(got, raw) {
		t.Fatalf("fresh JSON=%q, %v", got, err)
	}
	if got, err := os.ReadFile(filepath.Join(root, "inbox", requestID+".ready")); err != nil || len(got) != 0 {
		t.Fatalf("fresh marker=%q, %v", got, err)
	}
}

func TestBUG039ClientRefusesUnsafeResultDirectoryWithoutFollowingIt(t *testing.T) {
	for _, directory := range []string{"outbox", "diagnostics"} {
		t.Run(directory, func(t *testing.T) {
			root, client := newBUG039Client(t)
			if err := os.Remove(filepath.Join(root, directory)); err != nil {
				t.Fatal(err)
			}
			target := t.TempDir()
			const requestID = "req-bug039-parent"
			const sentinel = "UNRELATED_RESPONSE"
			path := filepath.Join(target, requestID+".json")
			if err := os.WriteFile(path, []byte(sentinel), fileMode); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(target, filepath.Join(root, directory)); err != nil {
				t.Fatal(err)
			}
			err := client.WriteRequest(requestID, []byte(`{"request_id":"req-bug039-parent","operation":"run","script":"must not be published"}`))
			if !errors.Is(err, ErrRequest) {
				t.Fatalf("unsafe result directory publication error=%v, want request refusal", err)
			}
			assertBUG039InboxEmpty(t, root)
			if got, err := os.ReadFile(path); err != nil || string(got) != sentinel {
				t.Fatalf("unsafe directory target changed: %q, %v", got, err)
			}
		})
	}
}

func newBUG039Client(t *testing.T) (string, *Client) {
	t.Helper()
	root := filepath.Join(t.TempDir(), "mailbox")
	for _, directory := range []string{"", "inbox", "outbox", "diagnostics", "acks"} {
		if err := os.Mkdir(filepath.Join(root, directory), directoryMode); err != nil {
			t.Fatal(err)
		}
	}
	client, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	return root, client
}

func assertBUG039InboxEmpty(t *testing.T, root string) {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(root, "inbox"))
	if err != nil || len(entries) != 0 {
		t.Fatalf("retained-ID refusal left inbox entries=%v, %v", entries, err)
	}
}
