package laneinstall

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"remote-session-runner/src/internal/config"
)

func TestSimpleArtifactWriterIsCreateOnlyIdempotentAndRejectsSymlinks(t *testing.T) {
	root := simpleTestTempDir(t)
	parent := filepath.Join(root, "lane", "config")
	if err := os.MkdirAll(parent, 0o700); err != nil {
		t.Fatal(err)
	}
	artifact := Artifact{Kind: ArtifactEntrypoint, InstallPath: filepath.Join(parent, "entrypoint.sh"), Mode: 0o700, Contents: "#!/bin/sh\nexit 0\n"}
	if err := writeSimpleUserArtifact(artifact); err != nil {
		t.Fatal(err)
	}
	if err := writeSimpleUserArtifact(artifact); err != nil {
		t.Fatalf("identical retry failed: %v", err)
	}
	conflict := artifact
	conflict.Contents += "# changed\n"
	if err := writeSimpleUserArtifact(conflict); !errors.Is(err, ErrSimpleArtifactConflict) {
		t.Fatalf("different existing artifact error=%v", err)
	}
	link := filepath.Join(parent, "linked")
	if err := os.Symlink(artifact.InstallPath, link); err != nil {
		t.Fatal(err)
	}
	artifact.InstallPath = link
	if err := writeSimpleUserArtifact(artifact); !errors.Is(err, ErrSimpleArtifactConflict) {
		t.Fatalf("symlink artifact error=%v", err)
	}
}

func TestSimpleLaneOwnedDirectoryRequiresOwnerOnlyRealDirectory(t *testing.T) {
	root := simpleTestTempDir(t)
	path := filepath.Join(root, "service")
	if err := ensureSimpleOwnedDirectory(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := ensureSimpleOwnedDirectory(path); !errors.Is(err, ErrSimpleLanePreflight) {
		t.Fatalf("world-readable lane root error=%v", err)
	}
	if err := os.Chmod(path, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "link")
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	if err := ensureSimpleOwnedDirectory(link); !errors.Is(err, ErrInvalidSimpleBundle) && !errors.Is(err, ErrSimpleLanePreflight) {
		t.Fatalf("symlink lane directory error=%v", err)
	}
}

func TestSimpleInstallMarkerIsLaneAndHostBound(t *testing.T) {
	root := simpleTestTempDir(t)
	namespace, err := config.DeriveSimpleLaneNamespace(config.HostKindLinux, config.ReleaseLaneDev, "linux-host")
	if err != nil {
		t.Fatal(err)
	}
	namespace.Staging = filepath.Join(root, "staging")
	if err := os.MkdirAll(namespace.Staging, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := writeSimpleInstallMarker(namespace, "linux-host", config.ReleaseLaneDev); err != nil {
		t.Fatal(err)
	}
	marker, exists, err := readSimpleInstallMarker(namespace)
	if err != nil || !exists || marker.HostID != "linux-host" || marker.Lane != config.ReleaseLaneDev {
		t.Fatalf("read install marker=%+v exists=%t err=%v", marker, exists, err)
	}
	if err := writeSimpleInstallMarker(namespace, "factory-sandbox", config.ReleaseLaneDev); !errors.Is(err, ErrSimpleLanePreflight) {
		t.Fatalf("foreign host marker rewrite error=%v", err)
	}
	if err := removeSimpleInstallMarker(namespace, "factory-sandbox", config.ReleaseLaneDev); !errors.Is(err, ErrSimpleLanePreflight) {
		t.Fatalf("foreign host marker removal error=%v", err)
	}
	if err := removeSimpleInstallMarker(namespace, "linux-host", config.ReleaseLaneDev); err != nil {
		t.Fatalf("remove completed selected-lane marker: %v", err)
	}
	if _, exists, err := readSimpleInstallMarker(namespace); err != nil || exists {
		t.Fatalf("completed install marker remains: exists=%t err=%v", exists, err)
	}
}
