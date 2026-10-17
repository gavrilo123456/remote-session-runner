package release

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestLanesL307RequestContainsOnlyOpaqueAuthorization(t *testing.T) {
	validID := strings.Repeat("a", 64)
	request, err := ParseBuildRequest([]byte(`{"authorization_id":"` + validID + `"}`))
	if err != nil || request.AuthorizationID != validID {
		t.Fatalf("valid opaque request rejected: request=%+v err=%v", request, err)
	}
	for _, raw := range []string{
		`{"authorization_id":"` + validID + `","artifact_path":"/tmp/x"}`,
		`{"authorization_id":"` + validID + `","source_commit_id":"` + strings.Repeat("b", 40) + `"}`,
		`{"authorization_id":"../artifact"}`,
		`{"authorization_id":"` + validID + `"} {}`,
		`{"authorization_id":"` + validID + `","evidence_refs":[]}`,
	} {
		if _, err := ParseBuildRequest([]byte(raw)); !errors.Is(err, ErrInvalidBuildRequest) {
			t.Errorf("unsafe/open build request accepted: raw=%s err=%v", raw, err)
		}
	}
	if got := reflect.TypeOf(BuildRequest{}).NumField(); got != 1 {
		t.Fatalf("BuildRequest exposes %d fields; want only the opaque authorization ID", got)
	}
}

func TestLanesL307InstalledServiceRequiresRootAndDedicatedBuilderIdentity(t *testing.T) {
	fixture := newL307Fixture(t)
	if _, err := NewSealedBuildService(fixture.policy, fixture.resolver, fixture.exporter, fixture.executor, fixture.publisher, fixture.cache); !errors.Is(err, ErrInvalidBuildRequest) {
		t.Fatalf("unprivileged service with a shared build identity was accepted: %v", err)
	}
	requestedOwner := uint32(os.Getuid())
	if requestedOwner == 0 {
		requestedOwner = 1
	}
	if _, err := NewSignedHostBuildOutbox(fixture.outboxDir, requestedOwner, fixture.signer); !errors.Is(err, ErrInvalidBuildRequest) {
		t.Fatalf("unprivileged process was allowed to construct the signing outbox: %v", err)
	}
}

func TestLanesL307FixedBuildSealsSignsAndPublishesExactArtifact(t *testing.T) {
	fixture := newL307Fixture(t)
	service := fixture.service(t, fixture.publisher)
	recordID, err := service.AttestBuild(context.Background(), fixture.authorizationID)
	if err != nil {
		t.Fatal(err)
	}
	if !isLowerHexDigest(recordID) || fixture.exporter.calls != 1 || fixture.executor.calls != 1 {
		t.Fatalf("build did not complete exactly once: id=%q export=%d build=%d", recordID, fixture.exporter.calls, fixture.executor.calls)
	}
	if _, err := service.AttestBuild(context.Background(), fixture.authorizationID); !errors.Is(err, ErrInvalidBuildRequest) {
		t.Fatalf("one-use build authorization replay was accepted: %v", err)
	}
	if fixture.exporter.calls != 1 || fixture.executor.calls != 1 {
		t.Fatalf("replayed authorization re-executed the build: export=%d build=%d", fixture.exporter.calls, fixture.executor.calls)
	}
	if fixture.cache.calls != 1 || fixture.cache.recordID != recordID || string(fixture.cache.artifact) != string(fixture.executor.artifact) {
		t.Fatalf("sealed output was not handed to cache exactly once: %+v", fixture.cache)
	}
	invocation := fixture.executor.last
	if invocation.WorkspacePath == "" || invocation.OutputPath == "" || invocation.BuilderUID != uint32(os.Geteuid()) ||
		invocation.ToolchainDigest != fixture.job.ToolchainDigest || invocation.BuildInputDigest != fixture.job.BuildInputDigest {
		t.Fatalf("fixed executor did not receive the resolved trusted invocation: %+v", invocation)
	}
	if strings.Contains(invocation.WorkspacePath, fixture.authorizationID) || strings.Contains(invocation.OutputPath, fixture.authorizationID) {
		t.Fatal("caller-controlled authorization ID influenced a filesystem path")
	}
	encoded, err := os.ReadFile(filepath.Join(fixture.outboxDir, recordID+".json"))
	if err != nil {
		t.Fatal(err)
	}
	trusted := map[string]TrustedKey{fixture.signer.KeyID: {KeyID: fixture.signer.KeyID, Role: SignerRoleHostAttestation, HostID: fixture.signer.HostID, PublicKey: fixture.signer.PrivateKey.Public().(ed25519.PublicKey)}}
	envelope, err := VerifyEnvelope(encoded, trusted)
	if err != nil || envelope.RecordType != "host_build_attestation" {
		t.Fatalf("outbox record did not verify as host build attestation: envelope=%+v err=%v", envelope, err)
	}
	if err := ValidateHostBuildAttestation(envelope.Payload); err != nil {
		t.Fatalf("published build payload rejected: %v", err)
	}
	var payload hostBuildPayload
	if err := decodeStrict(envelope.Payload, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.ArtifactDigest != l307Digest(string(fixture.executor.artifact)) || payload.SourceCommitID != fixture.job.SourceCommitID ||
		payload.ToolchainDigest != fixture.job.ToolchainDigest || !reflect.DeepEqual(payload.EvidenceRefs, fixture.job.EvidenceRefs) {
		t.Fatalf("attestation does not bind the exact resolved artifact and provenance: %+v", payload)
	}
}

func TestLanesL307ResolvedJobRejectsUnsafeOrInconsistentEvidenceBeforeExecution(t *testing.T) {
	cases := []struct {
		name string
		edit func(*VerifiedBuildJob)
	}{
		{"unequal source mirrors", func(job *VerifiedBuildJob) { job.GitHubCommitID = strings.Repeat("b", 40) }},
		{"malformed commit", func(job *VerifiedBuildJob) {
			job.SourceCommitID, job.GiteaCommitID, job.GitHubCommitID = "clean", "clean", "clean"
		}},
		{"unsupported tuple", func(job *VerifiedBuildJob) { job.Component = "arbitrary-command" }},
		{"unordered evidence", func(job *VerifiedBuildJob) {
			job.EvidenceRefs = []string{"sha256:" + strings.Repeat("b", 64), "sha256:" + strings.Repeat("a", 64)}
		}},
		{"caller-selected evidence path", func(job *VerifiedBuildJob) { job.EvidenceRefs = []string{"/tmp/evidence.json"} }},
		{"malformed toolchain digest", func(job *VerifiedBuildJob) { job.ToolchainDigest = "latest" }},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			fixture := newL307Fixture(t)
			test.edit(&fixture.job)
			service := fixture.service(t, fixture.publisher)
			if _, err := service.AttestBuild(context.Background(), fixture.authorizationID); !errors.Is(err, ErrInvalidBuildRequest) {
				t.Fatalf("unsafe resolved job accepted: %v", err)
			}
			if fixture.exporter.calls != 0 || fixture.executor.calls != 0 {
				t.Fatalf("invalid job reached source/build side effects: export=%d build=%d", fixture.exporter.calls, fixture.executor.calls)
			}
		})
	}
}

