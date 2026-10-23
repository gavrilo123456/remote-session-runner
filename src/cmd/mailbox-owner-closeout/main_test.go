package main

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
)

func TestApprovedRequestScopeIsExactlyTheOwnerAuthorizedSet(t *testing.T) {
	want := []string{
		"req-codex-logger-redis-build-4972-progress-20261023-11",
		"req-codex-logger-ee75-update-rules-20261023-02",
		"req-codex-logger-ee75-current-control-read-20261023-01",
	}
	if len(approvedRequests) != len(want) {
		t.Fatalf("approved request count=%d, want exactly %d", len(approvedRequests), len(want))
	}
	for i := range want {
		if approvedRequests[i].RequestID != want[i] {
			t.Fatalf("approved request[%d]=%q, want %q", i, approvedRequests[i].RequestID, want[i])
		}
		if approvedRequests[i].IntentID == "" || approvedRequests[i].JobID == "" || approvedRequests[i].SessionID == "" || approvedRequests[i].CommandID == "" {
			t.Fatalf("request %q lacks its fixed intent/job/session/command identity", want[i])
		}
	}
}

func TestValidateArchivedArtifactsRequiresExactPathsAndVerifiedBytes(t *testing.T) {
	root := t.TempDir()
	sourceRoot := filepath.Join(root, "source-mailbox")
	var snapshots []fileSnapshot
	for _, request := range approvedRequests {
		for _, relative := range []string{
			filepath.Join("inbox", request.RequestID+".json"),
			filepath.Join("inbox", request.RequestID+".ready"),
			filepath.Join("outbox", request.RequestID+".json"),
			filepath.Join("acks", request.RequestID+".json"),
			filepath.Join("acks", request.RequestID+".ready"),
			filepath.Join("diagnostics", request.RequestID+".json"),
			filepath.Join("events", string(request.CommandID)+".ndjson"),
		} {
			snapshot := fileSnapshot{Path: relative}
			if relative == filepath.Join("outbox", request.RequestID+".json") {
				payload := []byte("preserved terminal response")
				path := filepath.Join(sourceRoot, relative)
				if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, payload, 0o600); err != nil {
					t.Fatal(err)
				}
				digest := sha256.Sum256(payload)
				snapshot.Present = true
				snapshot.Size = int64(len(payload))
				snapshot.SHA256 = hex.EncodeToString(digest[:])
			}
			snapshots = append(snapshots, snapshot)
		}
	}
	if err := validateArchivedArtifacts(root, snapshots); err == nil {
		t.Fatal("validation accepted artifacts before they were copied into the private backup")
	}
	for _, snapshot := range snapshots {
		if !snapshot.Present {
			continue
		}
		source := filepath.Join(sourceRoot, snapshot.Path)
		destination := filepath.Join(root, "mailbox", snapshot.Path)
		if err := os.MkdirAll(filepath.Dir(destination), 0o700); err != nil {
			t.Fatal(err)
		}
		data, err := os.ReadFile(source)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(destination, data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := validateArchivedArtifacts(root, snapshots); err != nil {
		t.Fatalf("valid exact backup rejected: %v", err)
	}

	responsePath := filepath.Join(root, "mailbox", "outbox", approvedRequests[0].RequestID+".json")
	if err := os.WriteFile(responsePath, []byte("changed response"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := validateArchivedArtifacts(root, snapshots); err == nil {
		t.Fatal("validation accepted a modified terminal response")
	}

	snapshots = snapshots[1:]
	if err := validateArchivedArtifacts(root, snapshots); err == nil {
		t.Fatal("validation accepted a manifest that omitted an exact artifact path")
	}
}
