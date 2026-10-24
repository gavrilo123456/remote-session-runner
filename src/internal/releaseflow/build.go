package releaseflow

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"time"
)

type BuildRequest struct {
	Branch                string
	Commit                string
	ReleaseID             string
	ResumeID              string
	PlanOnly              bool
	QualificationReceipts []SourceTestReceipt
}

type BuildArtifact struct {
	Bundle   Bundle
	Settings HostBuildSettings
	Path     string
}

type BuildBackend interface {
	BuildNative(context.Context, string, Source, string, string) (BuildArtifact, error)
	VerifyArtifact(context.Context, string, string, string) error
	PublishNative(context.Context, string, Release, string) error
	InspectPublished(context.Context, string, Release) error
}

type BuildResult struct {
	Record  BuildRecord `json:"build"`
	Release Release     `json:"release"`
	Outcome string      `json:"outcome"`
	Next    string      `json:"next_step,omitempty"`
}

type Provenance struct {
	FormatVersion            int                 `json:"format_version"`
	BuildID                  string              `json:"build_id"`
	ReleaseID                string              `json:"release_id"`
	ReleaseSHA256            string              `json:"release_sha256"`
	Source                   Source              `json:"source"`
	Toolchain                string              `json:"toolchain"`
	ControllerRevision       string              `json:"controller_revision"`
	Hosts                    []Bundle            `json:"host_bundles"`
	BuildSettings            []HostBuildSettings `json:"host_build_settings"`
	QualificationReceiptRefs []string            `json:"qualification_receipt_refs"`
}

