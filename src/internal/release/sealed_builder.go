package release

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

const (
	buildArtifactName = "artifact"
	buildRequestLimit = 4096
)

var ErrInvalidBuildRequest = errors.New("invalid sealed build request")

// BuildRequest deliberately contains only an opaque, one-use authorization ID.
// Paths, source identities, toolchain inputs, evidence and keys are resolved by
// fixed trusted components after this request has been authenticated.
type BuildRequest struct {
	AuthorizationID string `json:"authorization_id"`
}

func ParseBuildRequest(raw []byte) (BuildRequest, error) {
	var request BuildRequest
	if len(raw) == 0 || len(raw) > buildRequestLimit || decodeStrict(raw, &request) != nil || !isLowerHexDigest(request.AuthorizationID) {
		return BuildRequest{}, ErrInvalidBuildRequest
	}
	return request, nil
}

// VerifiedBuildJob is returned only by the installed authorization resolver.
// The resolver verifies the clean checked-in source SHA and its Gitea/GitHub
// distribution identities before exposing this fixed build tuple.
type VerifiedBuildJob struct {
	HostID               string
	ReleaseTrack         string
	Component            string
	GOOS                 string
	GOARCH               string
	SourceCommitID       string
	GiteaCommitID        string
	GitHubCommitID       string
	ToolchainDigest      string
	BuildInputDigest     string
	TemplateSchemaDigest string
	EvidenceRefs         []string
}

type VerifiedBuildJobResolver interface {
	ResolveVerifiedBuildJob(context.Context, string) (VerifiedBuildJob, error)
}

// FixedSourceExporter exports the exact resolved commit without caller-supplied
// repository, ref, or destination paths. Exported files are made read-only
// before the builder can read the workspace.
type FixedSourceExporter interface {
	ExportCommit(context.Context, string, string) error
}

// FixedBuildInvocation is constructed by SealedBuildService. Implementations
// use their installed executable, arguments, environment, toolchain and input
// set; they must run under BuilderUID and return only after the process tree is
// stopped and reaped. Workspace/output paths are generated under staging.
type FixedBuildInvocation struct {
	HostID           string
	Component        string
	GOOS             string
	GOARCH           string
	WorkspacePath    string
	OutputPath       string
	ToolchainDigest  string
	BuildInputDigest string
	BuilderUID       uint32
	BuilderGID       uint32
}

type FixedBuildExecutor interface {
	BuilderIdentity() (uid, gid uint32)
	Build(context.Context, FixedBuildInvocation) error
}

// HostBuildRecordPublisher is the separate signer/outbox boundary. It receives
// a validated payload only; it never receives a source path, artifact path, or
// signing-key bytes from the builder process.
type HostBuildRecordPublisher interface {
	PublishHostBuild(context.Context, []byte) (recordID string, err error)
}

type SealedBuildPolicy struct {
	HostID          string
	StagingRoot     string
	ServiceUID      uint32
	BuilderUID      uint32
	BuilderGID      uint32
	MaxArtifactSize int64
}

type SealedBuildService struct {
	policy    SealedBuildPolicy
	resolver  VerifiedBuildJobResolver
	exporter  FixedSourceExporter
	executor  FixedBuildExecutor
	publisher HostBuildRecordPublisher
	now       func() time.Time
}

func NewSealedBuildService(policy SealedBuildPolicy, resolver VerifiedBuildJobResolver, exporter FixedSourceExporter, executor FixedBuildExecutor, publisher HostBuildRecordPublisher) (*SealedBuildService, error) {
	if os.Geteuid() != 0 || policy.ServiceUID != 0 || policy.BuilderUID == 0 || policy.BuilderUID == policy.ServiceUID ||
		policy.HostID == "" || !filepath.IsAbs(policy.StagingRoot) || policy.MaxArtifactSize <= 0 ||
		resolver == nil || exporter == nil || executor == nil || publisher == nil {
		return nil, fmt.Errorf("%w: root-owned service and distinct non-root builder policy required", ErrInvalidBuildRequest)
	}
	uid, gid := executor.BuilderIdentity()
	if uid != policy.BuilderUID || gid != policy.BuilderGID {
		return nil, fmt.Errorf("%w: executor identity differs from installed builder identity", ErrInvalidBuildRequest)
	}
	if err := verifyPrivateDirectory(policy.StagingRoot, policy.ServiceUID); err != nil {
		return nil, fmt.Errorf("%w: staging root: %v", ErrInvalidBuildRequest, err)
	}
	return &SealedBuildService{policy: policy, resolver: resolver, exporter: exporter, executor: executor, publisher: publisher, now: time.Now}, nil
}

