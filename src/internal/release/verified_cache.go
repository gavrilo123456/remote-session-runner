package release

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"

	"golang.org/x/sys/unix"
)

var (
	ErrVerifiedCache                = errors.New("verified release cache rejected operation")
	ErrVerifiedCacheUnavailable     = errors.New("verified candidate artifact is unavailable")
	ErrVerifiedCacheCapacity        = errors.New("verified release cache capacity exhausted")
	ErrVerifiedCacheRecordCollision = errors.New("immutable cache record collision")
)

const (
	verifiedCacheRecordLimit = 16 << 20
	verifiedBuildRecordLimit = 1 << 20
	verifiedCacheObjectLimit = int64(1<<63 - 1)
)

// VerifiedCacheTrust is read from the current protected anchor state for each
// operation. Public keys are never copied into cache records or backup data.
type VerifiedCacheTrust struct {
	Authorities map[string]TrustedKey
	Hosts       map[string]TrustedKey
}

type VerifiedCacheTrustResolver interface {
	CurrentVerifiedCacheTrust(context.Context) (VerifiedCacheTrust, error)
}

type HostBuildEnvelopeReader interface {
	ReadHostBuildEnvelope(context.Context, string) ([]byte, error)
}

// CandidateChain contains only signed canonical records. Its trust is always
// resolved separately from the current protected anchor state.
type CandidateChain struct {
	CandidateEnvelope        []byte            `json:"candidate_manifest_envelope"`
	ManagementEnvelope       []byte            `json:"host_management_manifest_envelope"`
	RuntimeBuildEnvelopes    map[string][]byte `json:"lane_runtime_build_envelopes"`
	ManagementBuildEnvelopes map[string][]byte `json:"host_management_build_envelopes"`
}

type VerifiedCachePolicy struct {
	HostID       string
	GOOS         string
	GOARCH       string
	CacheRoot    string
	BackupRoot   string
	OwnerUID     uint32
	CacheBudget  int64
	BackupBudget int64
}

type verifiedCacheStore struct {
	root       string
	objects    string
	builds     string
	candidates string
	quarantine string
	ownerUID   uint32
}

type VerifiedReleaseCache struct {
	policy VerifiedCachePolicy
	cache  verifiedCacheStore
	backup verifiedCacheStore
	builds HostBuildEnvelopeReader
	trust  VerifiedCacheTrustResolver
	mu     sync.Mutex
}

// NewVerifiedReleaseCache accepts only the canonical host paths, exact private
// root ownership, and separately mounted cache/backup devices. Installers must
// provision these roots before starting the helper; this constructor never
// creates them.
func NewVerifiedReleaseCache(policy VerifiedCachePolicy, builds HostBuildEnvelopeReader, trust VerifiedCacheTrustResolver) (*VerifiedReleaseCache, error) {
	if os.Geteuid() != 0 || policy.OwnerUID != 0 || builds == nil || trust == nil ||
		policy.CacheBudget <= 0 || policy.BackupBudget <= 0 {
		return nil, fmt.Errorf("%w: root-owned helper, positive budgets, and trusted record sources required", ErrVerifiedCache)
	}
	expectedCache, expectedBackup, expectedOS, expectedArch := fixedReleaseCachePaths(policy.HostID)
	if expectedCache == "" || policy.GOOS != expectedOS || policy.GOARCH != expectedArch ||
		policy.GOOS != runtime.GOOS || policy.GOARCH != runtime.GOARCH ||
		filepath.Clean(policy.CacheRoot) != expectedCache || filepath.Clean(policy.BackupRoot) != expectedBackup {
		return nil, fmt.Errorf("%w: host identity or fixed storage paths differ", ErrVerifiedCache)
	}
	cache, cacheDev, err := openVerifiedCacheStore(policy.CacheRoot, policy.OwnerUID)
	if err != nil {
		return nil, fmt.Errorf("%w: cache root: %v", ErrVerifiedCache, err)
	}
	backup, backupDev, err := openVerifiedCacheStore(policy.BackupRoot, policy.OwnerUID)
	if err != nil {
		return nil, fmt.Errorf("%w: backup root: %v", ErrVerifiedCache, err)
	}
	if err = validateCacheDeviceIdentity(cacheDev, backupDev); err != nil {
		return nil, fmt.Errorf("%w: cache and backup are not on distinct devices", ErrVerifiedCache)
	}
	return &VerifiedReleaseCache{policy: policy, cache: cache, backup: backup, builds: builds, trust: trust}, nil
}

func validateCacheDeviceIdentity(cacheDev, backupDev uint64) error {
	if cacheDev == backupDev {
		return fmt.Errorf("%w: cache and backup share a device", ErrVerifiedCache)
	}
	return nil
}

