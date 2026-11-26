package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"flag"
	"testing"
	"time"

	"remote-session-runner/src/internal/config"
	"remote-session-runner/src/internal/domain"
	"remote-session-runner/src/internal/store"
)

func TestOperatorArguments(t *testing.T) {
	cases := []struct {
		name          string
		args          []string
		called, apply bool
	}{
		{"default is an inspection", nil, true, false},
		{"explicit maintenance mode", []string{"--apply", "--publishers-stopped"}, true, true},
		{"apply without writer quiescence", []string{"--apply"}, false, false},
		{"quiescence alone", []string{"--publishers-stopped"}, false, false},
		{"arbitrary path", []string{"--root", "/somewhere"}, false, false},
		{"another request", []string{"req-unrelated"}, false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var out bytes.Buffer
			calls := 0
			err := run(context.Background(), tc.args, &out, func(_ context.Context, apply bool) (string, error) {
				calls++
				if apply != tc.apply {
					t.Errorf("apply = %t, want %t", apply, tc.apply)
				}
				return "ready", nil
			})
			if (err == nil) != tc.called || (calls == 1) != tc.called {
				t.Fatalf("calls=%d, error=%v", calls, err)
			}
			if tc.called {
				var receipt struct {
					ID    string `json:"request_id"`
					State string `json:"state"`
				}
				if err := json.Unmarshal(out.Bytes(), &receipt); err != nil || receipt.ID != requestID || receipt.State != "ready" {
					t.Fatalf("unexpected operator response %q, error %v", out.String(), err)
				}
			}
		})
	}
	t.Run("help never contacts authority", func(t *testing.T) {
		var out bytes.Buffer
		if err := run(context.Background(), []string{"--help"}, &out, nil); !errors.Is(err, flag.ErrHelp) {
			t.Fatalf("help: %v", err)
		}
	})
	t.Run("guard refusal produces no receipt", func(t *testing.T) {
		var out bytes.Buffer
		refusal := errors.New("service is running")
		err := run(context.Background(), []string{"--apply", "--publishers-stopped"}, &out, func(context.Context, bool) (string, error) { return "", refusal })
		if !errors.Is(err, refusal) || out.Len() != 0 {
			t.Fatalf("output=%q error=%v", out.String(), err)
		}
	})
}

func originalExchange(t *testing.T) store.MailboxExchangeRecord {
	t.Helper()
	at := time.Unix(1, 0).UTC()
	cursor := int64(7)
	owner, err := domain.NewControllerIdentity(domain.ControllerTypeLocalUser, config.MacAccount)
	if err != nil {
		t.Fatal(err)
	}
	target, err := domain.NewExecutionTarget(domain.TargetKindLocal, "mac-workstation")
	if err != nil {
		t.Fatal(err)
	}
	wire := map[string]any{
		"inbox_id": mailboxID, "request_id": requestID, "operation": "run", "request_state": "complete", "response_revision": 2,
		"job_id": jobID, "command_id": commandID, "session_id": sessionID, "available_event_sequence": 7, "job_phase": "complete",
		"command_state": "succeeded", "exit_code": 0, "teardown_outcome": "closed",
	}
	b, err := json.Marshal(wire)
	if err != nil {
		t.Fatal(err)
	}
	hash := sha256.Sum256(b)
	return store.MailboxExchangeRecord{
		MailboxID: mailboxID, RequestID: requestID, Operation: "run", Controller: owner, State: store.MailboxExchangeComplete, AcknowledgedAt: &at,
		ResponseRevision: 2, AvailableEventSequence: &cursor, EventFileCommandID: commandID, CanonicalPayload: []byte(`{"script":"original"}`),
		Selection:      &store.MailboxExecutionSelection{ContextName: "mac-local", Environment: "mac-dev", Target: target},
		ResponseSHA256: hash[:], TerminalResponseSHA256: hash[:], ResponseBytes: b, TerminalResponseBytes: bytes.Clone(b),
	}
}

func TestRetirementRequiresPriorAcknowledgedSuccess(t *testing.T) {
	original := originalExchange(t)
	response := digest(original.ResponseBytes)
	replacement := digest([]byte("replacement"))
	if err := verifyExchange(original, response, replacement); err != nil {
		t.Fatalf("valid original: %v", err)
	}
	tests := []struct {
		name   string
		change func(*store.MailboxExchangeRecord)
	}{
		{"other mailbox", func(e *store.MailboxExchangeRecord) { e.MailboxID = "other" }},
		{"other ID", func(e *store.MailboxExchangeRecord) { e.RequestID = "other" }},
		{"unacknowledged", func(e *store.MailboxExchangeRecord) { e.AcknowledgedAt = nil }},
		{"nonterminal", func(e *store.MailboxExchangeRecord) { e.State = store.MailboxExchangeAccepted }},
		{"wrong cursor", func(e *store.MailboxExchangeRecord) { e.AvailableEventSequence = nil }},
		{"other command", func(e *store.MailboxExchangeRecord) { e.EventFileCommandID = "cmd-other" }},
		{"other target", func(e *store.MailboxExchangeRecord) { e.Selection = nil }},
		{"same script", func(e *store.MailboxExchangeRecord) { e.CanonicalPayload = []byte(`{"script":"replacement"}`) }},
		{"missing old script", func(e *store.MailboxExchangeRecord) { e.CanonicalPayload = nil }},
		{"altered terminal bytes", func(e *store.MailboxExchangeRecord) { e.TerminalResponseBytes = []byte("modified") }},
		{"wrong authority checksum", func(e *store.MailboxExchangeRecord) { e.ResponseSHA256 = nil }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			e := originalExchange(t)
			tc.change(&e)
			if err := verifyExchange(e, response, replacement); err == nil {
				t.Fatal("wrong authority accepted")
			}
		})
	}
}
