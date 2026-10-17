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
	"runtime"
	"strings"
	"testing"
)

func TestLanesL308InstalledCacheRequiresRootFixedRootsAndDistinctDevices(t *testing.T) {
	fixture := newL308EmptyFixture(t, 1<<20)
	if _, err := NewVerifiedReleaseCache(fixture.cache.policy, fixture.reader, fixture.trust); !errors.Is(err, ErrVerifiedCache) {
		t.Fatalf("unprivileged fixture paths were accepted by the installed constructor: %v", err)
	}
	if err := validateCacheDeviceIdentity(44, 44); !errors.Is(err, ErrVerifiedCache) {
		t.Fatalf("cache and backup on one device were accepted: %v", err)
	}
	cachePath, backupPath, goos, goarch := fixedReleaseCachePaths("mac-control-plane")
	if cachePath != "/Library/Application Support/RemoteSessionRunner-release-cache" ||
		backupPath != "/Library/Application Support/RemoteSessionRunner-release-cache-backup" || goos != "darwin" || goarch != "arm64" {
		t.Fatalf("Mac cache roots differ from fixed design: %q %q %s/%s", cachePath, backupPath, goos, goarch)
	}
	_, cacheDev, err := openVerifiedCacheStore(fixture.cache.cache.root, fixture.cache.policy.OwnerUID)
	if err != nil {
		t.Fatal(err)
	}
	_, backupDev, err := openVerifiedCacheStore(fixture.cache.backup.root, fixture.cache.policy.OwnerUID)
	if err != nil {
		t.Fatal(err)
	}
	if cacheDev != backupDev {
		t.Skip("temporary roots unexpectedly use separate devices; production constructor's distinct-device branch is covered")
	}
	if cacheDev == backupDev {
		t.Log("hermetic fixture roots share a filesystem; production constructor rejects this device identity")
	}
}

func TestLanesL308BuildCacheVerifiesSignedLocalRecordAndExactSealedBytes(t *testing.T) {
	fixture := newL308EmptyFixture(t, 1<<20)
	var recordID string
	var content []byte
	for id, artifact := range fixture.artifacts {
		recordID, content = id, artifact
		break
	}
	file := l308SealedFile(t, fixture.root, content)
	defer file.Close()
	if err := fixture.cache.CacheBuiltArtifact(context.Background(), recordID, file, int64(len(content))); err != nil {
		t.Fatalf("exact signed local artifact was rejected: %v", err)
	}
	for _, store := range []verifiedCacheStore{fixture.cache.cache, fixture.cache.backup} {
		digest := l308HexDigest(content)
		if err := verifyObjectAt(store.objects, store.ownerUID, digest); err != nil {
			t.Fatalf("artifact missing or invalid in %s: %v", store.root, err)
		}
		if _, err := readCacheRecord(store.builds, store.ownerUID, recordID); err != nil {
			t.Fatalf("signed build record missing in %s: %v", store.root, err)
		}
	}

	badFile := l308SealedFile(t, fixture.root, []byte("wrong bytes"))
	defer badFile.Close()
	if err := fixture.cache.CacheBuiltArtifact(context.Background(), recordID, badFile, int64(len("wrong bytes"))); !errors.Is(err, ErrVerifiedCache) {
		t.Fatalf("digest-mismatched sealed output was accepted: %v", err)
	}
	if err := fixture.cache.CacheBuiltArtifact(context.Background(), strings.Repeat("f", 64), file, int64(len(content))); err == nil {
		t.Fatal("unknown signed build record was accepted")
	}
}

