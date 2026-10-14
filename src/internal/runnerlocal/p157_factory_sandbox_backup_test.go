package runnerlocal

import (
	"archive/tar"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"
)

const p157SelectedSSHItems = "config known_hosts gavrilo123456-github dev.slidestud.io factory-sandbox remote-session-runner"

func TestP157MacContinuityScriptsKeepFactorySandboxInSelectedSSHSet(t *testing.T) {
	repositoryRoot := p157BackupRepositoryRoot(t)
	backup := p157ReadBackupShellScript(t, filepath.Join(repositoryRoot, "deploy", "backup", "backup-macos-runner.sh"))
	restore := p157ReadBackupShellScript(t, filepath.Join(repositoryRoot, "deploy", "backup", "restore-macos-runner.sh"))
	verify := p157ReadBackupShellScript(t, filepath.Join(repositoryRoot, "deploy", "backup", "verify-macos-runner-backup.sh"))

	for name, fragment := range map[string]string{
		"backup copy":      "for name in " + p157SelectedSSHItems + "; do",
		"backup preflight": "for name in known_hosts gavrilo123456-github dev.slidestud.io factory-sandbox; do",
		"restore":          "config|known_hosts|gavrilo123456-github|dev.slidestud.io|factory-sandbox|remote-session-runner",
		"verify":           "config|known_hosts|gavrilo123456-github|dev.slidestud.io|factory-sandbox|remote-session-runner",
	} {
		var script string
		switch name {
		case "backup copy", "backup preflight":
			script = backup
		case "restore":
			script = restore
		case "verify":
			script = verify
		default:
			t.Fatalf("unhandled continuity script %q", name)
		}
		if !strings.Contains(script, fragment) {
			t.Fatalf("%s omits factory-sandbox from the selected SSH contract", name)
		}
	}
}

func TestP157MacBackupVerifierAcceptsFactorySandboxSelectedSSHMaterial(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("the continuity verifier uses macOS system tools")
	}

	repositoryRoot := p157BackupRepositoryRoot(t)
	verifier := filepath.Join(repositoryRoot, "deploy", "backup", "verify-macos-runner-backup.sh")

	t.Run("accepted bootstrap item", func(t *testing.T) {
		archive := p157WriteSelectedSSHBackupArchive(t, nil, true)
		output, err := exec.Command("/bin/sh", verifier, "--archive", archive).CombinedOutput()
		if err != nil {
			t.Fatalf("verify selected SSH archive: %v\n%s", err, output)
		}
		if !strings.Contains(string(output), "Backup verified:") {
			t.Fatalf("verify output=%q, want successful backup verification", output)
		}
	})

	t.Run("rejects an unrecognized selected item", func(t *testing.T) {
		archive := p157WriteSelectedSSHBackupArchive(t, map[string][]byte{
			"payload/ssh/not-allowed": []byte("synthetic SSH fixture; not credential material\n"),
		}, true)
		output, err := exec.Command("/bin/sh", verifier, "--archive", archive).CombinedOutput()
		if err == nil {
			t.Fatalf("verifier accepted an unrecognized selected SSH item: %s", output)
		}
		if !strings.Contains(string(output), "archive has an unexpected selected SSH entry: not-allowed") {
			t.Fatalf("verifier output=%q, want unrecognized selected-item rejection", output)
		}
	})

	t.Run("rejects an empty selected set", func(t *testing.T) {
		archive := p157WriteSelectedSSHBackupArchive(t, nil, false)
		output, err := exec.Command("/bin/sh", verifier, "--archive", archive).CombinedOutput()
		if err == nil {
			t.Fatalf("verifier accepted an empty selected SSH set: %s", output)
		}
		if !strings.Contains(string(output), "archive selected SSH payload is empty") {
			t.Fatalf("verifier output=%q, want empty selected-set rejection", output)
		}
	})
}

func p157BackupRepositoryRoot(t *testing.T) string {
	t.Helper()
	_, source, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate P157 backup test source")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(source), "..", "..", ".."))
}

func p157ReadBackupShellScript(t *testing.T, path string) string {
	t.Helper()
	if err := exec.Command("sh", "-n", path).Run(); err != nil {
		t.Fatalf("shell syntax %s: %v", path, err)
	}
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(contents)
}