// newVerifiedReleaseCacheFixture validates all directory and identity rules
// except effective UID, canonical absolute production paths, and device
// separation. Tests use it only with isolated temporary roots.
func newVerifiedReleaseCacheFixture(policy VerifiedCachePolicy, builds HostBuildEnvelopeReader, trust VerifiedCacheTrustResolver) (*VerifiedReleaseCache, error) {
	if !isCanonicalHostID(policy.HostID) || !isMatrixPlatform(policy.HostID, policy.GOOS, policy.GOARCH) ||
		policy.CacheBudget <= 0 || policy.BackupBudget <= 0 || builds == nil || trust == nil {
		return nil, fmt.Errorf("%w: invalid fixture policy", ErrVerifiedCache)
	}
	cache, _, err := openVerifiedCacheStore(policy.CacheRoot, policy.OwnerUID)
	if err != nil {
		return nil, err
	}
	backup, _, err := openVerifiedCacheStore(policy.BackupRoot, policy.OwnerUID)
	if err != nil {
		return nil, err
	}
	return &VerifiedReleaseCache{policy: policy, cache: cache, backup: backup, builds: builds, trust: trust}, nil
}

func fixedReleaseCachePaths(hostID string) (cache, backup, goos, goarch string) {
	if !isCanonicalHostID(hostID) {
		return "", "", "", ""
	}
	goos, goarch = l302Platform(hostID)
	if goos == "darwin" {
		return "/Library/Application Support/RemoteSessionRunner-release-cache", "/Library/Application Support/RemoteSessionRunner-release-cache-backup", goos, goarch
	}
	return "/var/lib/remote-session-runner-release-cache", "/var/lib/remote-session-runner-release-cache-backup", goos, goarch
}

func isMatrixPlatform(hostID, goos, goarch string) bool {
	expectedOS, expectedArch := l302Platform(hostID)
	return goos == expectedOS && goarch == expectedArch
}

func openVerifiedCacheStore(root string, ownerUID uint32) (verifiedCacheStore, uint64, error) {
	fd, err := openAbsoluteDirectoryNoSymlinks(root)
	if err != nil {
		return verifiedCacheStore{}, 0, err
	}
	defer unix.Close(fd)
	var st unix.Stat_t
	if err = unix.Fstat(fd, &st); err != nil {
		return verifiedCacheStore{}, 0, err
	}
	if st.Mode&unix.S_IFMT != unix.S_IFDIR || st.Uid != ownerUID || st.Mode&0o777 != 0o700 {
		return verifiedCacheStore{}, 0, fmt.Errorf("cache root must be owner-owned mode 0700")
	}
	store := verifiedCacheStore{
		root: root, objects: filepath.Join(root, "objects"), builds: filepath.Join(root, "builds"),
		candidates: filepath.Join(root, "candidates"), quarantine: filepath.Join(root, "quarantine"), ownerUID: ownerUID,
	}
	for _, child := range []string{store.objects, store.builds, store.candidates, store.quarantine} {
		if err = verifyExactPrivateDirectory(child, ownerUID); err != nil {
			return verifiedCacheStore{}, 0, fmt.Errorf("%s: %w", child, err)
		}
	}
	return store, uint64(st.Dev), nil
}

func verifyExactPrivateDirectory(path string, ownerUID uint32) error {
	fd, err := openAbsoluteDirectoryNoSymlinks(path)
	if err != nil {
		return err
	}
	defer unix.Close(fd)
	var st unix.Stat_t
	if err = unix.Fstat(fd, &st); err != nil {
		return err
	}
	if st.Mode&unix.S_IFMT != unix.S_IFDIR || st.Uid != ownerUID || st.Mode&0o777 != 0o700 {
		return fmt.Errorf("directory must be owner-owned mode 0700")
	}
	return nil
}