func TestLanesL307SealedCopyIsIndependentFromBuilderOutputAndOutboxRetriesIdempotently(t *testing.T) {
	fixture := newL307Fixture(t)
	mutating := l307MutatingPublisher{delegate: fixture.publisher, mutate: func() {
		if err := os.WriteFile(fixture.executor.last.OutputPath, []byte("changed after sealing"), 0o600); err != nil {
			t.Fatalf("mutate scratch output: %v", err)
		}
	}}
	service := fixture.service(t, mutating)
	recordID, err := service.AttestBuild(context.Background(), fixture.authorizationID)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := os.ReadFile(filepath.Join(fixture.outboxDir, recordID+".json"))
	if err != nil {
		t.Fatal(err)
	}
	var envelope Envelope
	if err := json.Unmarshal(encoded, &envelope); err != nil {
		t.Fatal(err)
	}
	var payload hostBuildPayload
	if err := decodeStrict(envelope.Payload, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.ArtifactDigest != l307Digest(string(fixture.executor.artifact)) {
		t.Fatalf("attestation followed mutable scratch bytes instead of its sealed copy: got %s", payload.ArtifactDigest)
	}
	if err := publishImmutableRecord(fixture.outboxDir, uint32(os.Getuid()), recordID, encoded); err != nil {
		t.Fatalf("identical outbox retry was not idempotent: %v", err)
	}
	if err := publishImmutableRecord(fixture.outboxDir, uint32(os.Getuid()), recordID, []byte("different")); err == nil {
		t.Fatal("outbox allowed replacement of an immutable record")
	}
}

func TestLanesL307RejectsSymlinkOutputAndBoundOverflow(t *testing.T) {
	for _, test := range []struct {
		name    string
		prepare func(*testing.T, *l307FakeExecutor)
		max     int64
	}{
		{"symlink", func(t *testing.T, executor *l307FakeExecutor) {
			original := func(path string) error { return os.WriteFile(path, executor.artifact, 0o600) }
			executor.writeArtifact = func(path string) error {
				if err := original(path); err != nil {
					return err
				}
				target := path + ".real"
				if err := os.Rename(path, target); err != nil {
					return err
				}
				return os.Symlink(target, path)
			}
		}, 1 << 20},
		{"too large", func(*testing.T, *l307FakeExecutor) {}, 4},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newL307Fixture(t)
			fixture.policy.MaxArtifactSize = test.max
			test.prepare(t, fixture.executor)
			service := fixture.service(t, fixture.publisher)
			if _, err := service.AttestBuild(context.Background(), fixture.authorizationID); !errors.Is(err, ErrInvalidBuildRequest) {
				t.Fatalf("unsafe artifact accepted: %v", err)
			}
			entries, err := os.ReadDir(fixture.outboxDir)
			if err != nil {
				t.Fatal(err)
			}
			if len(entries) != 0 {
				t.Fatalf("invalid artifact produced an outbox record: %v", entries)
			}
		})
	}
}

