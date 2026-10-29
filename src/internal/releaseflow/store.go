package releaseflow

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

// Store keeps workflow state outside the checkout and the sealed release
// catalogs. Its owner-only directory and atomic replacement make records
// durable across coordinator interruption without changing host receipts.
type Store struct{ Root string }

func NewStore(root string) (*Store, error) {
	if !filepath.IsAbs(root) || filepath.Clean(root) != root {
		return nil, ErrInvalidInput
	}
	var err error
	root, err = resolvePhysicalPath(root)
	if err != nil {
		return nil, err
	}
	if err := ensurePrivatePath(root); err != nil {
		return nil, err
	}
	for _, name := range []string{"builds", "operations", "releases", "acceptances", "source-test-receipts", "locks"} {
		if err := ensurePrivatePath(filepath.Join(root, name)); err != nil {
			return nil, err
		}
	}
	return &Store{Root: root}, nil
}

func resolvePhysicalPath(path string) (string, error) {
	missing := []string{}
	current := path
	for {
		_, err := os.Lstat(current)
		if err == nil {
			break
		}
		if !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
		parent := filepath.Dir(current)
		if parent == current {
			return "", ErrInvalidInput
		}
		missing = append(missing, filepath.Base(current))
		current = parent
	}
	resolved, err := filepath.EvalSymlinks(current)
	if err != nil {
		return "", err
	}
	for i := len(missing) - 1; i >= 0; i-- {
		resolved = filepath.Join(resolved, missing[i])
	}
	return filepath.Clean(resolved), nil
}

func DefaultStore() (*Store, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	return NewStore(filepath.Join(home, "projects-runtime", "runner-release-operations"))
}

func (s *Store) WithLaneLock(lane string, fn func() error) error {
	if s == nil || !contains(LaneOrder, lane) || fn == nil {
		return ErrInvalidInput
	}
	path := filepath.Join(s.Root, "locks", lane+".lock")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	if err := f.Chmod(0o600); err != nil {
		return err
	}
	if err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		if errors.Is(err, unix.EWOULDBLOCK) || errors.Is(err, unix.EAGAIN) {
			return fmt.Errorf("acquire %s lane lock: %w (%v)", lane, ErrOperationConflict, err)
		}
		return fmt.Errorf("acquire %s lane lock: %w", lane, err)
	}
	defer unix.Flock(int(f.Fd()), unix.LOCK_UN)
	return fn()
}

func (s *Store) SaveOperation(op Operation) error {
	if s == nil || ValidateOperation(op) != nil {
		return ErrInvalidInput
	}
	path := filepath.Join(s.Root, "operations", op.Lane, op.OperationID+".json")
	if err := ensurePrivatePath(filepath.Dir(path)); err != nil {
		return err
	}
	return writeCanonicalAtomic(path, op)
}

func (s *Store) LoadOperation(lane, id string) (Operation, error) {
	if s == nil || !contains(LaneOrder, lane) || !operationPattern.MatchString(id) {
		return Operation{}, ErrInvalidInput
	}
	path := filepath.Join(s.Root, "operations", lane, id+".json")
	var op Operation
	if err := readStrictJSON(path, &op); err != nil {
		return Operation{}, err
	}
	if ValidateOperation(op) != nil || op.Lane != lane || op.OperationID != id {
		return Operation{}, ErrOperationCorrupted
	}
	return op, nil
}

func (s *Store) SaveBuild(record BuildRecord) error {
	if s == nil || record.FormatVersion != FormatVersion || !operationPattern.MatchString(record.BuildID) || !releasePattern.MatchString(record.ReleaseID) || !shaPattern.MatchString(record.Source.Commit) {
		return ErrInvalidInput
	}
	if len(record.QualificationReceiptRefs) != 0 && len(record.QualificationReceiptRefs) != len(HostOrder) {
		return ErrInvalidInput
	}
	for i, ref := range record.QualificationReceiptRefs {
		if !validQualificationReference(ref, HostOrder[i]) {
			return ErrInvalidInput
		}
	}
	path := filepath.Join(s.Root, "builds", record.BuildID+".json")
	return writeCanonicalAtomic(path, record)
}

