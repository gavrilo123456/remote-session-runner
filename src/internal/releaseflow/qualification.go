package releaseflow

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"
)

type stringListFlag []string

func (f *stringListFlag) String() string { return strings.Join(*f, ",") }
func (f *stringListFlag) Set(value string) error {
	if strings.TrimSpace(value) == "" {
		return ErrInvalidInput
	}
	*f = append(*f, value)
	return nil
}

// LoadSourceTestReceiptFile accepts only canonical owner-only evidence. The
// digest is the immutable reference embedded in build and release provenance.
func LoadSourceTestReceiptFile(path string) (SourceTestReceipt, string, error) {
	var receipt SourceTestReceipt
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return receipt, "", ErrInvalidInput
	}
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return receipt, "", ErrOperationCorrupted
	}
	f := os.NewFile(uintptr(fd), path)
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 || info.Size() > 1<<20 {
		return receipt, "", ErrOperationCorrupted
	}
	if st, ok := info.Sys().(*syscall.Stat_t); !ok || int(st.Uid) != os.Getuid() || st.Nlink != 1 {
		return receipt, "", ErrOperationCorrupted
	}
	data, err := io.ReadAll(io.LimitReader(f, (1<<20)+1))
	if err != nil || len(data) > 1<<20 {
		return receipt, "", ErrOperationCorrupted
	}
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if err := d.Decode(&receipt); err != nil || d.Decode(&struct{}{}) != io.EOF {
		return SourceTestReceipt{}, "", ErrOperationCorrupted
	}
	canonical, err := json.MarshalIndent(receipt, "", "  ")
	if err != nil || !bytes.Equal(data, append(canonical, '\n')) || !validSourceTestReceipt(receipt) {
		return SourceTestReceipt{}, "", ErrOperationCorrupted
	}
	return receipt, digest(data), nil
}

func persistQualificationReceipts(s *Store, receipts []SourceTestReceipt, sourceCommit string) ([]string, error) {
	if s == nil || len(receipts) != len(HostOrder) || !shaPattern.MatchString(sourceCommit) {
		return nil, fmt.Errorf("one native full-suite receipt per host is required: %w", ErrInvalidInput)
	}
	byHost := make(map[string]SourceTestReceipt, len(receipts))
	for _, receipt := range receipts {
		if !validSourceTestReceipt(receipt) || receipt.SourceCommit != sourceCommit {
			return nil, fmt.Errorf("source-test receipt is invalid or for another commit: %w", ErrReleaseMismatch)
		}
		if _, exists := byHost[receipt.HostID]; exists {
			return nil, ErrOperationCorrupted
		}
		byHost[receipt.HostID] = receipt
	}
	refs := make([]string, 0, len(HostOrder))
	for _, host := range HostOrder {
		_, ok := byHost[host]
		if !ok {
			return nil, fmt.Errorf("missing %s native source-test receipt: %w", host, ErrInvalidInput)
		}
	}
	for _, host := range HostOrder {
		receipt := byHost[host]
		ref, err := s.SaveSourceTestReceipt(receipt)
		if err != nil {
			return nil, err
		}
		refs = append(refs, host+":sha256:"+ref)
	}
	return refs, nil
}

func validateStoredQualificationReceipts(s *Store, record BuildRecord) error {
	if s == nil || len(record.QualificationReceiptRefs) != len(HostOrder) {
		return fmt.Errorf("build is missing the four host-bound qualification receipts: %w", ErrOperationCorrupted)
	}
	for i, host := range HostOrder {
		prefix := host + ":sha256:"
		ref := record.QualificationReceiptRefs[i]
		if !strings.HasPrefix(ref, prefix) {
			return ErrOperationCorrupted
		}
		receipt, err := s.LoadSourceTestReceipt(strings.TrimPrefix(ref, prefix))
		if err != nil || receipt.HostID != host || receipt.SourceCommit != record.Source.Commit {
			return errors.Join(ErrOperationCorrupted, err)
		}
	}
	return nil
}

func receiptForHost(s *Store, refs []string, host string) (SourceTestReceipt, error) {
	for _, ref := range refs {
		prefix := host + ":sha256:"
		if strings.HasPrefix(ref, prefix) {
			return s.LoadSourceTestReceipt(strings.TrimPrefix(ref, prefix))
		}
	}
	return SourceTestReceipt{}, ErrOperationCorrupted
}

func verifyQualificationBundle(s *Store, record BuildRecord, host string, bundle Bundle, settings HostBuildSettings) error {
	receipt, err := receiptForHost(s, record.QualificationReceiptRefs, host)
	if err != nil {
		return err
	}
	return verifyQualificationReceiptForBuild(receipt, host, bundle, settings)
}

func verifyQualificationReceiptForBuild(receipt SourceTestReceipt, host string, bundle Bundle, settings HostBuildSettings) error {
	if !validSourceTestReceipt(receipt) || receipt.HostID != host || receipt.SourceCommit != bundle.SourceRevision ||
		receipt.GOOS != bundle.GOOS || receipt.GOARCH != bundle.GOARCH || receipt.Toolchain != bundle.Toolchain ||
		receipt.ToolchainBinarySHA256 != settings.ToolchainBinarySHA {
		return fmt.Errorf("%s native test receipt does not match frozen bundle/toolchain: %w", host, ErrReleaseMismatch)
	}
	return nil
}

func validQualificationReference(ref, host string) bool {
	return strings.HasPrefix(ref, host+":sha256:") && digestPattern.MatchString(strings.TrimPrefix(ref, host+":sha256:"))
}