// AttestBuild resolves every build input from trusted local state, exports the
// exact source commit, invokes the fixed executor, seals a private copy of its
// output, then asks the independent signer/outbox to publish the canonical
// attestation. It returns only the immutable record ID.
func (s *SealedBuildService) AttestBuild(ctx context.Context, authorizationID string) (recordID string, resultErr error) {
	if !isLowerHexDigest(authorizationID) {
		return "", ErrInvalidBuildRequest
	}
	job, err := s.resolver.ResolveVerifiedBuildJob(ctx, authorizationID)
	if err != nil {
		return "", err
	}
	if err := validateVerifiedBuildJob(job, s.policy.HostID); err != nil {
		return "", err
	}
	uid, gid := s.executor.BuilderIdentity()
	if uid != s.policy.BuilderUID || gid != s.policy.BuilderGID || uid == 0 {
		return "", fmt.Errorf("%w: builder identity changed", ErrInvalidBuildRequest)
	}

	jobDir, err := os.MkdirTemp(s.policy.StagingRoot, ".build-")
	if err != nil {
		return "", err
	}
	defer func() {
		if cleanupErr := cleanupBuildWorkspace(jobDir); resultErr == nil && cleanupErr != nil {
			resultErr = cleanupErr
		}
	}()
	if err = os.Chmod(jobDir, 0o711); err != nil {
		return "", err
	}
	workspace := filepath.Join(jobDir, "source")
	if err = os.Mkdir(workspace, 0o700); err != nil {
		return "", err
	}
	if err = s.exporter.ExportCommit(ctx, job.SourceCommitID, workspace); err != nil {
		return "", err
	}
	if err = makeExportReadOnly(workspace); err != nil {
		return "", err
	}
	if err = os.Chmod(jobDir, 0o711); err != nil {
		return "", err
	}
	outputDir := filepath.Join(jobDir, "output")
	if err = os.Mkdir(outputDir, 0o700); err != nil {
		return "", err
	}
	if uint32(os.Geteuid()) != s.policy.BuilderUID {
		if err = os.Chown(outputDir, int(s.policy.BuilderUID), int(s.policy.BuilderGID)); err != nil {
			return "", fmt.Errorf("assign fixed builder output ownership: %w", err)
		}
	}
	if err = os.Chmod(outputDir, 0o700); err != nil {
		return "", err
	}
	invocation := FixedBuildInvocation{
		HostID: job.HostID, Component: job.Component, GOOS: job.GOOS, GOARCH: job.GOARCH,
		WorkspacePath: workspace, OutputPath: filepath.Join(outputDir, buildArtifactName),
		ToolchainDigest: job.ToolchainDigest, BuildInputDigest: job.BuildInputDigest,
		BuilderUID: s.policy.BuilderUID, BuilderGID: s.policy.BuilderGID,
	}
	if err = s.executor.Build(ctx, invocation); err != nil {
		return "", err
	}
	sealed, digest, err := s.sealOutput(outputDir)
	if err != nil {
		return "", err
	}
	defer sealed.Close()

	payload := hostBuildPayload{
		HostID: job.HostID, ReleaseTrack: job.ReleaseTrack, Component: job.Component, GOOS: job.GOOS, GOARCH: job.GOARCH,
		SourceCommitID: job.SourceCommitID, GiteaCommitID: job.GiteaCommitID, GitHubCommitID: job.GitHubCommitID,
		ToolchainDigest: job.ToolchainDigest, BuildInputDigest: job.BuildInputDigest,
		ArtifactDigest: "sha256:" + digest, TemplateSchemaDigest: job.TemplateSchemaDigest,
		EvidenceRefs: append([]string(nil), job.EvidenceRefs...), BuildTimestamp: s.now().UTC().Format(time.RFC3339Nano),
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	if err = ValidateHostBuildAttestation(raw); err != nil {
		return "", err
	}
	recordID, err = s.publisher.PublishHostBuild(ctx, raw)
	if err != nil {
		return "", err
	}
	if !isLowerHexDigest(recordID) {
		return "", fmt.Errorf("%w: publisher returned invalid record ID", ErrInvalidBuildRequest)
	}
	return recordID, nil
}

func validateVerifiedBuildJob(job VerifiedBuildJob, expectedHost string) error {
	if job.HostID != expectedHost || !isMatrixTuple(job.HostID, job.ReleaseTrack, job.Component, job.GOOS, job.GOARCH) ||
		!equalSourceCommits(job.SourceCommitID, job.GiteaCommitID, job.GitHubCommitID) ||
		!validSourceCommitID(job.SourceCommitID) ||
		!isDigest(job.ToolchainDigest) || !isDigest(job.BuildInputDigest) || !isDigest(job.TemplateSchemaDigest) ||
		!sortedUnique(job.EvidenceRefs) || len(job.EvidenceRefs) == 0 {
		return fmt.Errorf("%w: resolved job is not a canonical host-build tuple", ErrInvalidBuildRequest)
	}
	for _, ref := range job.EvidenceRefs {
		if !isDigest(ref) {
			return fmt.Errorf("%w: invalid evidence reference", ErrInvalidBuildRequest)
		}
	}
	return nil
}

func validSourceCommitID(value string) bool {
	if len(value) != 40 {
		return false
	}
	for _, character := range value {
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return false
		}
	}
	return true
}

