package sshbridge

import (
	"bytes"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"gopkg.in/yaml.v3"

	"remote-session-runner/src/internal/domain"
)

const maxControllerMapBytes = 1 << 20

var ErrControllerMap = errors.New("SSH controller map is invalid")

type controllerMapDocument struct {
	Version int                           `yaml:"version"`
	Keys    map[string]controllerMapEntry `yaml:"keys"`
}

type controllerMapEntry struct {
	ControllerType string `yaml:"controller_type"`
	ControllerID   string `yaml:"controller_id"`
}

// LoadKeyControllerMap reads the owner-only server configuration used to map
// the key identity embedded in its forced authorized_keys command. The bridge
// never accepts a controller identity from a request frame.
func LoadKeyControllerMap(path string) (KeyControllerMap, error) {
	if path == "" || !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return nil, fmt.Errorf("%w: path must be absolute and clean", ErrControllerMap)
	}
	file, err := openOwnerOnlyControllerMap(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	raw, err := io.ReadAll(io.LimitReader(file, maxControllerMapBytes+1))
	if err != nil {
		return nil, fmt.Errorf("%w: read file: %v", ErrControllerMap, err)
	}
	if len(raw) > maxControllerMapBytes {
		return nil, fmt.Errorf("%w: file exceeds %d bytes", ErrControllerMap, maxControllerMapBytes)
	}
	decoder := yaml.NewDecoder(bytes.NewReader(raw))
	decoder.KnownFields(true)
	var document controllerMapDocument
	if err := decoder.Decode(&document); err != nil {
		return nil, fmt.Errorf("%w: decode: %v", ErrControllerMap, err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("%w: expected one YAML document", ErrControllerMap)
	}
	if document.Version != 1 || len(document.Keys) == 0 {
		return nil, fmt.Errorf("%w: version 1 and at least one key mapping are required", ErrControllerMap)
	}
	entries := make(map[string]domain.ControllerIdentity, len(document.Keys))
	for fingerprint, mapping := range document.Keys {
		if !validKeyFingerprint(fingerprint) {
			return nil, fmt.Errorf("%w: malformed key fingerprint", ErrControllerMap)
		}
		controller, err := domain.NewControllerIdentity(domain.ControllerType(mapping.ControllerType), domain.ControllerID(mapping.ControllerID))
		if err != nil {
			return nil, fmt.Errorf("%w: invalid controller mapping: %v", ErrControllerMap, err)
		}
		entries[fingerprint] = controller
	}
	result, err := NewKeyControllerMap(entries)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrControllerMap, err)
	}
	return result, nil
}

func openOwnerOnlyControllerMap(path string) (*os.File, error) {
	before, err := os.Lstat(path)
	if err != nil {
		return nil, fmt.Errorf("%w: inspect file: %v", ErrControllerMap, err)
	}
	if !before.Mode().IsRegular() || before.Mode().Perm()&0o077 != 0 {
		return nil, fmt.Errorf("%w: file must be regular and owner-only", ErrControllerMap)
	}
	stat, ok := before.Sys().(*syscall.Stat_t)
	if !ok || int(stat.Uid) != os.Getuid() {
		return nil, fmt.Errorf("%w: file owner differs from current account", ErrControllerMap)
	}
	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, fmt.Errorf("%w: open file: %v", ErrControllerMap, err)
	}
	file := os.NewFile(uintptr(fd), path)
	after, err := file.Stat()
	if err != nil || !os.SameFile(before, after) || !after.Mode().IsRegular() || after.Mode().Perm()&0o077 != 0 {
		_ = file.Close()
		return nil, fmt.Errorf("%w: file changed while opening", ErrControllerMap)
	}
	openedStat, ok := after.Sys().(*syscall.Stat_t)
	if !ok || int(openedStat.Uid) != os.Getuid() {
		_ = file.Close()
		return nil, fmt.Errorf("%w: file owner differs from current account", ErrControllerMap)
	}
	return file, nil
}

func validKeyFingerprint(value string) bool {
	if !strings.HasPrefix(value, "SHA256:") {
		return false
	}
	encoded := strings.TrimPrefix(value, "SHA256:")
	if len(encoded) != 43 {
		return false
	}
	decoded, err := base64.RawStdEncoding.DecodeString(encoded)
	return err == nil && len(decoded) == 32
}