func TestLanesL307NativeSealedOutboxUsesNoFollowAndDurableRecordPublication(t *testing.T) {
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		t.Fatalf("native sealed builder test unsupported on %s", runtime.GOOS)
	}
	fixture := newL307Fixture(t)
	if runtime.GOOS == "linux" {
		hostID := os.Getenv("LANES_NATIVE_HOST_ID")
		if hostID == "" {
			hostID = "linux-host"
			if runtime.GOARCH == "arm64" {
				hostID = "sandbox-host"
			}
		}
		if hostID != "linux-host" && hostID != "sandbox-host" && hostID != "factory-sandbox" {
			t.Fatalf("LANES_NATIVE_HOST_ID must identify the executing Ubuntu host; got %q", hostID)
		}
		fixture.job.HostID = hostID
		fixture.job.GOOS = "linux"
		fixture.job.GOARCH = "amd64"
		fixture.job.Component = "runnerd"
		if hostID == "sandbox-host" {
			fixture.job.GOARCH = "arm64"
		}
		fixture.signer.HostID = hostID
		fixture.publisher = l307TestPublisher(t, fixture.outboxDir, fixture.signer)
	} else {
		hostID := os.Getenv("LANES_NATIVE_HOST_ID")
		if hostID == "" {
			hostID = "mac-control-plane"
		}
		if hostID != "mac-control-plane" {
			t.Fatalf("LANES_NATIVE_HOST_ID must be mac-control-plane on Darwin")
		}
	}
	fixture.policy.HostID = fixture.job.HostID
	service := fixture.service(t, fixture.publisher)
	recordID, err := service.AttestBuild(context.Background(), fixture.authorizationID)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(fixture.outboxDir, recordID+".json")
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
		t.Fatalf("native outbox record mode/type = %v, err=%v", info, err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := VerifyEnvelope(raw, map[string]TrustedKey{fixture.signer.KeyID: {
		KeyID: fixture.signer.KeyID, Role: SignerRoleHostAttestation, HostID: fixture.signer.HostID,
		PublicKey: fixture.signer.PrivateKey.Public().(ed25519.PublicKey),
	}}); err != nil {
		t.Fatalf("native fsynced outbox record failed signature verification: %v", err)
	}
}

type l307Fixture struct {
	root            string
	stagingRoot     string
	outboxDir       string
	authorizationID string
	job             VerifiedBuildJob
	policy          SealedBuildPolicy
	resolver        *l307FakeResolver
	exporter        *l307FakeExporter
	executor        *l307FakeExecutor
	signer          SigningIdentity
	publisher       *SignedHostBuildOutbox
	cache           *l307FakeArtifactCache
}