func (s *SealedBuildService) sealOutput(outputDir string) (io.Closer, string, error) {
	dirFD, err := unix.Open(outputDir, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, "", fmt.Errorf("%w: fixed output directory is unsafe: %v", ErrInvalidBuildRequest, err)
	}
	defer unix.Close(dirFD)
	fd, err := unix.Openat(dirFD, buildArtifactName, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if err != nil {
		return nil, "", fmt.Errorf("%w: refusing symlink or missing build output: %v", ErrInvalidBuildRequest, err)
	}
	var st unix.Stat_t
	if err = unix.Fstat(fd, &st); err != nil {
		unix.Close(fd)
		return nil, "", err
	}
	if st.Mode&unix.S_IFMT != unix.S_IFREG || st.Uid != s.policy.BuilderUID || st.Size <= 0 || st.Size > s.policy.MaxArtifactSize || st.Mode&0o022 != 0 {
		unix.Close(fd)
		return nil, "", fmt.Errorf("%w: output is not a bounded builder-owned regular file", ErrInvalidBuildRequest)
	}
	source := os.NewFile(uintptr(fd), buildArtifactName)
	defer source.Close()

	sealedDir, err := os.MkdirTemp(s.policy.StagingRoot, ".sealed-")
	if err != nil {
		return nil, "", err
	}
	// The sealed directory remains service-owned and inaccessible to the builder.
	if err = os.Chmod(sealedDir, 0o700); err != nil {
		os.RemoveAll(sealedDir)
		return nil, "", err
	}
	sealedDirFD, err := unix.Open(sealedDir, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		os.RemoveAll(sealedDir)
		return nil, "", err
	}
	sealedFD, err := unix.Openat(sealedDirFD, buildArtifactName, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0o400)
	if err != nil {
		unix.Close(sealedDirFD)
		os.RemoveAll(sealedDir)
		return nil, "", err
	}
	sealedOutput := os.NewFile(uintptr(sealedFD), buildArtifactName)
	hash := sha256.New()
	written, copyErr := io.Copy(io.MultiWriter(sealedOutput, hash), io.LimitReader(source, s.policy.MaxArtifactSize+1))
	if copyErr != nil || written != st.Size || written > s.policy.MaxArtifactSize {
		sealedOutput.Close()
		unix.Close(sealedDirFD)
		os.RemoveAll(sealedDir)
		if copyErr != nil {
			return nil, "", copyErr
		}
		return nil, "", fmt.Errorf("%w: output changed or exceeded its size limit while sealing", ErrInvalidBuildRequest)
	}
	if err = unix.Fchmod(sealedFD, 0o400); err == nil {
		err = unix.Fsync(sealedFD)
	}
	if err == nil {
		err = unix.Fsync(sealedDirFD)
	}
	if err != nil {
		sealedOutput.Close()
		unix.Close(sealedDirFD)
		os.RemoveAll(sealedDir)
		return nil, "", err
	}
	unix.Close(sealedDirFD)
	// Keep the private directory until the descriptor is closed. The descriptor
	// and bytes are never exposed to the request caller or builder process.
	return &cleanupFile{File: sealedOutput, dir: sealedDir}, hex.EncodeToString(hash.Sum(nil)), nil
}

type cleanupFile struct {
	*os.File
	dir string
}

func (f *cleanupFile) Close() error {
	err := f.File.Close()
	removeErr := os.RemoveAll(f.dir)
	if err != nil {
		return err
	}
	return removeErr
}

func makeExportReadOnly(root string) error {
	return filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		info, err := os.Lstat(path)
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 || (!info.IsDir() && !info.Mode().IsRegular()) {
			return fmt.Errorf("%w: source export contains a link or special file", ErrInvalidBuildRequest)
		}
		if info.IsDir() {
			return os.Chmod(path, 0o555)
		}
		return os.Chmod(path, 0o444)
	})
}