func Build(ctx context.Context, s *Store, resolver GitResolver, backend BuildBackend, req BuildRequest, controllerRevision string, now func() time.Time) (BuildResult, error) {
	var result BuildResult
	if s == nil || backend == nil || now == nil {
		return result, ErrBackendUnavailable
	}
	var record BuildRecord
	var err error
	if req.ResumeID != "" {
		record, err = s.LoadBuild(req.ResumeID)
		if err != nil {
			return result, err
		}
		if req.Branch != "" && req.Branch != record.Source.Branch {
			return result, ErrReleaseMismatch
		}
		if req.Commit != "" && req.Commit != record.Source.Commit {
			return result, ErrReleaseMismatch
		}
		if req.ReleaseID != "" && req.ReleaseID != record.ReleaseID {
			return result, ErrReleaseMismatch
		}
		if len(req.QualificationReceipts) > 0 {
			refs, receiptErr := persistQualificationReceipts(s, req.QualificationReceipts, record.Source.Commit)
			if receiptErr != nil || !reflect.DeepEqual(refs, record.QualificationReceiptRefs) {
				return result, errors.Join(ErrReleaseMismatch, receiptErr)
			}
		} else if err := validateStoredQualificationReceipts(s, record); err != nil {
			return result, err
		}
	} else {
		if req.Branch == "" && req.Commit == "" {
			return result, ErrInvalidInput
		}
		source, resolveErr := resolver.Resolve(ctx, req.Branch, req.Commit)
		if resolveErr != nil {
			return result, resolveErr
		}
		buildID, idErr := newID()
		if idErr != nil {
			return result, idErr
		}
		releaseID := req.ReleaseID
		if releaseID == "" {
			suffix, _ := newID()
			releaseID = fmt.Sprintf("rr-%s-%s", source.Commit[:10], suffix[:8])
		}
		if !releasePattern.MatchString(releaseID) {
			return result, ErrInvalidInput
		}
		stamp := now().UTC().Format(time.RFC3339Nano)
		record = BuildRecord{FormatVersion: FormatVersion, BuildID: buildID, ReleaseID: releaseID, Source: source, Stage: "source_frozen", CreatedUTC: stamp, UpdatedUTC: stamp, Artifacts: map[string]string{}}
		if req.PlanOnly {
			return BuildResult{Record: record, Outcome: "planned_no_effects", Next: "run build-release with the same selectors to freeze and build"}, nil
		}
		record.QualificationReceiptRefs, err = persistQualificationReceipts(s, req.QualificationReceipts, source.Commit)
		if err != nil {
			return result, err
		}
		if err := s.SaveBuild(record); err != nil {
			return result, err
		}
	}
	if req.PlanOnly {
		return BuildResult{Record: record, Outcome: "planned_no_effects", Next: "run build-release with the same selectors to freeze and build"}, nil
	}
	if record.Artifacts == nil {
		record.Artifacts = map[string]string{}
	}
	if record.BuildSettings == nil {
		record.BuildSettings = map[string]HostBuildSettings{}
	}
	bundles := bundleMap(record.Bundles)
	for _, host := range HostOrder {
		if existing, ok := bundles[host]; ok {
			if err := backend.VerifyArtifact(ctx, host, record.Artifacts[host], existing.SHA256); err != nil {
				return result, fmt.Errorf("verify frozen %s artifact: %w", host, err)
			}
			if err := verifyQualificationBundle(s, record, host, existing, record.BuildSettings[host]); err != nil {
				return result, err
			}
			continue
		}
		artifact, buildErr := backend.BuildNative(ctx, host, record.Source, record.BuildID, record.ReleaseID)
		if buildErr != nil {
			record.Stage = "build_blocked"
			record.LastError = buildErr.Error()
			record.UpdatedUTC = now().UTC().Format(time.RFC3339Nano)
			_ = s.SaveBuild(record)
			result.Record = record
			result.Outcome = "build_blocked"
			return result, fmt.Errorf("native build %s: %w", host, buildErr)
		}
		if artifact.Bundle.HostID != host || artifact.Bundle.SourceRevision != record.Source.Commit || artifact.Bundle.Toolchain == "" || artifact.Path == "" || artifact.Bundle.Bytes <= 0 || !digestPattern.MatchString(artifact.Bundle.SHA256) || !digestPattern.MatchString(artifact.Bundle.ManifestSHA) || artifact.Bundle.GOOS == "" || artifact.Bundle.GOARCH == "" || !validHostBuildSettings(artifact.Settings, host, artifact.Bundle) {
			return result, ErrOperationCorrupted
		}
		receipt, err := receiptForHost(s, record.QualificationReceiptRefs, host)
		if err != nil {
			return result, err
		}
		if err := verifyQualificationReceiptForBuild(receipt, host, artifact.Bundle, artifact.Settings); err != nil {
			return result, err
		}
		if len(bundles) > 0 {
			for _, prior := range bundles {
				if artifactBundleCompatible(artifact.Bundle, prior, record.Source.Commit) != nil {
					return result, ErrOperationCorrupted
				}
			}
		}
		bundles[host] = artifact.Bundle
		record.Artifacts[host] = artifact.Path
		record.BuildSettings[host] = artifact.Settings
		record.Bundles = orderedBundles(bundles)
		record.Stage = "building"
		record.LastError = ""
		record.UpdatedUTC = now().UTC().Format(time.RFC3339Nano)
		if err := s.SaveBuild(record); err != nil {
			return result, err
		}
	}
	if len(bundles) != len(HostOrder) {
		return result, ErrOperationCorrupted
	}
	ordered := orderedBundles(bundles)
	toolchain := bundles[HostOrder[0]].Toolchain
	for _, b := range ordered {
		if b.Toolchain != toolchain {
			return result, ErrOperationCorrupted
		}
	}
	manifestDigest, err := CanonicalReleaseDigest(record.ReleaseID, record.Source.Commit, toolchain, ordered)
	if err != nil {
		return result, err
	}
	release := Release{ID: record.ReleaseID, Digest: manifestDigest, Source: record.Source, Toolchain: toolchain, ProvenanceSHA: "", Bundles: bundles, Sealed: true}
	settings := make([]HostBuildSettings, 0, len(HostOrder))
	for _, host := range HostOrder {
		setting, ok := record.BuildSettings[host]
		if !ok || !validHostBuildSettings(setting, host, bundles[host]) {
			return result, ErrOperationCorrupted
		}
		settings = append(settings, setting)
	}
	provenance := Provenance{FormatVersion: 1, BuildID: record.BuildID, ReleaseID: release.ID, ReleaseSHA256: release.Digest, Source: record.Source, Toolchain: toolchain, ControllerRevision: controllerRevision, Hosts: ordered, BuildSettings: settings, QualificationReceiptRefs: append([]string(nil), record.QualificationReceiptRefs...)}
	provenanceBytes, err := json.MarshalIndent(provenance, "", "  ")
	if err != nil {
		return result, err
	}
	provenanceBytes = append(provenanceBytes, '\n')
	provenanceSum := digest(provenanceBytes)
	release.ProvenanceSHA = provenanceSum
	if err := s.SaveProvenance(record.BuildID, provenanceBytes); err != nil {
		return result, err
	}
	if err := s.SaveReleaseProvenance(release.ID, provenanceBytes); err != nil {
		return result, err
	}
	if err := s.SaveRelease(release); err != nil {
		return result, err
	}
	record.ProvenanceSHA = provenanceSum
	record.Stage = "publishing"
	record.UpdatedUTC = now().UTC().Format(time.RFC3339Nano)
	if err := s.SaveBuild(record); err != nil {
		return result, err
	}
	published := stringSet(record.PublishedHosts)
	for _, host := range HostOrder {
		if published[host] {
			if err := backend.InspectPublished(ctx, host, release); err != nil {
				return result, fmt.Errorf("published %s release no longer verifies: %w", host, err)
			}
			continue
		}
		if err := backend.VerifyArtifact(ctx, host, record.Artifacts[host], bundles[host].SHA256); err != nil {
			return result, err
		}
		if err := backend.PublishNative(ctx, host, release, record.Artifacts[host]); err != nil {
			record.Stage = "publication_blocked"
			record.LastError = err.Error()
			_ = s.SaveBuild(record)
			result.Record = record
			result.Release = release
			result.Outcome = "publication_blocked"
			return result, err
		}
		if err := backend.InspectPublished(ctx, host, release); err != nil {
			return result, err
		}
		published[host] = true
		record.PublishedHosts = append(record.PublishedHosts, host)
		record.Stage = "publishing"
		record.LastError = ""
		record.UpdatedUTC = now().UTC().Format(time.RFC3339Nano)
		if err := s.SaveBuild(record); err != nil {
			return result, err
		}
	}
	record.Stage = "ready"
	record.UpdatedUTC = now().UTC().Format(time.RFC3339Nano)
	if err := s.SaveBuild(record); err != nil {
		return result, err
	}
	result = BuildResult{Record: record, Release: release, Outcome: "ready_without_activation"}
	return result, nil
}

