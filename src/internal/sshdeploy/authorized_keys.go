// Package sshdeploy contains the fail-closed SSH authorized_keys rendering
// and validation used by the Linux forced-command deployment.
package sshdeploy

import (
	"encoding/base64"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"unicode"
)

var (
	ErrInvalidEntry     = errors.New("invalid SSH authorized_keys entry")
	ErrUnsafeBridgePath = errors.New("SSH bridge path is unsafe")
	ErrOriginalCommand  = errors.New("SSH original command is not the fixed bridge command")
)

const DispatcherComment = "runner-mac-dispatcher"

// Entry is the only SSH key shape accepted for the dispatcher. PublicKey is a
// public authorized_keys line without options; private key material is never
// loaded by this package.
type Entry struct {
	PublicKey  string
	BridgePath string
	Comment    string
}

// FixedCommand returns the exact argv-like command placed in the authorized
// key option. It is compared byte-for-byte by the wrapper using SSH_ORIGINAL_COMMAND.
func (e Entry) FixedCommand() (string, error) {
	path := strings.TrimSpace(e.BridgePath)
	if path == "" || !filepath.IsAbs(path) || filepath.Clean(path) != path || hasUnsafePathRune(path) {
		return "", ErrUnsafeBridgePath
	}
	return path + " --stdio", nil
}

// Render returns one restricted authorized_keys line. The restriction set is
// intentionally fixed: no PTY, agent/X11 forwarding, TCP forwarding, or user
// startup file is available to the key.
func (e Entry) Render() (string, error) {
	command, err := e.FixedCommand()
	if err != nil {
		return "", err
	}
	keyType, keyData, comment, err := parsePublicKey(e.PublicKey)
	if err != nil {
		return "", err
	}
	if comment == "" {
		comment = strings.TrimSpace(e.Comment)
	}
	if comment == "" {
		comment = DispatcherComment
	}
	if hasUnsafeComment(comment) {
		return "", fmt.Errorf("%w: comment", ErrInvalidEntry)
	}
	return `restrict,command="` + command + `" ` + keyType + " " + keyData + " " + comment, nil
}

// ValidateRendered checks that a line contains exactly the fixed restriction
// and command shape produced by Render. It rejects option widening rather than
// attempting to interpret arbitrary authorized_keys options.
func ValidateRendered(line string, expected Entry) error {
	rendered, err := expected.Render()
	if err != nil {
		return err
	}
	if strings.TrimSpace(line) != rendered {
		return fmt.Errorf("%w: rendered restrictions or key differ", ErrInvalidEntry)
	}
	return nil
}

// ValidateOriginalCommand applies the server-side wrapper's exact command
// check. SSH_ORIGINAL_COMMAND is untrusted text and is never shell-evaluated.
func ValidateOriginalCommand(original string, expected Entry) error {
	command, err := expected.FixedCommand()
	if err != nil {
		return err
	}
	if original != command {
		return fmt.Errorf("%w: got %q", ErrOriginalCommand, original)
	}
	return nil
}

func parsePublicKey(raw string) (keyType, keyData, comment string, err error) {
	if strings.ContainsAny(raw, "\r\n") {
		return "", "", "", fmt.Errorf("%w: key contains a newline", ErrInvalidEntry)
	}
	fields := strings.Fields(raw)
	if len(fields) < 2 || len(fields) > 3 || fields[0] != "ssh-ed25519" {
		return "", "", "", fmt.Errorf("%w: only one ssh-ed25519 public key is accepted", ErrInvalidEntry)
	}
	if _, decodeErr := base64.StdEncoding.DecodeString(fields[1]); decodeErr != nil {
		return "", "", "", fmt.Errorf("%w: public key encoding", ErrInvalidEntry)
	}
	comment = ""
	if len(fields) == 3 {
		comment = fields[2]
	}
	return fields[0], fields[1], comment, nil
}

func hasUnsafePathRune(value string) bool {
	for _, r := range value {
		if unicode.IsSpace(r) || unicode.IsControl(r) || strings.ContainsRune(",\\\"';&|$`()<>!", r) {
			return true
		}
	}
	return false
}

func hasUnsafeComment(value string) bool {
	for _, r := range value {
		if unicode.IsSpace(r) || unicode.IsControl(r) || r == ',' {
			return true
		}
	}
	return false
}
