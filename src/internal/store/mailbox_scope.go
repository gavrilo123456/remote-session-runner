package store

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"

	"remote-session-runner/src/internal/domain"
)

// DefaultMailboxID is the compatibility namespace for the original single
// mailbox root. New runtimes bind this value explicitly instead of relying on
// an unscoped database lookup.
const DefaultMailboxID = "default"

// MailboxExchangeRef identifies a mailbox-visible exchange without exposing
// the durable internal exchange ID. Client request IDs are only unique within
// their named mailbox.
type MailboxExchangeRef struct {
	MailboxID       string
	ClientRequestID string
}

// MailboxEventRef identifies one physical mailbox event projection. A command
// can be projected into more than one mailbox root, so the mailbox is part of
// its cleanup identity.
type MailboxEventRef struct {
	MailboxID string
	CommandID domain.CommandID
}

func NewMailboxExchangeRef(mailboxID, clientRequestID string) (MailboxExchangeRef, error) {
	if !validMailboxID(mailboxID) || validateMailboxRequestID(clientRequestID) != nil {
		return MailboxExchangeRef{}, ErrMailboxExchangeInvalid
	}
	return MailboxExchangeRef{MailboxID: mailboxID, ClientRequestID: clientRequestID}, nil
}

func validateMailboxExchangeRef(ref MailboxExchangeRef) (MailboxExchangeRef, error) {
	return NewMailboxExchangeRef(ref.MailboxID, ref.ClientRequestID)
}

func validateMailboxID(mailboxID string) error {
	if !validMailboxID(mailboxID) {
		return ErrMailboxExchangeInvalid
	}
	return nil
}

func validMailboxID(value string) bool {
	if len(value) == 0 || len(value) > 128 {
		return false
	}
	for index := 0; index < len(value); index++ {
		character := value[index]
		letter := character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z'
		digit := character >= '0' && character <= '9'
		if index == 0 {
			if !letter && !digit {
				return false
			}
			continue
		}
		if !letter && !digit && character != '.' && character != '_' && character != '-' {
			return false
		}
	}
	return true
}

// mailboxExchangeID is an injective, versioned internal identity. The input
// validator forbids NUL in both components, which makes the separator
// unambiguous. It is intentionally never returned to a mailbox client.
func mailboxExchangeID(ref MailboxExchangeRef) string {
	return "mbx-exchange-v1-" + hex.EncodeToString([]byte(ref.MailboxID+"\x00"+ref.ClientRequestID))
}

// mailboxExecutionIdempotencyKey scopes a client-supplied mutation key before
// it enters local intents, jobs, commands, or a remote bridge frame. An empty
// client key identifies a read and remains empty.
func mailboxExecutionIdempotencyKey(mailboxID, clientKey string) string {
	if clientKey == "" {
		return ""
	}
	digest := sha256.Sum256([]byte("remote-session-runner/mailbox-idempotency/v1\x00" + mailboxID + "\x00" + clientKey))
	return "mbx-idempotency-v1-" + hex.EncodeToString(digest[:])
}

func defaultMailboxExchangeRef(requestID string) (MailboxExchangeRef, error) {
	return NewMailboxExchangeRef(DefaultMailboxID, requestID)
}

func mailboxRefFromInput(input MailboxExchangeCreate, requireExplicitMailbox bool) (MailboxExchangeRef, MailboxExchangeCreate, error) {
	mailboxID := input.MailboxID
	if mailboxID == "" && !requireExplicitMailbox {
		mailboxID = DefaultMailboxID
	}
	ref, err := NewMailboxExchangeRef(mailboxID, input.RequestID)
	if err != nil {
		return MailboxExchangeRef{}, MailboxExchangeCreate{}, err
	}
	input.MailboxID = ref.MailboxID
	input.RequestID = ref.ClientRequestID
	return ref, input, nil
}

func mailboxRefWithInput(ref MailboxExchangeRef, input MailboxExchangeCreate) (MailboxExchangeCreate, error) {
	validated, err := validateMailboxExchangeRef(ref)
	if err != nil {
		return MailboxExchangeCreate{}, err
	}
	if (input.MailboxID != "" && input.MailboxID != validated.MailboxID) ||
		(input.RequestID != "" && input.RequestID != validated.ClientRequestID) {
		return MailboxExchangeCreate{}, fmt.Errorf("%w: mailbox reference differs from input", ErrMailboxExchangeInvalid)
	}
	input.MailboxID = validated.MailboxID
	input.RequestID = validated.ClientRequestID
	return input, nil
}

func validateExecutionIdempotencyKey(value string) error {
	if len(value) > 256 || strings.IndexByte(value, 0) >= 0 {
		return ErrMailboxExchangeInvalid
	}
	return nil
}