func (s *Store) LoadBuild(id string) (BuildRecord, error) {
	if s == nil || !operationPattern.MatchString(id) || strings.ContainsAny(id, `/\`) {
		return BuildRecord{}, ErrInvalidInput
	}
	var record BuildRecord
	if err := readStrictJSON(filepath.Join(s.Root, "builds", id+".json"), &record); err != nil {
		return BuildRecord{}, err
	}
	if record.FormatVersion != FormatVersion || record.BuildID != id || !releasePattern.MatchString(record.ReleaseID) || !shaPattern.MatchString(record.Source.Commit) || len(record.QualificationReceiptRefs) != 0 && len(record.QualificationReceiptRefs) != len(HostOrder) {
		return BuildRecord{}, ErrOperationCorrupted
	}
	for i, ref := range record.QualificationReceiptRefs {
		if !validQualificationReference(ref, HostOrder[i]) {
			return BuildRecord{}, ErrOperationCorrupted
		}
	}
	return record, nil
}

func (s *Store) SaveRelease(r Release) error {
	if s == nil || ValidateRelease(r) != nil {
		return ErrInvalidInput
	}
	path := filepath.Join(s.Root, "releases", r.ID+".json")
	if current, err := readRelease(path); err == nil {
		current.PublishedHosts = nil
		r.PublishedHosts = nil
		old, _ := json.Marshal(current)
		next, _ := json.Marshal(r)
		if !bytes.Equal(old, next) {
			return ErrReleaseMismatch
		}
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return writeCanonicalImmutable(path, r)
}

func (s *Store) LoadRelease(id string) (Release, error) {
	if s == nil || !releasePattern.MatchString(id) {
		return Release{}, ErrInvalidInput
	}
	r, err := readRelease(filepath.Join(s.Root, "releases", id+".json"))
	if err != nil {
		return Release{}, err
	}
	provenancePath := filepath.Join(s.Root, "releases", id+".provenance.json")
	var provenance Provenance
	if err := readStrictJSON(provenancePath, &provenance); err != nil {
		return Release{}, fmt.Errorf("release provenance unavailable: %w", err)
	}
	if len(provenance.QualificationReceiptRefs) != 0 && len(provenance.QualificationReceiptRefs) != len(HostOrder) {
		return Release{}, ErrOperationCorrupted
	}
	for i, ref := range provenance.QualificationReceiptRefs {
		if !validQualificationReference(ref, HostOrder[i]) {
			return Release{}, ErrOperationCorrupted
		}
		if len(provenance.QualificationReceiptRefs) != 0 {
			receipt, receiptErr := s.LoadSourceTestReceipt(strings.TrimPrefix(ref, HostOrder[i]+":sha256:"))
			if receiptErr != nil || receipt.HostID != HostOrder[i] || receipt.SourceCommit != provenance.Source.Commit {
				return Release{}, errors.Join(ErrOperationCorrupted, receiptErr)
			}
		}
	}
	data, err := os.ReadFile(provenancePath)
	if err != nil || digest(data) != r.ProvenanceSHA || provenance.ReleaseID != r.ID || provenance.ReleaseSHA256 != r.Digest ||
		provenance.Source != r.Source || provenance.Toolchain != r.Toolchain || len(provenance.Hosts) != len(HostOrder) ||
		len(provenance.BuildSettings) != len(HostOrder) || !shaPattern.MatchString(provenance.ControllerRevision) {
		return Release{}, ErrOperationCorrupted
	}
	settingsByHost := make(map[string]HostBuildSettings, len(provenance.BuildSettings))
	for _, setting := range provenance.BuildSettings {
		if _, exists := settingsByHost[setting.HostID]; exists {
			return Release{}, ErrOperationCorrupted
		}
		settingsByHost[setting.HostID] = setting
	}
	for i, host := range HostOrderByName {
		if provenance.Hosts[i].HostID != host || !reflect.DeepEqual(provenance.Hosts[i], r.Bundles[host]) ||
			!validHostBuildSettings(settingsByHost[host], host, r.Bundles[host]) {
			return Release{}, ErrOperationCorrupted
		}
	}
	return r, nil
}

func (s *Store) LoadReleaseProvenance(id string) (Provenance, error) {
	release, err := s.LoadRelease(id)
	if err != nil {
		return Provenance{}, err
	}
	var provenance Provenance
	path := filepath.Join(s.Root, "releases", id+".provenance.json")
	if err := readStrictJSON(path, &provenance); err != nil {
		return Provenance{}, err
	}
	data, err := os.ReadFile(path)
	if err != nil || digest(data) != release.ProvenanceSHA {
		return Provenance{}, ErrOperationCorrupted
	}
	return provenance, nil
}

func (s *Store) SaveReleaseProvenance(releaseID string, data []byte) error {
	if s == nil || !releasePattern.MatchString(releaseID) || len(data) == 0 || len(data) > 1<<20 {
		return ErrInvalidInput
	}
	path := filepath.Join(s.Root, "releases", releaseID+".provenance.json")
	if existing, err := os.ReadFile(path); err == nil {
		if !bytes.Equal(existing, data) {
			return ErrReleaseMismatch
		}
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return writeCanonicalBytesImmutable(path, data)
}

func (s *Store) SaveAcceptance(record AcceptanceRecord) error {
	if s == nil || !validAcceptanceRecord(record) {
		return ErrInvalidInput
	}
	path := filepath.Join(s.Root, "acceptances", record.Lane, record.ReleaseID+".json")
	if existing, err := readAcceptance(path); err == nil {
		old, _ := json.Marshal(existing)
		fresh, _ := json.Marshal(record)
		if !bytes.Equal(old, fresh) {
			return ErrReleaseMismatch
		}
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return writeCanonicalImmutable(path, record)
}

func (s *Store) SaveSourceTestReceipt(receipt SourceTestReceipt) (string, error) {
	if s == nil || !validSourceTestReceipt(receipt) {
		return "", ErrInvalidInput
	}
	data, err := json.MarshalIndent(receipt, "", "  ")
	if err != nil {
		return "", err
	}
	data = append(data, '\n')
	ref := digest(data)
	path := filepath.Join(s.Root, "source-test-receipts", ref+".json")
	if err := writeCanonicalBytesImmutable(path, data); err != nil {
		return "", err
	}
	return ref, nil
}

func (s *Store) LoadSourceTestReceipt(ref string) (SourceTestReceipt, error) {
	if s == nil || !digestPattern.MatchString(ref) {
		return SourceTestReceipt{}, ErrInvalidInput
	}
	path := filepath.Join(s.Root, "source-test-receipts", ref+".json")
	receipt, actual, err := LoadSourceTestReceiptFile(path)
	if err != nil || actual != ref {
		return SourceTestReceipt{}, errors.Join(ErrOperationCorrupted, err)
	}
	return receipt, nil
}

func (s *Store) LoadAcceptance(lane, releaseID string) (AcceptanceRecord, error) {
	if s == nil || !contains(LaneOrder, lane) || !releasePattern.MatchString(releaseID) {
		return AcceptanceRecord{}, ErrInvalidInput
	}
	return readAcceptance(filepath.Join(s.Root, "acceptances", lane, releaseID+".json"))
}

func readAcceptance(path string) (AcceptanceRecord, error) {
	var record AcceptanceRecord
	if err := readStrictJSON(path, &record); err != nil {
		return record, err
	}
	if !validAcceptanceRecord(record) {
		return AcceptanceRecord{}, ErrOperationCorrupted
	}
	return record, nil
}

func validAcceptanceRecord(record AcceptanceRecord) bool {
	if record.FormatVersion != FormatVersion || !contains(LaneOrder, record.Lane) || !record.Passed ||
		!operationPattern.MatchString(record.OperationID) || record.Profile == "" ||
		!releasePattern.MatchString(record.ReleaseID) || !digestPattern.MatchString(record.ReleaseSHA256) ||
		!digestPattern.MatchString(record.EvidenceSHA) || len(record.BundleHashes) != len(HostOrder) || !validRequiredGates(record.Gates) {
		return false
	}
	if _, err := time.Parse(time.RFC3339Nano, record.AcceptedUTC); err != nil {
		return false
	}
	for _, host := range HostOrder {
		if !digestPattern.MatchString(record.BundleHashes[host]) {
			return false
		}
	}
	return true
}

func readRelease(path string) (Release, error) {
	var r Release
	if err := readStrictJSON(path, &r); err != nil {
		return Release{}, err
	}
	if ValidateRelease(r) != nil {
		return Release{}, ErrOperationCorrupted
	}
	return r, nil
}

func readStrictJSON(path string, target any) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 || info.Size() > 8<<20 {
		return ErrOperationCorrupted
	}
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	d := json.NewDecoder(io.LimitReader(f, (8<<20)+1))
	d.DisallowUnknownFields()
	if err := d.Decode(target); err != nil {
		return ErrOperationCorrupted
	}
	var extra any
	if err := d.Decode(&extra); !errors.Is(err, io.EOF) {
		return ErrOperationCorrupted
	}
	return nil
}

func writeCanonicalAtomic(path string, value any) error {
	if err := ensurePrivatePath(filepath.Dir(path)); err != nil {
		return err
	}
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	tmp, err := os.CreateTemp(filepath.Dir(path), ".record-*.tmp")
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
	dir, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer dir.Close()
	if err := dir.Sync(); err != nil {
		return err
	}
	ok = true
	return nil
}

func writeCanonicalBytesAtomic(path string, data []byte) error {
	if err := ensurePrivatePath(filepath.Dir(path)); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".record-*.tmp")
	if err != nil {
		return err
	}
	name := tmp.Name()
	ok := false
	defer func() {
		_ = tmp.Close()
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
	dir, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer dir.Close()
	if err := dir.Sync(); err != nil {
		return err
	}
	ok = true
	return nil
}

// writeCanonicalImmutable publishes by hard-linking a fully synced temporary
// file. It never replaces an existing identity, including under concurrent
// writers.
func writeCanonicalImmutable(path string, value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	return writeCanonicalBytesImmutable(path, append(data, '\n'))
}

func writeCanonicalBytesImmutable(path string, data []byte) error {
	if err := ensurePrivatePath(filepath.Dir(path)); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".immutable-*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Link(tmpPath, path); err != nil {
		if errors.Is(err, os.ErrExist) {
			existing, readErr := os.ReadFile(path)
			if readErr == nil && bytes.Equal(existing, data) {
				return nil
			}
			return ErrReleaseMismatch
		}
		return err
	}
	dir, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}

func ensurePrivatePath(path string) error {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return ErrInvalidInput
	}
	volume := filepath.VolumeName(path)
	current := volume + string(filepath.Separator)
	for _, part := range strings.Split(strings.TrimPrefix(path, current), string(filepath.Separator)) {
		if part == "" {
			continue
		}
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if errors.Is(err, os.ErrNotExist) {
			if err := os.Mkdir(current, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
				return err
			}
			info, err = os.Lstat(current)
		}
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return ErrOperationCorrupted
		}
		if current == path && info.Mode().Perm() != 0o700 {
			if err := os.Chmod(current, 0o700); err != nil {
				return err
			}
		}
	}
	return nil
}