// CacheBuiltArtifact verifies the signed local host-build record and hashes the
// already-sealed descriptor before atomically publishing immutable copies to
// both the verified cache and its same-host backup. Partial writes are
// unreferenced and never replace an existing digest path.
func (c *VerifiedReleaseCache) CacheBuiltArtifact(ctx context.Context, recordID string, artifact *os.File, size int64) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if !isLowerHexDigest(recordID) || artifact == nil || size <= 0 || size > verifiedCacheObjectLimit {
		return fmt.Errorf("%w: invalid artifact identity or size", ErrVerifiedCache)
	}
	if size > c.policy.CacheBudget || size > c.policy.BackupBudget {
		return ErrVerifiedCacheCapacity
	}
	var st unix.Stat_t
	if err := unix.Fstat(int(artifact.Fd()), &st); err != nil || st.Mode&unix.S_IFMT != unix.S_IFREG || st.Uid != c.policy.OwnerUID || st.Size != size || st.Mode&0o222 != 0 {
		return fmt.Errorf("%w: sealed descriptor is not a bounded private regular file", ErrVerifiedCache)
	}
	raw, err := c.builds.ReadHostBuildEnvelope(ctx, recordID)
	if err != nil {
		return err
	}
	if len(raw) == 0 || len(raw) > verifiedBuildRecordLimit {
		return fmt.Errorf("%w: host-build envelope exceeds its record bound", ErrVerifiedCache)
	}
	trust, err := c.trust.CurrentVerifiedCacheTrust(ctx)
	if err != nil {
		return err
	}
	envelope, err := VerifyEnvelope(raw, trust.Hosts)
	if err != nil || envelope.RecordType != "host_build_attestation" || envelope.RecordID != recordID {
		return fmt.Errorf("%w: signed host-build record failed identity verification", ErrVerifiedCache)
	}
	if err = ValidateHostBuildAttestation(envelope.Payload); err != nil {
		return fmt.Errorf("%w: invalid host-build payload: %v", ErrVerifiedCache, err)
	}
	var build hostBuildPayload
	if err = decodeStrict(envelope.Payload, &build); err != nil || build.HostID != c.policy.HostID || build.GOOS != c.policy.GOOS || build.GOARCH != c.policy.GOARCH {
		return fmt.Errorf("%w: host-build tuple is not local to this cache", ErrVerifiedCache)
	}
	digest, err := hashDescriptor(artifact, size)
	if err != nil || build.ArtifactDigest != "sha256:"+digest {
		return fmt.Errorf("%w: sealed bytes do not match the signed artifact digest", ErrVerifiedCache)
	}
	return c.withStoresLocked(func() error {
		for _, store := range []verifiedCacheStore{c.cache, c.backup} {
			if err := ensureStoreBudget(store, storeBudget(store, c.policy), digest, size, store.builds, recordID, int64(len(raw))); err != nil {
				return err
			}
		}
		for _, store := range []verifiedCacheStore{c.cache, c.backup} {
			if err := publishImmutableObject(store.objects, store.ownerUID, digest, artifact, size); err != nil {
				return err
			}
			if err := publishImmutableCacheRecord(store.builds, store.ownerUID, recordID, raw); err != nil {
				return err
			}
		}
		return nil
	})
}

func storeBudget(store verifiedCacheStore, policy VerifiedCachePolicy) int64 {
	if store.root == policy.CacheRoot {
		return policy.CacheBudget
	}
	return policy.BackupBudget
}