func p157WriteSelectedSSHBackupArchive(t *testing.T, additional map[string][]byte, includeSelectedSSH bool) string {
	t.Helper()
	files := map[string][]byte{
		"metadata/manifest.txt": []byte(strings.Join([]string{
			"format=remote-session-runner-macos-continuity-v1",
			"source_revision=aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
			"source_origin=ssh://example.invalid/remote-session-runner.git",
			"",
		}, "\n")),
		"metadata/service-components.tsv":                 []byte("fixture\n"),
		"metadata/mailboxes.tsv":                          []byte("fixture\n"),
		"metadata/external-mailboxes.tsv":                 []byte("fixture\n"),
		"metadata/ssh.txt":                                []byte("mode=selected\n"),
		"metadata/ssh.tsv":                                nil,
		"payload/service-root/config/mac.yaml":            []byte("schema_version: 2\n"),
		"payload/service-root/toolchains/go1.27.1/bin/go": []byte("synthetic go toolchain fixture\n"),
		"source/remote-session-runner.bundle":             []byte("synthetic source bundle fixture\n"),
	}
	if includeSelectedSSH {
		files["metadata/ssh.tsv"] = []byte("/Users/tomasz.walczuk/.ssh/factory-sandbox\tpayload/ssh/factory-sandbox\n")
		files["payload/ssh/config"] = []byte("Host factory-sandbox\n")
		files["payload/ssh/known_hosts"] = []byte("factory-sandbox fixture host key\n")
		files["payload/ssh/gavrilo123456-github"] = []byte("synthetic GitHub identity fixture; not credential material\n")
		files["payload/ssh/dev.slidestud.io"] = []byte("synthetic SlideStudio identity fixture; not credential material\n")
		files["payload/ssh/factory-sandbox"] = []byte("synthetic factory bootstrap fixture; not credential material\n")
		files["payload/ssh/remote-session-runner"] = []byte("synthetic Runner identity fixture; not credential material\n")
	}
	for name, contents := range additional {
		files[name] = contents
	}

	fileNames := make([]string, 0, len(files))
	for name := range files {
		fileNames = append(fileNames, name)
	}
	sort.Strings(fileNames)
	var sums strings.Builder
	for _, name := range fileNames {
		digest := sha256.Sum256(files[name])
		fmt.Fprintf(&sums, "%x  %s\n", digest, name)
	}
	files["metadata/SHA256SUMS"] = []byte(sums.String())

	archive := filepath.Join(t.TempDir(), "factory-sandbox-selected-ssh.tar.gz")
	archiveFile, err := os.OpenFile(archive, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	compressed := gzip.NewWriter(archiveFile)
	writer := tar.NewWriter(compressed)
	for _, directory := range []string{
		"metadata/",
		"payload/",
		"payload/service-root/",
		"payload/service-root/secrets/",
		"payload/service-root/state/",
		"payload/service-root/mailbox/",
		"payload/service-root/mailboxes/",
		"payload/service-root/backups/",
		"payload/service-root/toolchains/",
		"payload/service-root/toolchains/go1.27.1/",
		"payload/service-root/toolchains/go1.27.1/bin/",
		"payload/ssh/",
		"source/",
	} {
		if err := writer.WriteHeader(&tar.Header{Name: directory, Typeflag: tar.TypeDir, Mode: 0o700}); err != nil {
			t.Fatal(err)
		}
	}
	fileNames = fileNames[:0]
	for name := range files {
		fileNames = append(fileNames, name)
	}
	sort.Strings(fileNames)
	for _, name := range fileNames {
		contents := files[name]
		if err := writer.WriteHeader(&tar.Header{Name: name, Typeflag: tar.TypeReg, Mode: 0o600, Size: int64(len(contents))}); err != nil {
			t.Fatal(err)
		}
		if _, err := writer.Write(contents); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := compressed.Close(); err != nil {
		t.Fatal(err)
	}
	if err := archiveFile.Close(); err != nil {
		t.Fatal(err)
	}
	archiveBytes, err := os.ReadFile(archive)
	if err != nil {
		t.Fatal(err)
	}
	archiveDigest := sha256.Sum256(archiveBytes)
	checksum := hex.EncodeToString(archiveDigest[:]) + "  " + filepath.Base(archive) + "\n"
	if err := os.WriteFile(archive+".sha256", []byte(checksum), 0o600); err != nil {
		t.Fatal(err)
	}
	return archive
}