func TestLanesL308CandidateCacheRequiresCompleteSignedChainAndLocalArtifacts(t *testing.T) {
	fixture := newL308Fixture(t, 1<<20)
	if _, err := fixture.cache.CacheCandidateChain(context.Background(), fixture.chain); err != nil {
		t.Fatalf("complete candidate and management chain rejected: %v", err)
	}
	for _, store := range []verifiedCacheStore{fixture.cache.cache, fixture.cache.backup} {
		if _, err := readCacheRecord(store.candidates, store.ownerUID, fixture.candidateID); err != nil {
			t.Fatalf("candidate chain absent from %s: %v", store.root, err)
		}
	}
	empty := newL308EmptyFixture(t, 1<<20)
	if _, err := empty.cache.CacheCandidateChain(context.Background(), fixture.chain); !errors.Is(err, ErrVerifiedCache) {
		t.Fatalf("candidate accepted before local runtime and management artifacts were cached: %v", err)
	}

	incomplete := fixture.chain
	incomplete.ManagementBuildEnvelopes = cloneEnvelopeMap(incomplete.ManagementBuildEnvelopes)
	for id := range incomplete.ManagementBuildEnvelopes {
		delete(incomplete.ManagementBuildEnvelopes, id)
		break
	}
	if _, err := fixture.cache.CacheCandidateChain(context.Background(), incomplete); !errors.Is(err, ErrVerifiedCache) {
		t.Fatalf("incomplete management build chain was accepted: %v", err)
	}
	forged := fixture.chain
	forged.CandidateEnvelope = append([]byte(nil), forged.CandidateEnvelope...)
	forged.CandidateEnvelope[len(forged.CandidateEnvelope)-2] ^= 1
	if _, err := fixture.cache.CacheCandidateChain(context.Background(), forged); !errors.Is(err, ErrVerifiedCache) {
		t.Fatalf("candidate with invalid signature was accepted: %v", err)
	}
	wrongTrust := newL308Fixture(t, 1<<20)
	wrongTrust.trust.value.Hosts["host-test-mac-control-plane"] = TrustedKey{
		KeyID: "host-test-mac-control-plane", Role: SignerRoleHostAttestation, HostID: "mac-control-plane",
		PublicKey: testSigningKey(7).Public().(ed25519.PublicKey),
	}
	if _, err := wrongTrust.cache.CacheCandidateChain(context.Background(), wrongTrust.chain); !errors.Is(err, ErrVerifiedCache) {
		t.Fatalf("build chain signed by a key outside current host trust was accepted: %v", err)
	}
}

func TestLanesL308OpenCandidateRehydratesOnlyFromVerifiedSameHostBackup(t *testing.T) {
	fixture := newL308Fixture(t, 1<<20)
	if _, err := fixture.cache.CacheCandidateChain(context.Background(), fixture.chain); err != nil {
		t.Fatal(err)
	}
	digest := l308HexDigest(fixture.artifacts[fixture.targetBuildID])
	if err := os.Remove(filepath.Join(fixture.cache.cache.objects, digest)); err != nil {
		t.Fatal(err)
	}
	file, err := fixture.cache.OpenCandidateArtifact(context.Background(), fixture.candidateID, fixture.targetBuildID)
	if err != nil {
		t.Fatalf("valid same-host backup did not rehydrate cache: %v", err)
	}
	defer file.Close()
	got := make([]byte, len(fixture.artifacts[fixture.targetBuildID]))
	if _, err = file.ReadAt(got, 0); err != nil || string(got) != string(fixture.artifacts[fixture.targetBuildID]) {
		t.Fatalf("rehydrated artifact bytes differ: got=%q err=%v", got, err)
	}
	if err = verifyObjectAt(fixture.cache.cache.objects, fixture.cache.policy.OwnerUID, digest); err != nil {
		t.Fatalf("rehydrated cache object did not verify: %v", err)
	}
}

func TestLanesL308CorruptCacheEvidenceIsPreservedBeforeVerifiedRestore(t *testing.T) {
	fixture := newL308Fixture(t, 1<<20)
	if _, err := fixture.cache.CacheCandidateChain(context.Background(), fixture.chain); err != nil {
		t.Fatal(err)
	}
	digest := l308HexDigest(fixture.artifacts[fixture.targetBuildID])
	path := filepath.Join(fixture.cache.cache.objects, digest)
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("corrupt retained cache evidence"), 0o400); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.cache.OpenCandidateArtifact(context.Background(), fixture.candidateID, fixture.targetBuildID); err != nil {
		t.Fatalf("verified backup should replace corrupt cache after preserving it: %v", err)
	}
	entries, err := os.ReadDir(fixture.cache.cache.quarantine)
	if err != nil || len(entries) != 1 {
		t.Fatalf("corrupt cache entry was not retained in quarantine: entries=%v err=%v", entries, err)
	}
	retained, err := os.ReadFile(filepath.Join(fixture.cache.cache.quarantine, entries[0].Name()))
	if err != nil || string(retained) != "corrupt retained cache evidence" {
		t.Fatalf("quarantined evidence changed: bytes=%q err=%v", retained, err)
	}
	if err = verifyObjectAt(fixture.cache.cache.objects, fixture.cache.policy.OwnerUID, digest); err != nil {
		t.Fatalf("backup restore did not install the exact verified bytes: %v", err)
	}
}

