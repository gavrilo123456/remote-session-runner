package config

import (
	"path/filepath"
	"regexp"
)

var mailboxHandoffReceiptNamePattern = regexp.MustCompile(`^[0-9a-f]{32}\.json$`)

func macMailboxIngressEnabled(mac *macDocument) bool {
	return mac.MailboxIngressEnabled == nil || *mac.MailboxIngressEnabled
}

// A disabled ingress leaves execution policies and service paths intact. Only
// an explicit false permits an empty registry; omission keeps the legacy
// default-inbox requirement. Durable registry ownership is a runtime concern.
func validMacMailboxIngress(mac *macDocument, mailboxes map[string]mailboxDocument) bool {
	if mac == nil {
		return false
	}
	if !macMailboxIngressEnabled(mac) {
		return len(mailboxes) == 0
	}
	_, exists := mailboxes["default"]
	return exists
}

// Parsing validates only the canonical, lane-local reference. The runtime
// handoff guard must open and verify the receipt before using an external root.
func validMacMailboxHandoffReceipt(serviceRoot, receipt string) bool {
	return filepath.IsAbs(receipt) && filepath.Clean(receipt) == receipt &&
		filepath.Dir(receipt) == filepath.Join(serviceRoot, "staging", "mailbox-handoffs") &&
		mailboxHandoffReceiptNamePattern.MatchString(filepath.Base(receipt))
}