func cleanupBuildWorkspace(root string) error {
	if err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return os.Chmod(path, 0o700)
		}
		return nil
	}); err != nil {
		return err
	}
	return os.RemoveAll(root)
}

func verifyPrivateDirectory(path string, ownerUID uint32) error {
	fd, err := openAbsoluteDirectoryNoSymlinks(path)
	if err != nil {
		return err
	}
	defer unix.Close(fd)
	var st unix.Stat_t
	if err = unix.Fstat(fd, &st); err != nil {
		return err
	}
	if st.Uid != ownerUID || st.Mode&unix.S_IFMT != unix.S_IFDIR || st.Mode&0o077 != 0 {
		return fmt.Errorf("directory owner or mode is not private")
	}
	return nil
}

func openAbsoluteDirectoryNoSymlinks(path string) (int, error) {
	if !filepath.IsAbs(path) || strings.Contains(path, "\x00") {
		return -1, fmt.Errorf("directory path must be absolute")
	}
	parts := strings.Split(strings.Trim(filepath.Clean(path), string(os.PathSeparator)), string(os.PathSeparator))
	fd, err := unix.Open(string(os.PathSeparator), unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return -1, err
	}
	for _, part := range parts {
		if part == "" || part == "." || part == ".." {
			unix.Close(fd)
			return -1, fmt.Errorf("invalid directory component")
		}
		next, openErr := unix.Openat(fd, part, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
		unix.Close(fd)
		if openErr != nil {
			return -1, openErr
		}
		fd = next
	}
	return fd, nil
}

// SignedHostBuildOutbox keeps the host key and output directory inside the
// trusted signer process. Callers receive only the record ID.
type SignedHostBuildOutbox struct {
	directory string
	ownerUID  uint32
	signer    SigningIdentity
}

func NewSignedHostBuildOutbox(directory string, ownerUID uint32, signer SigningIdentity) (*SignedHostBuildOutbox, error) {
	if os.Geteuid() != 0 || ownerUID != 0 || signer.Role != SignerRoleHostAttestation || signer.KeyID == "" || signer.HostID == "" || len(signer.PrivateKey) != ed25519.PrivateKeySize {
		return nil, fmt.Errorf("%w: invalid host attestation signer", ErrInvalidBuildRequest)
	}
	if err := verifyPrivateDirectory(directory, ownerUID); err != nil {
		return nil, fmt.Errorf("%w: outbox directory: %v", ErrInvalidBuildRequest, err)
	}
	signer.PrivateKey = append(ed25519.PrivateKey(nil), signer.PrivateKey...)
	return &SignedHostBuildOutbox{directory: directory, ownerUID: ownerUID, signer: signer}, nil
}

func (o *SignedHostBuildOutbox) PublishHostBuild(ctx context.Context, payload []byte) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if err := ValidateHostBuildAttestation(payload); err != nil {
		return "", err
	}
	var build hostBuildPayload
	if err := decodeStrict(payload, &build); err != nil || build.HostID != o.signer.HostID {
		return "", fmt.Errorf("%w: payload is not bound to the local signer host", ErrInvalidBuildRequest)
	}
	envelopeBytes, err := SignEnvelope("host_build_attestation", payload, o.signer)
	if err != nil {
		return "", err
	}
	var envelope Envelope
	if err = decodeStrict(envelopeBytes, &envelope); err != nil || !isLowerHexDigest(envelope.RecordID) {
		return "", ErrInvalidEnvelope
	}
	if err = publishImmutableRecord(o.directory, o.ownerUID, envelope.RecordID, envelopeBytes); err != nil {
		return "", err
	}
	return envelope.RecordID, nil
}