// CacheCandidateChain accepts a complete, signature-verified candidate and
// management chain only after every local runtime and management artifact is
// present in both local stores with its exact digest.
func (c *VerifiedReleaseCache) CacheCandidateChain(ctx context.Context, chain CandidateChain) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	encoded, err := encodeCandidateChain(chain)
	if err != nil {
		return "", err
	}
	trust, err := c.trust.CurrentVerifiedCacheTrust(ctx)
	if err != nil {
		return "", err
	}
	verified, err := verifyCandidateChain(chain, trust, c.policy.HostID, c.policy.GOOS, c.policy.GOARCH)
	if err != nil {
		return "", err
	}
	local := make(map[string]struct{}, len(verified.localArtifacts))
	for _, tuple := range verified.localArtifacts {
		local[strings.TrimPrefix(tuple.ArtifactDigest, "sha256:")] = struct{}{}
	}
	err = c.withStoresLocked(func() error {
		for _, store := range []verifiedCacheStore{c.cache, c.backup} {
			for digest := range local {
				if err := verifyObjectAt(store.objects, store.ownerUID, digest); err != nil {
					return fmt.Errorf("%w: local chain artifact %s is absent or invalid", ErrVerifiedCache, digest)
				}
			}
		}
		for _, store := range []verifiedCacheStore{c.cache, c.backup} {
			if err := ensureStoreBudget(store, storeBudget(store, c.policy), "", 0, store.candidates, verified.candidateID, int64(len(encoded))); err != nil {
				return err
			}
		}
		for _, store := range []verifiedCacheStore{c.cache, c.backup} {
			if err := publishImmutableCacheRecord(store.candidates, store.ownerUID, verified.candidateID, encoded); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	return verified.candidateID, nil
}

type verifiedCandidateChain struct {
	candidateID      string
	candidate        candidateManifestPayload
	managementID     string
	management       hostManagementManifestPayload
	runtimeBuilds    map[string]hostBuildPayload
	managementBuilds map[string]hostBuildPayload
	localArtifacts   []artifactTuple
}

func verifyCandidateChain(chain CandidateChain, trust VerifiedCacheTrust, hostID, goos, goarch string) (verifiedCandidateChain, error) {
	var result verifiedCandidateChain
	if err := validCandidateChainBounds(chain); err != nil {
		return result, err
	}
	candidateEnvelope, err := VerifyEnvelope(chain.CandidateEnvelope, trust.Authorities)
	if err != nil || candidateEnvelope.RecordType != "candidate_manifest" {
		return result, fmt.Errorf("%w: candidate signature or record type invalid", ErrVerifiedCache)
	}
	managementEnvelope, err := VerifyEnvelope(chain.ManagementEnvelope, trust.Authorities)
	if err != nil || managementEnvelope.RecordType != "host_management_manifest" {
		return result, fmt.Errorf("%w: management signature or record type invalid", ErrVerifiedCache)
	}
	if err = decodeStrict(candidateEnvelope.Payload, &result.candidate); err != nil || result.candidate.RequiredManagementManifestID != managementEnvelope.RecordID {
		return verifiedCandidateChain{}, fmt.Errorf("%w: candidate does not require the included management manifest", ErrVerifiedCache)
	}
	if err = decodeStrict(managementEnvelope.Payload, &result.management); err != nil {
		return verifiedCandidateChain{}, fmt.Errorf("%w: malformed management manifest", ErrVerifiedCache)
	}
	runtimePayloads, runtimeBuilds, err := verifyBuildEnvelopeSet(chain.RuntimeBuildEnvelopes, trust.Hosts)
	if err != nil {
		return verifiedCandidateChain{}, err
	}
	managementPayloads, managementBuilds, err := verifyBuildEnvelopeSet(chain.ManagementBuildEnvelopes, trust.Hosts)
	if err != nil {
		return verifiedCandidateChain{}, err
	}
	if err = ValidateCandidateManifest(candidateEnvelope.Payload, runtimePayloads); err != nil {
		return verifiedCandidateChain{}, fmt.Errorf("%w: candidate build matrix invalid: %v", ErrVerifiedCache, err)
	}
	if err = ValidateHostManagementManifest(managementEnvelope.Payload, managementPayloads); err != nil {
		return verifiedCandidateChain{}, fmt.Errorf("%w: management build matrix invalid: %v", ErrVerifiedCache, err)
	}
	if !isCanonicalHostID(hostID) || !isMatrixPlatform(hostID, goos, goarch) {
		return verifiedCandidateChain{}, fmt.Errorf("%w: local host/platform tuple is invalid", ErrVerifiedCache)
	}
	for _, tuple := range result.candidate.Artifacts {
		if tuple.HostID == hostID {
			if tuple.GOOS != goos || tuple.GOARCH != goarch {
				return verifiedCandidateChain{}, fmt.Errorf("%w: local candidate platform mismatch", ErrVerifiedCache)
			}
			result.localArtifacts = append(result.localArtifacts, tuple)
		}
	}
	for _, tuple := range result.management.Artifacts {
		if tuple.HostID == hostID {
			if tuple.GOOS != goos || tuple.GOARCH != goarch {
				return verifiedCandidateChain{}, fmt.Errorf("%w: local management platform mismatch", ErrVerifiedCache)
			}
			result.localArtifacts = append(result.localArtifacts, tuple)
		}
	}
	if len(result.localArtifacts) != len(runtimeComponents[hostID])+len(managementComponents[hostID]) {
		return verifiedCandidateChain{}, fmt.Errorf("%w: complete local candidate/management matrix is required", ErrVerifiedCache)
	}
	result.candidateID = candidateEnvelope.RecordID
	result.managementID = managementEnvelope.RecordID
	result.runtimeBuilds = runtimeBuilds
	result.managementBuilds = managementBuilds
	return result, nil
}

func verifyBuildEnvelopeSet(envelopes map[string][]byte, trusted map[string]TrustedKey) (map[string][]byte, map[string]hostBuildPayload, error) {
	if len(envelopes) == 0 || len(envelopes) > len(expectedComponentKeys("host_management")) {
		return nil, nil, fmt.Errorf("%w: empty or oversized build attestation set", ErrVerifiedCache)
	}
	payloads := make(map[string][]byte, len(envelopes))
	decoded := make(map[string]hostBuildPayload, len(envelopes))
	for key, raw := range envelopes {
		if !isLowerHexDigest(key) || len(raw) == 0 || len(raw) > verifiedBuildRecordLimit {
			return nil, nil, fmt.Errorf("%w: invalid build record key", ErrVerifiedCache)
		}
		envelope, err := VerifyEnvelope(raw, trusted)
		if err != nil || envelope.RecordType != "host_build_attestation" || envelope.RecordID != key {
			return nil, nil, fmt.Errorf("%w: build attestation signature or ID invalid", ErrVerifiedCache)
		}
		if err = ValidateHostBuildAttestation(envelope.Payload); err != nil {
			return nil, nil, fmt.Errorf("%w: invalid build attestation payload", ErrVerifiedCache)
		}
		var build hostBuildPayload
		if err = decodeStrict(envelope.Payload, &build); err != nil {
			return nil, nil, fmt.Errorf("%w: malformed build attestation", ErrVerifiedCache)
		}
		payloads[key], decoded[key] = raw, build
	}
	return payloads, decoded, nil
}

func validCandidateChainBounds(chain CandidateChain) error {
	if len(chain.CandidateEnvelope) == 0 || len(chain.CandidateEnvelope) > verifiedBuildRecordLimit ||
		len(chain.ManagementEnvelope) == 0 || len(chain.ManagementEnvelope) > verifiedBuildRecordLimit ||
		len(chain.RuntimeBuildEnvelopes) != len(expectedComponentKeys("lane_runtime")) ||
		len(chain.ManagementBuildEnvelopes) != len(expectedComponentKeys("host_management")) {
		return fmt.Errorf("%w: candidate chain is incomplete or exceeds its fixed record counts", ErrVerifiedCache)
	}
	total := len(chain.CandidateEnvelope) + len(chain.ManagementEnvelope)
	for _, records := range []map[string][]byte{chain.RuntimeBuildEnvelopes, chain.ManagementBuildEnvelopes} {
		for id, raw := range records {
			if !isLowerHexDigest(id) || len(raw) == 0 || len(raw) > verifiedBuildRecordLimit {
				return fmt.Errorf("%w: candidate chain contains an invalid or oversized build record", ErrVerifiedCache)
			}
			total += len(raw)
			if total > verifiedCacheRecordLimit {
				return fmt.Errorf("%w: candidate chain exceeds its aggregate record bound", ErrVerifiedCache)
			}
		}
	}
	return nil
}

func encodeCandidateChain(chain CandidateChain) ([]byte, error) {
	if err := validCandidateChainBounds(chain); err != nil {
		return nil, err
	}
	encoded, err := json.Marshal(chain)
	if err != nil || len(encoded) > verifiedCacheRecordLimit {
		return nil, fmt.Errorf("%w: candidate chain exceeds record limit", ErrVerifiedCache)
	}
	return CanonicalizeJSON(encoded)
}

// OpenCandidateArtifact verifies the current signatures and complete recorded
// chain on every open. It restores a missing/corrupt cache entry only from a
// matching same-host backup object and never rebuilds from a checkout.
func (c *VerifiedReleaseCache) OpenCandidateArtifact(ctx context.Context, candidateID, buildRecordID string) (*os.File, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !isLowerHexDigest(candidateID) || !isLowerHexDigest(buildRecordID) {
		return nil, fmt.Errorf("%w: invalid candidate or build record ID", ErrVerifiedCache)
	}
	var result *os.File
	err := c.withStoresLocked(func() error {
		var err error
		trust, trustErr := c.trust.CurrentVerifiedCacheTrust(ctx)
		if trustErr != nil {
			return trustErr
		}
		var chain CandidateChain
		var chainBytes []byte
		var source verifiedCacheStore
		var verified verifiedCandidateChain
		found := false
		chainRecords, recordErr := c.readCandidateChains(candidateID)
		if recordErr != nil {
			return recordErr
		}
		for _, record := range chainRecords {
			if decodeStrict(record.data, &chain) != nil {
				if record.store.root == c.cache.root {
					if archiveErr := archiveCacheEntry(c.cache, "candidate-"+candidateID); archiveErr != nil && !errors.Is(archiveErr, unix.ENOENT) {
						return archiveErr
					}
				}
				continue
			}
			candidate, verifyErr := verifyCandidateChain(chain, trust, c.policy.HostID, c.policy.GOOS, c.policy.GOARCH)
			if verifyErr != nil || candidate.candidateID != candidateID {
				if record.store.root == c.cache.root {
					if archiveErr := archiveCacheEntry(c.cache, "candidate-"+candidateID); archiveErr != nil && !errors.Is(archiveErr, unix.ENOENT) {
						return archiveErr
					}
				}
				continue
			}
			chainBytes, source, verified, found = record.data, record.store, candidate, true
			break
		}
		if !found {
			return fmt.Errorf("%w: no current valid candidate chain exists in either store", ErrVerifiedCacheUnavailable)
		}
		digest, ok := artifactDigestFor(verified, buildRecordID)
		if !ok {
			return fmt.Errorf("%w: build record is outside the verified local chain", ErrVerifiedCacheUnavailable)
		}
		cacheObjectErr := verifyObjectAt(c.cache.objects, c.cache.ownerUID, digest)
		if cacheObjectErr == nil {
			result, err = openObjectAt(c.cache.objects, c.cache.ownerUID, digest)
			if err == nil && source.root == c.backup.root {
				if err = ensureStoreBudget(c.cache, c.policy.CacheBudget, "", 0, c.cache.candidates, candidateID, int64(len(chainBytes))); err == nil {
					err = restoreCandidateRecord(c.cache, candidateID, chainBytes)
				}
				if err != nil {
					_ = result.Close()
					result = nil
				}
			}
			return err
		}
		backupFile, backupErr := openObjectAt(c.backup.objects, c.backup.ownerUID, digest)
		if backupErr != nil {
			return fmt.Errorf("%w: neither cache nor same-host backup contains the verified bytes", ErrVerifiedCacheUnavailable)
		}
		defer backupFile.Close()
		var backupStat unix.Stat_t
		if backupErr = unix.Fstat(int(backupFile.Fd()), &backupStat); backupErr != nil {
			return backupErr
		}
		if !errors.Is(cacheObjectErr, unix.ENOENT) {
			if archiveErr := archiveCacheEntry(c.cache, "object-"+digest); archiveErr != nil && !errors.Is(archiveErr, unix.ENOENT) {
				return archiveErr
			}
		}
		if err = ensureStoreBudget(c.cache, c.policy.CacheBudget, digest, backupStat.Size, c.cache.candidates, candidateID, int64(len(chainBytes))); err != nil {
			return err
		}
		if err = publishImmutableObject(c.cache.objects, c.cache.ownerUID, digest, backupFile, backupStat.Size); err != nil {
			return err
		}
		if source == c.backup {
			if err = restoreCandidateRecord(c.cache, candidateID, chainBytes); err != nil {
				return err
			}
		}
		result, err = openObjectAt(c.cache.objects, c.cache.ownerUID, digest)
		return err
	})
	if err != nil {
		if result != nil {
			_ = result.Close()
		}
		return nil, err
	}
	return result, nil
}

func artifactDigestFor(chain verifiedCandidateChain, recordID string) (string, bool) {
	for _, tuple := range chain.localArtifacts {
		if tuple.HostBuildAttestationID == recordID && strings.HasPrefix(tuple.ArtifactDigest, "sha256:") {
			return strings.TrimPrefix(tuple.ArtifactDigest, "sha256:"), true
		}
	}
	return "", false
}

type candidateChainRecord struct {
	data  []byte
	store verifiedCacheStore
}

func (c *VerifiedReleaseCache) readCandidateChains(candidateID string) ([]candidateChainRecord, error) {
	var records []candidateChainRecord
	for _, store := range []verifiedCacheStore{c.cache, c.backup} {
		data, readErr := readCacheRecord(store.candidates, store.ownerUID, candidateID)
		if readErr == nil {
			records = append(records, candidateChainRecord{data: data, store: store})
			continue
		}
		if !errors.Is(readErr, unix.ENOENT) {
			if store.root == c.cache.root {
				if archiveErr := archiveCacheEntry(store, "candidate-"+candidateID); archiveErr != nil && !errors.Is(archiveErr, unix.ENOENT) {
					return nil, archiveErr
				}
			}
		}
	}
	return records, nil
}

func restoreCandidateRecord(store verifiedCacheStore, candidateID string, data []byte) error {
	if err := publishImmutableCacheRecord(store.candidates, store.ownerUID, candidateID, data); err == nil {
		return nil
	} else if !errors.Is(err, ErrVerifiedCacheRecordCollision) {
		return err
	}
	if err := archiveCacheEntry(store, "candidate-"+candidateID); err != nil {
		return err
	}
	return publishImmutableCacheRecord(store.candidates, store.ownerUID, candidateID, data)
}

func ensureStoreBudget(store verifiedCacheStore, budget int64, digest string, size int64, recordDir string, recordID string, recordSize int64) error {
	used, err := cacheStoreBytes(store)
	if err != nil {
		return err
	}
	additional := int64(0)
	if digest != "" {
		if err = verifyObjectAt(store.objects, store.ownerUID, digest); err != nil {
			if !errors.Is(err, unix.ENOENT) && !errors.Is(err, ErrVerifiedCache) {
				return err
			}
			additional += size
		}
	}
	if recordDir != "" && recordID != "" {
		_, readErr := readCacheRecord(recordDir, store.ownerUID, recordID)
		if readErr != nil {
			if !errors.Is(readErr, unix.ENOENT) {
				return readErr
			}
			additional += recordSize
		}
	}
	if size < 0 || recordSize < 0 || used > budget || additional > budget-used {
		return ErrVerifiedCacheCapacity
	}
	return nil
}

func cacheStoreBytes(store verifiedCacheStore) (int64, error) {
	var total int64
	for _, root := range []string{store.objects, store.builds, store.candidates, store.quarantine} {
		err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			var stat unix.Stat_t
			if err := unix.Lstat(path, &stat); err != nil {
				return err
			}
			if stat.Mode&unix.S_IFMT == unix.S_IFLNK || (stat.Mode&unix.S_IFMT != unix.S_IFDIR && stat.Mode&unix.S_IFMT != unix.S_IFREG) {
				return fmt.Errorf("%w: cache contains a link or special file", ErrVerifiedCache)
			}
			if stat.Mode&unix.S_IFMT == unix.S_IFDIR {
				if path != root || stat.Uid != store.ownerUID || stat.Mode&0o777 != 0o700 {
					return fmt.Errorf("%w: unexpected nested or unsafe directory", ErrVerifiedCache)
				}
				return nil
			}
			if stat.Uid != store.ownerUID || stat.Mode&0o022 != 0 || stat.Size < 0 || total > int64(^uint64(0)>>1)-stat.Size {
				return fmt.Errorf("%w: cache file ownership, mode, or size invalid", ErrVerifiedCache)
			}
			total += stat.Size
			return nil
		})
		if err != nil {
			return 0, err
		}
	}
	return total, nil
}

