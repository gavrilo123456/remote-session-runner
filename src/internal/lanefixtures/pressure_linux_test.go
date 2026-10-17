//go:build linux

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
	if m.UnitID != "LANES-L2-07" {
		return ErrManifest
	}
	for _, project := range m.Projects {
		if err := verifyFixtureDirectory(project.Path, m.FixtureRoot); err != nil {
			return err
		}
		if err := requireThirtyPercentFree(project.Path); err != nil {
			return err
		}
		if err := probeLinuxByteQuota(t, m.FixtureID, project); err != nil {
			return err
		}
		if err := probeLinuxInodeQuota(t, m.FixtureID, project); err != nil {
			return err
		}
		if err := requireThirtyPercentFree(project.Path); err != nil {
			return err
		}
	}
	var managementPath string
	for _, project := range m.Projects {
		if project.Name == "rsr-management" {
			managementPath = project.Path
			break
		}
	}
	if managementPath == "" {
		return ErrManifest
	}
	if err := probeFixtureLogBudget(t, managementPath, m.FixtureID); err != nil {
		return fmt.Errorf("fixture log saturation: %w", err)
	}
	if err := requireThirtyPercentFree(managementPath); err != nil {
		return fmt.Errorf("post-log data fixture reserve: %w", err)
	}
	return nil
}

func probeLinuxByteQuota(t *testing.T, fixtureID string, project Project) error {
	path := filepath.Join(project.Path, ".rsr-byte-probe-"+fixtureID)
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("create byte probe for project %s: %w", project.Name, err)
	}
	created := true
	defer func() {
		if created {
			if err := os.Remove(path); err != nil {
				t.Errorf("remove task-owned byte probe %s: %v", project.Name, err)
			}
		}
	}()
	chunk := make([]byte, 1<<20)
	var written uint64
	limit := project.HardBytes + 4096
	var quotaError error
	for written < limit {
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
	closeErr := file.Close()
	if quotaError == nil {
		quotaError = closeErr
	}
	if !errors.Is(quotaError, syscall.EDQUOT) {
		return fmt.Errorf("project %s byte hard limit was not enforced as EDQUOT (written=%d, error=%v)", project.Name, written, quotaError)
	}
	if written < project.HardBytes-(64<<20) || written > project.HardBytes+(1<<20) {
		return fmt.Errorf("project %s byte limit triggered outside expected bound (hard=%d, written=%d)", project.Name, project.HardBytes, written)
	}
	if err := os.Remove(path); err != nil {
		return fmt.Errorf("remove byte probe for project %s: %w", project.Name, err)
	}
	created = false
	t.Logf("project %s byte quota enforced at %d/%d bytes", project.Name, written, project.HardBytes)
	return nil
}

func probeLinuxInodeQuota(t *testing.T, fixtureID string, project Project) error {
	prefix := ".rsr-inode-probe-" + fixtureID + fmt.Sprintf("-%d-", project.ID)
	var created uint64
	defer func() {
		for index := uint64(0); index < created; index++ {
			path := filepath.Join(project.Path, fmt.Sprintf("%s%08d", prefix, index))
			if err := os.Remove(path); err != nil {
				t.Errorf("remove task-owned inode probe for %s at %d: %v", project.Name, index, err)
			}
		}
	}()
	for created <= project.HardInodes {
		path := filepath.Join(project.Path, fmt.Sprintf("%s%08d", prefix, created))
		file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err != nil {
			if errors.Is(err, syscall.EDQUOT) {
				break
			}
			return fmt.Errorf("project %s inode probe stopped before EDQUOT at %d/%d: %w", project.Name, created, project.HardInodes, err)
		}
		created++
		if err := file.Close(); err != nil {
			return fmt.Errorf("close inode probe for %s at %d: %w", project.Name, created-1, err)
		}
	}
	if created < project.HardInodes-16 || created > project.HardInodes {
		return fmt.Errorf("project %s inode hard limit not enforced near %d (created=%d)", project.Name, project.HardInodes, created)
	}
	t.Logf("project %s inode quota enforced after %d files (limit=%d)", project.Name, created, project.HardInodes)
	return nil
}

func verifyFixtureDirectory(path, root string) error {
	if path != root && !strings.HasPrefix(path, root+string(filepath.Separator)) {
		return ErrManifest
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil || resolved != path {
		return fmt.Errorf("fixture project path is missing or symlinked: %s", path)
	}
	return verifyOwnedFixtureDirectory(path)
}

func requireThirtyPercentFree(path string) error {
	var stat syscall.Statfs_t
	if err := syscall.Statfs(path, &stat); err != nil {
		return err
	}
	capacity := uint64(stat.Blocks) * uint64(stat.Bsize)
	free := uint64(stat.Bavail) * uint64(stat.Bsize)
	if capacity == 0 || free*100 < capacity*30 {
		return fmt.Errorf("fixture filesystem fell below 30%% free: %s", path)
	}
	return nil
}