func publishImmutableRecord(directory string, ownerUID uint32, recordID string, data []byte) error {
	if !isLowerHexDigest(recordID) {
		return ErrInvalidBuildRequest
	}
	dirFD, err := openAbsoluteDirectoryNoSymlinks(directory)
	if err != nil {
		return err
	}
	defer unix.Close(dirFD)
	var dirStat unix.Stat_t
	if err = unix.Fstat(dirFD, &dirStat); err != nil {
		return err
	}
	if dirStat.Uid != ownerUID || dirStat.Mode&unix.S_IFMT != unix.S_IFDIR || dirStat.Mode&0o077 != 0 {
		return fmt.Errorf("outbox directory owner or mode changed")
	}
	target := recordID + ".json"
	if existing, readErr := readAtNoFollow(dirFD, target, ownerUID, 1<<20); readErr == nil {
		if string(existing) == string(data) {
			return nil
		}
		return fmt.Errorf("immutable outbox record collision")
	} else if !errors.Is(readErr, unix.ENOENT) {
		return readErr
	}
	random := make([]byte, 16)
	if _, err = rand.Read(random); err != nil {
		return err
	}
	temporary := ".record-" + hex.EncodeToString(random)
	fd, err := unix.Openat(dirFD, temporary, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0o600)
	if err != nil {
		return err
	}
	file := os.NewFile(uintptr(fd), temporary)
	if _, err = file.Write(data); err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		unix.Unlinkat(dirFD, temporary, 0)
		return err
	}
	if err = unix.Linkat(dirFD, temporary, dirFD, target, 0); err != nil {
		unix.Unlinkat(dirFD, temporary, 0)
		if errors.Is(err, unix.EEXIST) {
			existing, readErr := readAtNoFollow(dirFD, target, ownerUID, 1<<20)
			if readErr == nil && string(existing) == string(data) {
				return nil
			}
		}
		return err
	}
	if err = unix.Unlinkat(dirFD, temporary, 0); err != nil {
		return err
	}
	return unix.Fsync(dirFD)
}

func readAtNoFollow(dirFD int, name string, ownerUID uint32, limit int64) ([]byte, error) {
	fd, err := unix.Openat(dirFD, name, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), name)
	defer file.Close()
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil {
		return nil, err
	}
	if st.Mode&unix.S_IFMT != unix.S_IFREG || st.Uid != ownerUID || st.Mode&0o022 != 0 || st.Size < 0 || st.Size > limit {
		return nil, fmt.Errorf("outbox target is not a bounded private regular file")
	}
	return io.ReadAll(io.LimitReader(file, limit+1))
}