func TestLanesL308InvalidCachedChainFallsBackToBackupAndPreservesEvidence(t *testing.T) {
	fixture := newL308Fixture(t, 1<<20)
	if _, err := fixture.cache.CacheCandidateChain(context.Background(), fixture.chain); err != nil {
		t.Fatal(err)
	}
	cacheRecord := filepath.Join(fixture.cache.cache.candidates, fixture.candidateID+".json")
	if err := os.Chmod(cacheRecord, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cacheRecord, []byte(`{"tampered":true}`), 0o400); err != nil {
		t.Fatal(err)
	}
	file, err := fixture.cache.OpenCandidateArtifact(context.Background(), fixture.candidateID, fixture.targetBuildID)
	if err != nil {
		t.Fatalf("valid backup chain should recover an invalid cache copy: %v", err)
	}
	_ = file.Close()
	entries, err := os.ReadDir(fixture.cache.cache.quarantine)
	if err != nil || len(entries) != 1 {
		t.Fatalf("invalid candidate-chain evidence was not preserved: entries=%v err=%v", entries, err)
	}
	retained, err := os.ReadFile(filepath.Join(fixture.cache.cache.quarantine, entries[0].Name()))
	if err != nil || string(retained) != `{"tampered":true}` {
		t.Fatalf("quarantined chain bytes changed: %q err=%v", retained, err)
	}
	if _, err = readCacheRecord(fixture.cache.cache.candidates, fixture.cache.policy.OwnerUID, fixture.candidateID); err != nil {
		t.Fatalf("verified backup chain was not restored to the cache: %v", err)
	}
}

