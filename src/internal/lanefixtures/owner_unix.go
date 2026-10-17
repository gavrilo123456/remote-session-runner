//go:build linux || darwin

package lanefixtures

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"syscall"
)

func ownedByCurrentUser(info os.FileInfo) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && stat.Uid == uint32(os.Geteuid())
}

func fixtureCommandEnvironment() ([]string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("resolve fixture command HOME: %w", err)
	}
	if home == "" || !filepath.IsAbs(home) {
		return nil, fmt.Errorf("resolve fixture command HOME: empty or relative path")
	}
	tmp := "/tmp"
	if runtime.GOOS == "darwin" {
		tmp = "/private/tmp"
	}
	return []string{
		"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin",
		"HOME=" + home,
		"TMPDIR=" + tmp,
		"LANG=C",
	}, nil
}
