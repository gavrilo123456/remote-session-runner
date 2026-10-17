package laneinstall

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"time"

	"golang.org/x/sys/unix"

	"remote-session-runner/src/internal/config"
)

type simpleInstallMarker struct {
	FormatVersion int                `json:"format_version"`
	HostID        string             `json:"host_id"`
	Lane          config.ReleaseLane `json:"lane"`
	CreatedAt     time.Time          `json:"created_at"`
}

func simpleInstallMarkerPath(namespace config.LaneNamespace) string {
	return filepath.Join(namespace.Staging, "install-progress.json")
}

func readSimpleInstallMarker(namespace config.LaneNamespace) (simpleInstallMarker, bool, error) {
	path := simpleInstallMarkerPath(namespace)
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return simpleInstallMarker{}, false, nil
	}
	if err != nil {
		return simpleInstallMarker{}, false, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
		return simpleInstallMarker{}, false, ErrSimpleLanePreflight
	}
	var stat unix.Stat_t
	if err := unix.Lstat(path, &stat); err != nil || stat.Uid != uint32(os.Getuid()) {
		return simpleInstallMarker{}, false, ErrSimpleLanePreflight
	}
	contents, err := os.ReadFile(path)
	if err != nil {
		return simpleInstallMarker{}, false, err
	}
	decoder := json.NewDecoder(bytes.NewReader(contents))
	decoder.DisallowUnknownFields()
	var marker simpleInstallMarker
	if err := decoder.Decode(&marker); err != nil || marker.FormatVersion != 1 || marker.HostID == "" ||
		(marker.Lane != config.ReleaseLaneDev && marker.Lane != config.ReleaseLaneQAS) || marker.CreatedAt.IsZero() {
		return simpleInstallMarker{}, false, ErrSimpleLanePreflight
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return simpleInstallMarker{}, false, ErrSimpleLanePreflight
	}
	return marker, true, nil
}

func writeSimpleInstallMarker(namespace config.LaneNamespace, hostID string, lane config.ReleaseLane) error {
	if hostID == "" || (lane != config.ReleaseLaneDev && lane != config.ReleaseLaneQAS) {
		return ErrSimpleLanePreflight
	}
	if existing, ok, err := readSimpleInstallMarker(namespace); err != nil {
		return err
	} else if ok {
		if existing.HostID == hostID && existing.Lane == lane {
			return nil
		}
		return ErrSimpleLanePreflight
	}
	marker := simpleInstallMarker{FormatVersion: 1, HostID: hostID, Lane: lane, CreatedAt: time.Now().UTC()}
	contents, err := json.MarshalIndent(marker, "", "  ")
	if err != nil {
		return err
	}
	contents = append(contents, '\n')
	return writeSimpleUserArtifact(Artifact{InstallPath: simpleInstallMarkerPath(namespace), Mode: 0o600, Contents: string(contents)})
}

func removeSimpleInstallMarker(namespace config.LaneNamespace, hostID string, lane config.ReleaseLane) error {
	marker, exists, err := readSimpleInstallMarker(namespace)
	if err != nil {
		return err
	}
	if !exists {
		return nil
	}
	if marker.HostID != hostID || marker.Lane != lane {
		return ErrSimpleLanePreflight
	}
	if err := unix.Unlink(simpleInstallMarkerPath(namespace)); err != nil {
		return err
	}
	return syncSimpleDirectory(namespace.Staging)
}