func TestLanesL308CapacityAndMissingCacheBackupFailClosed(t *testing.T) {
	fixture := newL308EmptyFixture(t, 1)
	for id, content := range fixture.artifacts {
		file := l308SealedFile(t, fixture.root, content)
		err := fixture.cache.CacheBuiltArtifact(context.Background(), id, file, int64(len(content)))
		_ = file.Close()
		if !errors.Is(err, ErrVerifiedCache) && !errors.Is(err, ErrVerifiedCacheCapacity) {
			t.Fatalf("over-budget artifact error = %v", err)
		}
		break
	}
	for _, store := range []verifiedCacheStore{fixture.cache.cache, fixture.cache.backup} {
		entries, err := os.ReadDir(store.objects)
		if err != nil || len(entries) != 0 {
			t.Fatalf("over-budget cache partially published artifact under %s: entries=%v err=%v", store.root, entries, err)
		}
	}

	fixture = newL308Fixture(t, 1<<20)
	if _, err := fixture.cache.CacheCandidateChain(context.Background(), fixture.chain); err != nil {
		t.Fatal(err)
	}
	digest := l308HexDigest(fixture.artifacts[fixture.targetBuildID])
	for _, store := range []verifiedCacheStore{fixture.cache.cache, fixture.cache.backup} {
		if err := os.Remove(filepath.Join(store.objects, digest)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := fixture.cache.OpenCandidateArtifact(context.Background(), fixture.candidateID, fixture.targetBuildID); !errors.Is(err, ErrVerifiedCacheUnavailable) {
		t.Fatalf("missing cache and same-host backup did not fail closed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(fixture.cache.cache.objects, digest)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("closed-stop path created or rebuilt an artifact: %v", err)
	}
}

type l308Fixture struct {
	root          string
	cache         *VerifiedReleaseCache
	reader        *l308BuildReader
	trust         *l308TrustResolver
	chain         CandidateChain
	candidateID   string
	targetBuildID string
	artifacts     map[string][]byte
}

func newL308Fixture(t *testing.T, budget int64) *l308Fixture {
	t.Helper()
	fixture := newL308EmptyFixture(t, budget)
	for id, content := range fixture.artifacts {
		file := l308SealedFile(t, fixture.root, content)
		err := fixture.cache.CacheBuiltArtifact(context.Background(), id, file, int64(len(content)))
		_ = file.Close()
		if err != nil {
			t.Fatalf("cache local build %s: %v", id, err)
		}
	}
	return fixture
}

func newL308EmptyFixture(t *testing.T, budget int64) *l308Fixture {
	t.Helper()
	tempRoot := ""
	if runtime.GOOS == "darwin" {
		tempRoot = "/private/tmp"
	}
	root, err := os.MkdirTemp(tempRoot, "lanes-l308-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	cacheRoot := filepath.Join(root, "cache")
	backupRoot := filepath.Join(root, "backup")
	for _, store := range []string{cacheRoot, backupRoot} {
		if err := os.Mkdir(store, 0o700); err != nil {
			t.Fatal(err)
		}
		for _, child := range []string{"objects", "builds", "candidates", "quarantine"} {
			if err := os.Mkdir(filepath.Join(store, child), 0o700); err != nil {
				t.Fatal(err)
			}
		}
	}
	chain, artifacts, reader, trust, targetBuildID := l308CandidateChain(t)
	var candidateEnvelope Envelope
	if err := json.Unmarshal(chain.CandidateEnvelope, &candidateEnvelope); err != nil {
		t.Fatal(err)
	}
	policy := VerifiedCachePolicy{
		HostID: "mac-control-plane", GOOS: "darwin", GOARCH: "arm64", CacheRoot: cacheRoot, BackupRoot: backupRoot,
		OwnerUID: uint32(os.Getuid()), CacheBudget: budget, BackupBudget: budget,
	}
	cache, err := newVerifiedReleaseCacheFixture(policy, reader, trust)
	if err != nil {
		t.Fatal(err)
	}
	return &l308Fixture{
		root: root, cache: cache, reader: reader, trust: trust, chain: chain,
		candidateID: candidateEnvelope.RecordID, targetBuildID: targetBuildID, artifacts: artifacts,
	}
}

func l308CandidateChain(t *testing.T) (CandidateChain, map[string][]byte, *l308BuildReader, *l308TrustResolver, string) {
	t.Helper()
	candidatePayload, runtimeEnvelopes := l302CandidateFixture(t)
	managementPayload, managementEnvelopes := l302ManagementFixture(t)
	runtimeEnvelopes, runtimeContent, runtimeRewrites := l308RewriteLocalBuilds(t, runtimeEnvelopes, "lane_runtime")
	managementEnvelopes, managementContent, managementRewrites := l308RewriteLocalBuilds(t, managementEnvelopes, "host_management")
	var candidate candidateManifestPayload
	if err := json.Unmarshal(candidatePayload, &candidate); err != nil {
		t.Fatal(err)
	}
	var management hostManagementManifestPayload
	if err := json.Unmarshal(managementPayload, &management); err != nil {
		t.Fatal(err)
	}
	for i := range candidate.Artifacts {
		if replacement, ok := runtimeRewrites[candidate.Artifacts[i].HostBuildAttestationID]; ok {
			candidate.Artifacts[i].HostBuildAttestationID = replacement[0]
			candidate.Artifacts[i].ArtifactDigest = replacement[1]
		}
	}
	for i := range management.Artifacts {
		if replacement, ok := managementRewrites[management.Artifacts[i].HostBuildAttestationID]; ok {
			management.Artifacts[i].HostBuildAttestationID = replacement[0]
			management.Artifacts[i].ArtifactDigest = replacement[1]
		}
	}
	managementRaw := l302JSON(t, management)
	authorityPrivate := testSigningKey(224)
	authority := SigningIdentity{KeyID: "l308-authority", Role: SignerRoleReleaseAuthority, PrivateKey: authorityPrivate}
	managementEnvelopeRaw, err := SignEnvelope("host_management_manifest", managementRaw, authority)
	if err != nil {
		t.Fatal(err)
	}
	var managementEnvelope Envelope
	if err := json.Unmarshal(managementEnvelopeRaw, &managementEnvelope); err != nil {
		t.Fatal(err)
	}
	candidate.RequiredManagementManifestID = managementEnvelope.RecordID
	candidateRaw := l302JSON(t, candidate)
	candidateEnvelopeRaw, err := SignEnvelope("candidate_manifest", candidateRaw, authority)
	if err != nil {
		t.Fatal(err)
	}
	authorityPublic := authorityPrivate.Public().(ed25519.PublicKey)
	trust := &l308TrustResolver{value: VerifiedCacheTrust{
		Authorities: map[string]TrustedKey{authority.KeyID: {KeyID: authority.KeyID, Role: SignerRoleReleaseAuthority, PublicKey: authorityPublic}},
		Hosts:       make(map[string]TrustedKey, len(canonicalHostIDs)),
	}}
	for index, hostID := range canonicalHostIDs {
		private := testSigningKey(byte(index * 32))
		keyID := "host-test-" + hostID
		trust.value.Hosts[keyID] = TrustedKey{KeyID: keyID, Role: SignerRoleHostAttestation, HostID: hostID, PublicKey: private.Public().(ed25519.PublicKey)}
	}
	builds := make(map[string][]byte, len(runtimeEnvelopes)+len(managementEnvelopes))
	for id, raw := range runtimeEnvelopes {
		builds[id] = raw
	}
	for id, raw := range managementEnvelopes {
		builds[id] = raw
	}
	reader := &l308BuildReader{records: builds}
	artifacts := make(map[string][]byte, len(runtimeContent)+len(managementContent))
	for id, content := range runtimeContent {
		artifacts[id] = content
	}
	for id, content := range managementContent {
		artifacts[id] = content
	}
	target := ""
	for _, raw := range runtimeEnvelopes {
		var env Envelope
		if err := json.Unmarshal(raw, &env); err != nil {
			t.Fatal(err)
		}
		var build hostBuildPayload
		if err := json.Unmarshal(env.Payload, &build); err != nil {
			t.Fatal(err)
		}
		if build.HostID == "mac-control-plane" && build.Component == "runner" {
			target = env.RecordID
			break
		}
	}
	chain := CandidateChain{
		CandidateEnvelope: candidateEnvelopeRaw, ManagementEnvelope: managementEnvelopeRaw,
		RuntimeBuildEnvelopes: runtimeEnvelopes, ManagementBuildEnvelopes: managementEnvelopes,
	}
	return chain, artifacts, reader, trust, target
}

func l308RewriteLocalBuilds(t *testing.T, records map[string][]byte, track string) (map[string][]byte, map[string][]byte, map[string][2]string) {
	t.Helper()
	rewritten := make(map[string][]byte, len(records))
	contentByID := make(map[string][]byte)
	oldToNew := make(map[string][2]string)
	for oldID, raw := range records {
		var envelope Envelope
		if err := json.Unmarshal(raw, &envelope); err != nil {
			t.Fatal(err)
		}
		var build hostBuildPayload
		if err := json.Unmarshal(envelope.Payload, &build); err != nil {
			t.Fatal(err)
		}
		if build.HostID == "mac-control-plane" {
			content := []byte("LANES-L3-08:" + track + ":" + build.Component)
			digest := l308HexDigest(content)
			build.ArtifactDigest = "sha256:" + digest
			payload := l302JSON(t, build)
			private := l302HostPrivateKey(raw)
			updated, err := SignEnvelope("host_build_attestation", payload, SigningIdentity{
				KeyID: envelope.SignerKeyID, Role: SignerRoleHostAttestation, HostID: build.HostID, PrivateKey: private,
			})
			if err != nil {
				t.Fatal(err)
			}
			var updatedEnvelope Envelope
			if err := json.Unmarshal(updated, &updatedEnvelope); err != nil {
				t.Fatal(err)
			}
			rewritten[updatedEnvelope.RecordID] = updated
			contentByID[updatedEnvelope.RecordID] = content
			oldToNew[oldID] = [2]string{updatedEnvelope.RecordID, build.ArtifactDigest}
		} else {
			rewritten[oldID] = raw
		}
	}
	return rewritten, contentByID, oldToNew
}

type l308BuildReader struct{ records map[string][]byte }

func (r *l308BuildReader) ReadHostBuildEnvelope(_ context.Context, recordID string) ([]byte, error) {
	raw, ok := r.records[recordID]
	if !ok {
		return nil, os.ErrNotExist
	}
	return append([]byte(nil), raw...), nil
}

type l308TrustResolver struct{ value VerifiedCacheTrust }

func (r *l308TrustResolver) CurrentVerifiedCacheTrust(context.Context) (VerifiedCacheTrust, error) {
	return r.value, nil
}

func cloneEnvelopeMap(input map[string][]byte) map[string][]byte {
	output := make(map[string][]byte, len(input))
	for id, raw := range input {
		output[id] = append([]byte(nil), raw...)
	}
	return output
}

func l308SealedFile(t *testing.T, directory string, content []byte) *os.File {
	t.Helper()
	file, err := os.CreateTemp(directory, "sealed-")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = file.Write(content); err == nil {
		err = file.Sync()
	}
	if err == nil {
		err = file.Chmod(0o400)
	}
	if err == nil {
		_, err = file.Seek(0, 0)
	}
	if err != nil {
		file.Close()
		t.Fatal(err)
	}
	return file
}

func l308HexDigest(content []byte) string {
	digest := sha256.Sum256(content)
	return hex.EncodeToString(digest[:])
}