func readCacheRecord(directory string, ownerUID uint32, recordID string) ([]byte, error) {
	if !isLowerHexDigest(recordID) {
		return nil, ErrVerifiedCache
	}
	dirFD, err := openAbsoluteDirectoryNoSymlinks(directory)
	if err != nil {
		return nil, err
	}
	defer unix.Close(dirFD)
	return readAtNoFollow(dirFD, recordID+".json", ownerUID, verifiedCacheRecordLimit)
}

func publishImmutableObject(directory string, ownerUID uint32, digest string, source *os.File, size int64) error {
	if !isLowerHexDigest(digest) || source == nil || size <= 0 || size > verifiedCacheObjectLimit {
		return ErrVerifiedCache
	}
	dirFD, err := openAbsoluteDirectoryNoSymlinks(directory)
	if err != nil {
		return err
	}
	defer unix.Close(dirFD)
	target := digest
	if existing, openErr := openObjectAt(directory, ownerUID, digest); openErr == nil {
		defer existing.Close()
		existingDigest, hashErr := hashDescriptor(existing, size)
		var st unix.Stat_t
		statErr := unix.Fstat(int(existing.Fd()), &st)
		if hashErr == nil && statErr == nil && st.Size == size && existingDigest == digest {
			return nil
		}
		return fmt.Errorf("%w: immutable artifact digest collision", ErrVerifiedCache)
	} else if !errors.Is(openErr, unix.ENOENT) {
		return openErr
	}
	random := make([]byte, 16)
	if _, err = rand.Read(random); err != nil {
		return err
	}
	temporary := ".object-" + hex.EncodeToString(random)
	fd, err := unix.Openat(dirFD, temporary, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0o600)
	if err != nil {
		return err
	}
	output := os.NewFile(uintptr(fd), temporary)
	hash := sha256.New()
	written, copyErr := io.CopyN(io.MultiWriter(output, hash), io.NewSectionReader(source, 0, size), size)
	if copyErr == nil && written != size {
		copyErr = io.ErrUnexpectedEOF
	}
	if copyErr == nil && hex.EncodeToString(hash.Sum(nil)) != digest {
		copyErr = fmt.Errorf("%w: source descriptor digest changed", ErrVerifiedCache)
	}
	if copyErr == nil {
		copyErr = output.Chmod(0o400)
	}
	if copyErr == nil {
		copyErr = output.Sync()
	}
	closeErr := output.Close()
	if copyErr == nil {
		copyErr = closeErr
	}
	if copyErr != nil {
		_ = unix.Unlinkat(dirFD, temporary, 0)
		return copyErr
	}
	if err = unix.Linkat(dirFD, temporary, dirFD, target, 0); err != nil {
		_ = unix.Unlinkat(dirFD, temporary, 0)
		if errors.Is(err, unix.EEXIST) {
			return publishImmutableObject(directory, ownerUID, digest, source, size)
		}
		return err
	}
	if err = unix.Unlinkat(dirFD, temporary, 0); err != nil {
		return err
	}
	return unix.Fsync(dirFD)
}