func newL307Fixture(t *testing.T) *l307Fixture {
	t.Helper()
	root := ""
	if runtime.GOOS == "darwin" {
		var err error
		root, err = os.MkdirTemp("/private/tmp", "lanes-l307-")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.RemoveAll(root) })
	} else {
		root = t.TempDir()
	}
	staging := filepath.Join(root, "staging")
	outbox := filepath.Join(root, "outbox")
	for _, path := range []string{staging, outbox} {
		if err := os.Mkdir(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	seed := sha256.Sum256([]byte("LANES-L3-07 isolated fixture signing key"))
	private := ed25519.NewKeyFromSeed(seed[:])
	signer := SigningIdentity{KeyID: "host-test-key", Role: SignerRoleHostAttestation, HostID: "mac-control-plane", PrivateKey: private}
	publisher := l307TestPublisher(t, outbox, signer)
	job := VerifiedBuildJob{
		HostID: "mac-control-plane", ReleaseTrack: "lane_runtime", Component: "runner", GOOS: "darwin", GOARCH: "arm64",
		SourceCommitID: strings.Repeat("a", 40), GiteaCommitID: strings.Repeat("a", 40), GitHubCommitID: strings.Repeat("a", 40),
		ToolchainDigest: l307Digest("toolchain"), BuildInputDigest: l307Digest("inputs"), TemplateSchemaDigest: l307Digest("template"),
		EvidenceRefs: []string{l307Digest("source-evidence")},
	}
	return &l307Fixture{
		root: root, stagingRoot: staging, outboxDir: outbox, authorizationID: strings.Repeat("c", 64), job: job,
		policy:   SealedBuildPolicy{HostID: job.HostID, StagingRoot: staging, ServiceUID: uint32(os.Getuid()), BuilderUID: uint32(os.Geteuid()), BuilderGID: uint32(os.Getegid()), MaxArtifactSize: 1 << 20},
		resolver: &l307FakeResolver{job: job}, exporter: &l307FakeExporter{}, executor: &l307FakeExecutor{artifact: []byte("sealed binary contents")},
		signer: signer, publisher: publisher, cache: &l307FakeArtifactCache{},
	}
}

func (f *l307Fixture) service(t *testing.T, publisher HostBuildRecordPublisher) *SealedBuildService {
	t.Helper()
	f.resolver.job = f.job
	if f.executor.writeArtifact == nil {
		f.executor.writeArtifact = func(path string) error { return os.WriteFile(path, f.executor.artifact, 0o600) }
	}
	// Native/H fixtures exercise the pipeline with a private temp root and the
	// current test UID. The installed constructor separately requires root plus
	// a distinct dedicated builder UID before any real host deployment.
	if err := verifyPrivateDirectory(f.policy.StagingRoot, f.policy.ServiceUID); err != nil {
		t.Fatal(err)
	}
	uid, gid := f.executor.BuilderIdentity()
	if uid != f.policy.BuilderUID || gid != f.policy.BuilderGID {
		t.Fatalf("fixture executor identity mismatch: %d:%d", uid, gid)
	}
	service := &SealedBuildService{policy: f.policy, resolver: f.resolver, exporter: f.exporter, executor: f.executor, publisher: publisher, cache: f.cache}
	service.now = func() time.Time { return time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC) }
	return service
}

func l307Digest(value string) string {
	digest := sha256.Sum256([]byte(value))
	return "sha256:" + hex.EncodeToString(digest[:])
}

func l307TestPublisher(t *testing.T, directory string, signer SigningIdentity) *SignedHostBuildOutbox {
	t.Helper()
	if err := verifyPrivateDirectory(directory, uint32(os.Getuid())); err != nil {
		t.Fatal(err)
	}
	signer.PrivateKey = append(ed25519.PrivateKey(nil), signer.PrivateKey...)
	return &SignedHostBuildOutbox{directory: directory, ownerUID: uint32(os.Getuid()), signer: signer}
}

type l307FakeResolver struct {
	job     VerifiedBuildJob
	claimed map[string]bool
}

func (f *l307FakeResolver) ClaimVerifiedBuildJob(_ context.Context, authorizationID string) (VerifiedBuildJob, error) {
	if authorizationID != strings.Repeat("c", 64) {
		return VerifiedBuildJob{}, ErrInvalidBuildRequest
	}
	if f.claimed == nil {
		f.claimed = make(map[string]bool)
	}
	if f.claimed[authorizationID] {
		return VerifiedBuildJob{}, ErrInvalidBuildRequest
	}
	f.claimed[authorizationID] = true
	return f.job, nil
}

type l307FakeExporter struct{ calls int }

func (f *l307FakeExporter) ExportCommit(_ context.Context, commit, destination string) error {
	f.calls++
	if commit != strings.Repeat("a", 40) && commit != strings.Repeat("b", 40) {
		return ErrInvalidBuildRequest
	}
	return os.WriteFile(filepath.Join(destination, "source.txt"), []byte(commit), 0o644)
}

type l307FakeExecutor struct {
	calls         int
	last          FixedBuildInvocation
	artifact      []byte
	writeArtifact func(string) error
}

func (f *l307FakeExecutor) BuilderIdentity() (uint32, uint32) {
	return uint32(os.Geteuid()), uint32(os.Getegid())
}

func (f *l307FakeExecutor) Build(_ context.Context, invocation FixedBuildInvocation) error {
	f.calls++
	f.last = invocation
	if invocation.ToolchainDigest == "" || invocation.BuildInputDigest == "" || invocation.BuilderUID == 0 {
		return ErrInvalidBuildRequest
	}
	return f.writeArtifact(invocation.OutputPath)
}

type l307MutatingPublisher struct {
	delegate HostBuildRecordPublisher
	mutate   func()
}

type l307FakeArtifactCache struct {
	calls    int
	recordID string
	artifact []byte
}

func (f *l307FakeArtifactCache) CacheBuiltArtifact(_ context.Context, recordID string, artifact *os.File, size int64) error {
	f.calls++
	f.recordID = recordID
	f.artifact = make([]byte, size)
	_, err := artifact.ReadAt(f.artifact, 0)
	return err
}

func (p l307MutatingPublisher) PublishHostBuild(ctx context.Context, payload []byte) (string, error) {
	p.mutate()
	return p.delegate.PublishHostBuild(ctx, payload)
}
