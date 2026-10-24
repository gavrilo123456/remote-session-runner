package releaseflow

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

type fakeBuildBackend struct {
	root        string
	built       int
	published   []string
	inspected   []string
	failPublish string
}

func (f *fakeBuildBackend) BuildNative(_ context.Context, host string, source Source, buildID, releaseID string) (BuildArtifact, error) {
	f.built++
	data := []byte("native artifact:" + source.Commit + ":" + host + ":" + buildID + ":" + releaseID)
	path := filepath.Join(f.root, host+".bundle")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return BuildArtifact{}, err
	}
	goos, goarch := "linux", "amd64"
	if host == "sandbox-host" {
		goarch = "arm64"
	}
	if host == "mac-control-plane" {
		goos, goarch = "darwin", "arm64"
	}
	bundle := Bundle{HostID: host, GOOS: goos, GOARCH: goarch, Toolchain: "go1.27.1", SourceRevision: source.Commit, DatabaseSchemaMax: 40, Components: testComponents(host), SHA256: digest(data), ManifestSHA: digest([]byte("manifest:" + host)), Bytes: int64(len(data))}
	settings := HostBuildSettings{HostID: host, ToolchainBinarySHA: digest([]byte("go1.27.1")), GOOS: goos, GOARCH: goarch, CGOEnabled: goos == "darwin", Flags: hostBuildFlags(source.Commit)}
	return BuildArtifact{Path: path, Bundle: bundle, Settings: settings}, nil
}
func (f *fakeBuildBackend) VerifyArtifact(_ context.Context, _ string, path, want string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if digest(data) != want {
		return ErrReleaseMismatch
	}
	return nil
}
func (f *fakeBuildBackend) PublishNative(_ context.Context, host string, _ Release, _ string) error {
	if host == f.failPublish {
		return errors.New("injected publication interruption")
	}
	f.published = append(f.published, host)
	return nil
}
func (f *fakeBuildBackend) InspectPublished(_ context.Context, host string, _ Release) error {
	f.inspected = append(f.inspected, host)
	return nil
}

func TestBuildReleaseResumesPublicationWithoutRebuilding(t *testing.T) {
	root := t.TempDir()
	resolver, commit := buildSourceFixture(t, root)
	store, err := NewStore(filepath.Join(root, "runtime", "operations"))
	if err != nil {
		t.Fatal(err)
	}
	backend := &fakeBuildBackend{root: filepath.Join(root, "artifacts"), failPublish: "sandbox-host"}
	if err := os.MkdirAll(backend.root, 0o700); err != nil {
		t.Fatal(err)
	}
	now := func() time.Time { return time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC) }
	receipts := fixtureSourceTestReceipts(commit)
	result, err := Build(context.Background(), store, resolver, backend, BuildRequest{Branch: "dev", Commit: commit, ReleaseID: "release-build-test", QualificationReceipts: receipts}, strings.Repeat("b", 40), now)
	if err == nil {
		t.Fatal("expected publication interruption")
	}
	if backend.built != 4 {
		t.Fatalf("native build count=%d", backend.built)
	}
	record, err := store.LoadBuild(result.Record.BuildID)
	if err != nil {
		t.Fatal(err)
	}
	if record.Stage != "publication_blocked" || len(record.PublishedHosts) != 1 || record.PublishedHosts[0] != "linux-host" {
		t.Fatalf("record=%+v", record)
	}
	backend.failPublish = ""
	resumed, err := Build(context.Background(), store, resolver, backend, BuildRequest{ResumeID: record.BuildID, ReleaseID: record.ReleaseID}, strings.Repeat("b", 40), now)
	if err != nil {
		t.Fatal(err)
	}
	if resumed.Outcome != "ready_without_activation" || backend.built != 4 || record.Source.Commit != commit || len(resumed.Record.PublishedHosts) != 4 {
		t.Fatalf("result=%+v builds=%d", resumed, backend.built)
	}
	if !reflect.DeepEqual(resumed.Record.PublishedHosts, HostOrder) {
		t.Fatalf("publication order=%v", resumed.Record.PublishedHosts)
	}
	if _, err := store.LoadRelease(record.ReleaseID); err != nil {
		t.Fatal(err)
	}
}

func TestBuildRejectsMissingHostQualificationBeforeNativeBuild(t *testing.T) {
	root := t.TempDir()
	resolver, commit := buildSourceFixture(t, root)
	store, err := NewStore(filepath.Join(root, "runtime", "operations"))
	if err != nil {
		t.Fatal(err)
	}
	backend := &fakeBuildBackend{root: filepath.Join(root, "artifacts")}
	if err := os.MkdirAll(backend.root, 0o700); err != nil {
		t.Fatal(err)
	}
	_, err = Build(context.Background(), store, resolver, backend, BuildRequest{Branch: "dev", Commit: commit, ReleaseID: "release-no-receipts"}, strings.Repeat("b", 40), time.Now)
	if err == nil || backend.built != 0 {
		t.Fatalf("build without four host qualification receipts was not rejected before build: err=%v built=%d", err, backend.built)
	}
}

func fixtureSourceTestReceipts(commit string) []SourceTestReceipt {
	receipts := make([]SourceTestReceipt, 0, len(HostOrder))
	for _, host := range HostOrder {
		goos, goarch := "linux", "amd64"
		if host == "sandbox-host" {
			goarch = "arm64"
		}
		if host == "mac-control-plane" {
			goos, goarch = "darwin", "arm64"
		}
		receipts = append(receipts, SourceTestReceipt{FormatVersion: FormatVersion, SourceCommit: commit, HostID: host,
			GOOS: goos, GOARCH: goarch, Toolchain: "go1.27.1", ToolchainBinarySHA256: digest([]byte("go1.27.1")),
			CoverageProfile: NativeFullCoverageProfile, Gates: map[string]string{"test": "passed", "vet": "passed", "build": "passed", "smoke": "passed"},
			EvidenceSHA256: digest([]byte("native source-test evidence:" + host + ":" + commit)), TestedAtUTC: time.Now().UTC().Format(time.RFC3339Nano)})
	}
	return receipts
}

func buildSourceFixture(t *testing.T, root string) (GitResolver, string) {
	t.Helper()
	work := filepath.Join(root, "source-work")
	primary := filepath.Join(root, "source-primary.git")
	mirror := filepath.Join(root, "source-mirror.git")
	initGitTestBranch(t, work, "dev")
	runGitTest(t, "-C", work, "config", "user.name", "Build fixture")
	runGitTest(t, "-C", work, "config", "user.email", "build@example.invalid")
	if err := os.WriteFile(filepath.Join(work, "README.md"), []byte("fixture"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGitTest(t, "-C", work, "add", "README.md")
	runGitTest(t, "-C", work, "commit", "-m", "fixture")
	commit := strings.TrimSpace(runGitTest(t, "-C", work, "rev-parse", "HEAD"))
	runGitTest(t, "init", "--bare", primary)
	runGitTest(t, "init", "--bare", mirror)
	runGitTest(t, "-C", work, "remote", "add", "origin", primary)
	runGitTest(t, "-C", work, "remote", "add", "github", mirror)
	runGitTest(t, "-C", work, "push", "origin", "dev")
	runGitTest(t, "-C", work, "push", "github", "dev")
	return GitResolver{Repository: work, TempRoot: root}, commit
}