func publishImmutableCacheRecord(directory string, ownerUID uint32, recordID string, data []byte) error {
	if !isLowerHexDigest(recordID) || len(data) == 0 || len(data) > verifiedCacheRecordLimit {
		return ErrVerifiedCache
	}
	dirFD, err := openAbsoluteDirectoryNoSymlinks(directory)
	if err != nil {
		return err
	}
	defer unix.Close(dirFD)
	target := recordID + ".json"
	if existing, readErr := readAtNoFollow(dirFD, target, ownerUID, verifiedCacheRecordLimit); readErr == nil {
		if bytes.Equal(existing, data) {
			return nil
		}
		return ErrVerifiedCacheRecordCollision
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
	_, writeErr := file.Write(data)
	if writeErr == nil {
		writeErr = file.Chmod(0o400)
	}
	if writeErr == nil {
		writeErr = file.Sync()
	}
	closeErr := file.Close()
	if writeErr == nil {
		writeErr = closeErr
	}
	if writeErr != nil {
		_ = unix.Unlinkat(dirFD, temporary, 0)
		return writeErr
	}
	if err = unix.Linkat(dirFD, temporary, dirFD, target, 0); err != nil {
		_ = unix.Unlinkat(dirFD, temporary, 0)
		if errors.Is(err, unix.EEXIST) {
			return publishImmutableCacheRecord(directory, ownerUID, recordID, data)
		}
		return err
	}
	if err = unix.Unlinkat(dirFD, temporary, 0); err != nil {
		return err
	}
	return unix.Fsync(dirFD)
}

func verifyObjectAt(directory string, ownerUID uint32, digest string) error {
	file, err := openObjectAt(directory, ownerUID, digest)
	if err != nil {
		return err
	}
	defer file.Close()
	var st unix.Stat_t
	if err = unix.Fstat(int(file.Fd()), &st); err != nil {
		return err
	}
	actual, err := hashDescriptor(file, st.Size)
	if err != nil || actual != digest {
		return fmt.Errorf("%w: content-addressed object digest mismatch", ErrVerifiedCache)
	}
	return nil
}

func openObjectAt(directory string, ownerUID uint32, digest string) (*os.File, error) {
	if !isLowerHexDigest(digest) {
		return nil, ErrVerifiedCache
	}
	return openNamedObject(directory, ownerUID, digest)
}

func openNamedObject(directory string, ownerUID uint32, name string) (*os.File, error) {
	dirFD, err := openAbsoluteDirectoryNoSymlinks(directory)
	if err != nil {
		return nil, err
	}
	defer unix.Close(dirFD)
	fd, err := unix.Openat(dirFD, name, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	var st unix.Stat_t
	if err = unix.Fstat(fd, &st); err != nil {
		unix.Close(fd)
		return nil, err
	}
	if st.Mode&unix.S_IFMT != unix.S_IFREG || st.Uid != ownerUID || st.Mode&0o222 != 0 || st.Size <= 0 || st.Size > verifiedCacheObjectLimit {
		unix.Close(fd)
		return nil, fmt.Errorf("%w: object is not a bounded immutable private regular file", ErrVerifiedCache)
	}
	return os.NewFile(uintptr(fd), name), nil
}

func hashDescriptor(file *os.File, size int64) (string, error) {
	if size <= 0 || size > verifiedCacheObjectLimit {
		return "", ErrVerifiedCache
	}
	hash := sha256.New()
	written, err := io.CopyN(hash, io.NewSectionReader(file, 0, size), size)
	if err != nil || written != size {
		return "", fmt.Errorf("%w: unable to hash complete descriptor", ErrVerifiedCache)
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func archiveCacheEntry(store verifiedCacheStore, named string) error {
	parts := strings.SplitN(named, "-", 2)
	if len(parts) != 2 || !isLowerHexDigest(parts[1]) {
		return ErrVerifiedCache
	}
	sourceDir := store.objects
	if parts[0] == "candidate" {
		sourceDir = store.candidates
	}
	fromFD, err := openAbsoluteDirectoryNoSymlinks(sourceDir)
	if err != nil {
		return err
	}
	defer unix.Close(fromFD)
	toFD, err := openAbsoluteDirectoryNoSymlinks(store.quarantine)
	if err != nil {
		return err
	}
	defer unix.Close(toFD)
	name := parts[1]
	if parts[0] == "candidate" {
		name += ".json"
	}
	var st unix.Stat_t
	if err = unix.Fstatat(fromFD, name, &st, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return err
	}
	random := make([]byte, 12)
	if _, err = rand.Read(random); err != nil {
		return err
	}
	quarantined := parts[0] + "-" + parts[1] + "-" + hex.EncodeToString(random)
	if err = unix.Renameat(fromFD, name, toFD, quarantined); err != nil {
		return err
	}
	if err = unix.Fsync(fromFD); err != nil {
		return err
	}
	return unix.Fsync(toFD)
}

func (c *VerifiedReleaseCache) withStoresLocked(run func() error) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	stores := []verifiedCacheStore{c.cache, c.backup}
	sort.Slice(stores, func(i, j int) bool { return stores[i].root < stores[j].root })
	locks := make([]*os.File, 0, len(stores))
	defer func() {
		for i := len(locks) - 1; i >= 0; i-- {
			_ = unix.Flock(int(locks[i].Fd()), unix.LOCK_UN)
			_ = locks[i].Close()
		}
	}()
	for _, store := range stores {
		fd, err := openAbsoluteDirectoryNoSymlinks(store.root)
		if err != nil {
			return err
		}
		lockFD, openErr := unix.Openat(fd, ".writer.lock", unix.O_RDWR|unix.O_CREAT|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0o600)
		unix.Close(fd)
		if openErr != nil {
			return openErr
		}
		var st unix.Stat_t
		if openErr = unix.Fstat(lockFD, &st); openErr != nil || st.Mode&unix.S_IFMT != unix.S_IFREG || st.Uid != store.ownerUID || st.Mode&0o777 != 0o600 {
			unix.Close(lockFD)
			return fmt.Errorf("%w: cache writer lock has unsafe ownership or mode", ErrVerifiedCache)
		}
		lock := os.NewFile(uintptr(lockFD), store.root+"/.writer.lock")
		if openErr = unix.Flock(lockFD, unix.LOCK_EX); openErr != nil {
			lock.Close()
			return openErr
		}
		locks = append(locks, lock)
	}
	return run()
}
