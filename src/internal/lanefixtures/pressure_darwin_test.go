//go:build darwin

package lanefixtures

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

func runFixturePressure(t *testing.T, m Manifest) error {
	if m.UnitID != "LANES-L2-08" {
		return ErrManifest
	}
	for _, mount := range m.Mounts {
		if err := verifyFixtureDirectory(mount.Path, m.FixtureRoot); err != nil {
			return err
		}
		path := filepath.Join(mount.Path, ".rsr-apfs-quota-probe-"+m.FixtureID)
		file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err != nil {
			return fmt.Errorf("create APFS quota probe for %s: %w", mount.Role, err)
		}
		created := true
		func() {
			defer func() {
				if created {
					if err := os.Remove(path); err != nil {
						t.Errorf("remove task-owned APFS quota probe %s: %v", mount.Role, err)
					}
				}
			}()
			chunk := make([]byte, 1<<20)
			var written uint64
			var quotaError error
			for written < mount.QuotaBytes+4096 {
				n, writeErr := file.Write(chunk)
				written += uint64(n)
				if writeErr != nil {
					quotaError = writeErr
					break
				}
				if written%(64<<20) == 0 {
					if syncErr := file.Sync(); syncErr != nil {
						quotaError = syncErr
						break
					}
				}
			}
			if quotaError == nil {
				quotaError = file.Sync()
			}
			if quotaError == nil {
				_, quotaError = file.Write(make([]byte, 4096))
			}
			if closeErr := file.Close(); quotaError == nil {
				quotaError = closeErr
			}
			if !errors.Is(quotaError, syscall.ENOSPC) && !errors.Is(quotaError, syscall.EDQUOT) {
				err = fmt.Errorf("APFS quota for %s was not enforced at %d bytes (written=%d, error=%v)", mount.Role, mount.QuotaBytes, written, quotaError)
				return
			}
			if written < mount.QuotaBytes-(1<<20) || written > mount.QuotaBytes+(1<<20) {
				err = fmt.Errorf("APFS quota for %s triggered outside expected bound (quota=%d, written=%d)", mount.Role, mount.QuotaBytes, written)
				return
			}
			if removeErr := os.Remove(path); removeErr != nil {
				err = fmt.Errorf("remove APFS quota probe for %s: %w", mount.Role, removeErr)
				return
			}
			created = false
			t.Logf("APFS %s quota enforced at %d/%d bytes", mount.Role, written, mount.QuotaBytes)
		}()
		if err != nil {
			return err
		}
	}
	var devPath string
	for _, mount := range m.Mounts {
		if mount.Role == "dev" {
			devPath = mount.Path
			break
		}
	}
	if devPath == "" {
		return ErrManifest
	}
	if err := probeFixtureLogBudget(t, devPath, m.FixtureID); err != nil {
		return fmt.Errorf("fixture log saturation: %w", err)
	}
	if err := VerifyLive(m); err != nil {
		return fmt.Errorf("post-probe APFS identity/headroom: %w", err)
	}
	return nil
}

func verifyFixtureDirectory(path, root string) error {
	if path != root && !strings.HasPrefix(path, root+string(filepath.Separator)) {
		return ErrManifest
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil || resolved != path {
		return fmt.Errorf("fixture mount path is missing or symlinked: %s", path)
	}
	return verifyOwnedFixtureDirectory(path)
}
