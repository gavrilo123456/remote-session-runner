// Package mailboxhandoff binds an external mailbox to one reversible lane
// handoff. It carries filesystem ownership and replay exclusions, never Runner
// execution authority or historical request bodies.
package mailboxhandoff

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"path/filepath"
	"regexp"
	"strings"
)

const (
	Version        = 1
	MarkerFileName = ".runner-mailbox-owner.json"
	LockFileName   = ".runner-mailbox-owner.lock"
)

var (
	ErrInvalid       = errors.New("mailbox handoff metadata is invalid")
	ErrUnsafe        = errors.New("mailbox handoff filesystem boundary is unsafe")
	ErrOwnership     = errors.New("mailbox handoff ownership is unavailable")
	ErrBusy          = errors.New("mailbox handoff already has a runtime owner")
	ErrReplay        = errors.New("historical mailbox identity is fenced")
	ErrClosed        = errors.New("mailbox handoff lease is closed")
	operationPattern = regexp.MustCompile(`^[0-9a-f]{32}$`)
	digestPattern    = regexp.MustCompile(`^[0-9a-f]{64}$`)
	namePattern      = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)
)

type Definition struct {
	ID   string `json:"id"`
	Root string `json:"root"`
}

type Mailbox struct {
	SourceID                 string   `json:"source_id"`
	TargetID                 string   `json:"target_id"`
	Root                     string   `json:"root"`
	Device                   uint64   `json:"device"`
	Inode                    uint64   `json:"inode"`
	ArchiveRelative          string   `json:"archive_relative"`
	BlockedRequestIDs        []string `json:"blocked_request_ids"`
	BlockedIdempotencySHA256 []string `json:"blocked_idempotency_sha256"`
}

type Receipt struct {
	Version            int               `json:"version"`
	OperationID        string            `json:"operation_id"`
	SourceLane         string            `json:"source_lane"`
	TargetLane         string            `json:"target_lane"`
	SourceServiceRoot  string            `json:"source_service_root"`
	TargetServiceRoot  string            `json:"target_service_root"`
	SourceConfigSHA256 string            `json:"source_config_sha256"`
	TargetConfigSHA256 string            `json:"target_config_sha256"`
	Phase              string            `json:"phase"`
	Mailboxes          []Mailbox         `json:"mailboxes"`
	EnvironmentAliases map[string]string `json:"environment_aliases,omitempty"`
}

// Marker identifies an opened physical mailbox independently of its path.
// OwnerLane "none" records a transition; runtimes admit only active QAS owners.
type Marker struct {
	OperationID   string `json:"operation_id"`
	OwnerLane     string `json:"owner_lane"`
	ReceiptSHA256 string `json:"receipt_sha256"`
	Device        uint64 `json:"device"`
	Inode         uint64 `json:"inode"`
}

func ValidateReceipt(r Receipt) error {
	if r.Version != Version || !operationPattern.MatchString(r.OperationID) ||
		r.SourceLane != "prd" || r.TargetLane != "qas" ||
		!cleanAbsolute(r.SourceServiceRoot) || !cleanAbsolute(r.TargetServiceRoot) ||
		nested(r.SourceServiceRoot, r.TargetServiceRoot) || nested(r.TargetServiceRoot, r.SourceServiceRoot) ||
		!digestPattern.MatchString(r.SourceConfigSHA256) || !digestPattern.MatchString(r.TargetConfigSHA256) ||
		!namePattern.MatchString(r.Phase) || len(r.Mailboxes) == 0 || len(r.Mailboxes) > 128 {
		return ErrInvalid
	}
	ids, sourceIDs, roots := map[string]bool{}, map[string]bool{}, map[string]bool{}
	identities := map[[2]uint64]bool{}
	for _, m := range r.Mailboxes {
		if !namePattern.MatchString(m.SourceID) || !namePattern.MatchString(m.TargetID) ||
			!cleanAbsolute(m.Root) || m.Inode == 0 || !cleanRelative(m.ArchiveRelative) ||
			!allowedSourceRoot(r.SourceServiceRoot, m.SourceID, m.Root) || nested(r.TargetServiceRoot, m.Root) ||
			ids[m.TargetID] || sourceIDs[m.SourceID] || roots[m.Root] || identities[[2]uint64{m.Device, m.Inode}] {
			return ErrInvalid
		}
		for root := range roots {
			if nested(root, m.Root) || nested(m.Root, root) {
				return ErrInvalid
			}
		}
		ids[m.TargetID], sourceIDs[m.SourceID], roots[m.Root], identities[[2]uint64{m.Device, m.Inode}] = true, true, true, true
		seen := map[string]bool{}
		for _, id := range m.BlockedRequestIDs {
			if !namePattern.MatchString(id) || seen[id] {
				return ErrInvalid
			}
			seen[id] = true
		}
		seen = map[string]bool{}
		for _, digest := range m.BlockedIdempotencySHA256 {
			if !digestPattern.MatchString(digest) || seen[digest] {
				return ErrInvalid
			}
			seen[digest] = true
		}
	}
	for from, to := range r.EnvironmentAliases {
		if !namePattern.MatchString(from) || !namePattern.MatchString(to) {
			return ErrInvalid
		}
	}
	return nil
}

// Existing PRD lane-local inboxes can be handed off without moving their
// physical directories. Other paths inside that service root remain protected.
func allowedSourceRoot(serviceRoot, sourceID, root string) bool {
	if !nested(serviceRoot, root) {
		return true
	}
	if sourceID == "default" {
		return root == filepath.Join(serviceRoot, "mailbox")
	}
	return root == filepath.Join(serviceRoot, "mailboxes", sourceID)
}

// MarshalReceipt returns deterministic JSON. Markers bind the SHA-256 of these
// exact bytes (including the final newline), not of a decoded/re-encoded file.
func MarshalReceipt(r Receipt) ([]byte, error) {
	if err := ValidateReceipt(r); err != nil {
		return nil, err
	}
	raw, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return nil, ErrInvalid
	}
	return append(raw, '\n'), nil
}

func SHA256(raw []byte) string {
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func ReceiptPath(r Receipt) string {
	return filepath.Join(r.TargetServiceRoot, "staging", "mailbox-handoffs", r.OperationID+".json")
}

func cleanAbsolute(path string) bool {
	return filepath.IsAbs(path) && filepath.Clean(path) == path && path != string(filepath.Separator) && !strings.ContainsRune(path, 0)
}

func cleanRelative(path string) bool {
	return path != "" && path != "." && !filepath.IsAbs(path) && filepath.Clean(path) == path && path != ".." && !strings.HasPrefix(path, ".."+string(filepath.Separator)) && !strings.ContainsRune(path, 0)
}

func nested(parent, child string) bool {
	rel, err := filepath.Rel(parent, child)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