func validHostBuildSettings(s HostBuildSettings, host string, bundle Bundle) bool {
	if s.HostID != host || s.GOOS != bundle.GOOS || s.GOARCH != bundle.GOARCH || !s.CGOEnabled ||
		!digestPattern.MatchString(s.ToolchainBinarySHA) || len(s.Flags) == 0 {
		return false
	}
	want := []string{"-trimpath", "-buildvcs=false"}
	if len(s.Flags) != len(want) {
		return false
	}
	for i := range want {
		if s.Flags[i] != want[i] {
			return false
		}
	}
	return true
}

func (s *Store) SaveProvenance(buildID string, data []byte) error {
	if s == nil || !operationPattern.MatchString(buildID) || len(data) == 0 || len(data) > 1<<20 {
		return ErrInvalidInput
	}
	path := filepath.Join(s.Root, "builds", buildID+".provenance.json")
	if existing, err := os.ReadFile(path); err == nil {
		if string(existing) != string(data) {
			return ErrReleaseMismatch
		}
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".provenance-*.tmp")
	if err != nil {
		return err
	}
	name := tmp.Name()
	ok := false
	defer func() {
		tmp.Close()
		if !ok {
			_ = os.Remove(name)
		}
	}()
	if err := tmp.Chmod(0o600); err != nil {
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		return err
	}
	if err := tmp.Sync(); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(name, path); err != nil {
		return err
	}
	ok = true
	return nil
}

func bundleMap(list []Bundle) map[string]Bundle {
	m := map[string]Bundle{}
	for _, b := range list {
		m[b.HostID] = b
	}
	return m
}
func orderedBundles(m map[string]Bundle) []Bundle {
	list := make([]Bundle, 0, len(m))
	for _, b := range m {
		list = append(list, b)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].HostID < list[j].HostID })
	return list
}
func artifactBundleCompatible(a, b Bundle, source string) error {
	if a.Toolchain != b.Toolchain || a.SourceRevision != source || b.SourceRevision != source {
		return ErrReleaseMismatch
	}
	return nil
}
func stringSet(list []string) map[string]bool {
	m := map[string]bool{}
	for _, v := range list {
		m[v] = true
	}
	return m
}
func newID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}
