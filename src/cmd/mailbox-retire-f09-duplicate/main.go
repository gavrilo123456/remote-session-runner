// Command mailbox-retire-f09-duplicate archives one exact conflicting input.
// It never stops services, edits authority records, or submits work.
package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"remote-session-runner/src/internal/config"
	"remote-session-runner/src/internal/domain"
	"remote-session-runner/src/internal/laneinstall"
	"remote-session-runner/src/internal/mailbox"
	"remote-session-runner/src/internal/mailboxretire"
	"remote-session-runner/src/internal/store"
)

const (
	mailboxID       = "gpu-services"
	requestID       = "req-gpu-t10-f09-merge-dev-20261126-terra01"
	jobID           = "job-120a3375cc86bc61b32fbe89125f1634"
	sessionID       = "sess-711fb6cab98984326a22a0576df54673"
	commandID       = "cmd-363f44aa877e76e25b4a959b3c32f981"
	responseSHA256  = "5bceb42a6650dbff812fe35605877c2105e0e967cf9eaeb13bb8df34afc25709"
	eventsSHA256    = "7179a02c40ce22ce8fc10ffd5632cb598c05a4dec51600062a60d3dc6f097909"
	newScriptSHA256 = "abc292d1f004f5b54c5906a573e156de9fb8178677e40cf47da3be323b96c33a"
	archiveDir      = "/Users/tomasz.walczuk/projects-runtime/runner-release-operations/maintenance/gpu-services-f09-duplicate-merge-20261126/retired-input"
)

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	if err := run(ctx, os.Args[1:], os.Stdout, operate); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return
		}
		fmt.Fprintln(os.Stderr, "mailbox-retire-f09-duplicate:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, output io.Writer, operator func(context.Context, bool) (string, error)) error {
	flags := flag.NewFlagSet("mailbox-retire-f09-duplicate", flag.ContinueOnError)
	flags.SetOutput(output)
	apply := flags.Bool("apply", false, "retire only the specified F09 input (default: inspect only)")
	quiesced := flags.Bool("publishers-stopped", false, "operator confirms direct writers and noncooperating publishers are stopped for the maintenance window")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 || *apply != *quiesced {
		return errors.New("use no arguments to inspect, or --apply --publishers-stopped after quiescing all direct writers")
	}
	state, err := operator(ctx, *apply)
	if err != nil {
		return err
	}
	return json.NewEncoder(output).Encode(struct {
		RequestID string `json:"request_id"`
		State     string `json:"state"`
	}{requestID, state})
}

func operate(ctx context.Context, apply bool) (string, error) {
	return laneinstall.WithF09DuplicateRetirementGuard(ctx, apply, func(databasePath, mailboxRoot string) (string, error) {
		database, err := store.OpenExistingCurrentReadOnly(ctx, databasePath)
		if err != nil {
			return "", fmt.Errorf("open original authority read-only: %w", err)
		}
		defer database.Close()
		authority, err := store.NewAuthorityStore(database)
		if err != nil {
			return "", err
		}
		if err := verifyAuthority(ctx, authority, responseSHA256, eventsSHA256); err != nil {
			return "", err
		}
		if err := ctx.Err(); err != nil {
			return "", err
		}
		state, err := mailboxretire.RetireF09(mailboxRoot, archiveDir, apply)
		if err != nil || !apply {
			return state, err
		}
		if err := verifyAuthority(ctx, authority, responseSHA256, eventsSHA256); err != nil {
			return "", fmt.Errorf("post-retirement verification failed; preserve the archive and keep the grant held: %w", err)
		}
		return state, nil
	})
}

func digest(data []byte) string {
	return fmt.Sprintf("%x", sha256.Sum256(data))
}

type authorityReader interface {
	GetMailboxExchangeInMailbox(context.Context, store.MailboxExchangeRef) (store.MailboxExchangeRecord, error)
	GetLocalIntentByIdempotency(context.Context, string, string, domain.ControllerIdentity) (store.LocalIntentRecord, error)
	GetCommandWithEvents(context.Context, domain.CommandID) (store.CommandRecord, []store.CommandEventRecord, error)
}

func verifyAuthority(ctx context.Context, authority authorityReader, responseDigest, eventDigest string) error {
	ref, err := store.NewMailboxExchangeRef(mailboxID, requestID)
	if err != nil {
		return err
	}
	record, err := authority.GetMailboxExchangeInMailbox(ctx, ref)
	if err != nil {
		return fmt.Errorf("retrieve the original exchange: %w", err)
	}
	if err := verifyExchange(record, responseDigest, newScriptSHA256); err != nil {
		return err
	}
	intent, err := authority.GetLocalIntentByIdempotency(ctx, "run", record.ExecutionIdempotencyKey, record.Controller)
	if err != nil {
		return fmt.Errorf("retrieve bound Mac execution intent: %w", err)
	}
	if record.ExecutionIdempotencyKey == "" || intent.IdempotencyKey != record.ExecutionIdempotencyKey ||
		intent.Controller != record.Controller || intent.Operation != "run" || intent.ResourceID != jobID ||
		intent.JobID != jobID || intent.CommandID != commandID || intent.SessionID != sessionID ||
		intent.Target != record.Selection.Target || intent.Environment != record.Selection.Environment ||
		(intent.DeliveryState != store.LocalIntentAccepted && intent.DeliveryState != store.LocalIntentReconciled) {
		return errors.New("original request is not bound to its completed Mac execution")
	}
	command, events, err := authority.GetCommandWithEvents(ctx, commandID)
	if err != nil {
		return fmt.Errorf("retrieve original command and events: %w", err)
	}
	if command.CommandID != commandID || command.SessionID != sessionID || command.State != domain.CommandStateSucceeded || command.ExitCode == nil || *command.ExitCode != 0 ||
		command.FinalEventSequence == nil || *command.FinalEventSequence != 7 || !command.OutputComplete || command.OutputTruncated ||
		len(events) != 7 || events[6].Type != "command_succeeded" {
		return errors.New("original command result or final event does not match the incident")
	}
	rendered, err := mailbox.RenderCommandEvents(command, events)
	if err != nil || digest(rendered) != eventDigest {
		return errors.New("original durable command events do not match the preserved event evidence")
	}
	return nil
}

func verifyExchange(e store.MailboxExchangeRecord, responseSHA, duplicateScriptSHA string) error {
	if e.MailboxID != mailboxID || e.RequestID != requestID || e.Operation != "run" ||
		e.State != store.MailboxExchangeComplete || e.AcknowledgedAt == nil || e.ResponseRevision != 2 ||
		e.AvailableEventSequence == nil || *e.AvailableEventSequence != 7 || e.EventFileCommandID != commandID ||
		e.Controller.Type() != domain.ControllerTypeLocalUser || string(e.Controller.ID()) != config.MacAccount {
		return errors.New("authority does not contain the acknowledged original F09 exchange")
	}
	if e.Selection == nil || e.Selection.ContextName != "mac-local" || e.Selection.Environment != "mac-dev" ||
		e.Selection.Target.Kind() != domain.TargetKindLocal || e.Selection.Target.Profile() != "mac-workstation" {
		return errors.New("original exchange is not bound to the expected Mac target")
	}
	var payload struct {
		Script string `json:"script"`
	}
	if json.Unmarshal(e.CanonicalPayload, &payload) != nil || payload.Script == "" || digest([]byte(payload.Script)) == duplicateScriptSHA {
		return errors.New("could not prove that the retained script differs from the published duplicate")
	}
	if digest(e.ResponseBytes) != responseSHA || hex.EncodeToString(e.ResponseSHA256) != responseSHA ||
		!bytes.Equal(e.ResponseBytes, e.TerminalResponseBytes) || hex.EncodeToString(e.TerminalResponseSHA256) != responseSHA {
		return errors.New("original response differs from the preserved terminal result")
	}
	var wire struct {
		MailboxID    string `json:"inbox_id"`
		RequestID    string `json:"request_id"`
		Operation    string `json:"operation"`
		State        string `json:"request_state"`
		Revision     int64  `json:"response_revision"`
		Cursor       int64  `json:"available_event_sequence"`
		JobID        string `json:"job_id"`
		SessionID    string `json:"session_id"`
		CommandID    string `json:"command_id"`
		Phase        string `json:"job_phase"`
		CommandState string `json:"command_state"`
		ExitCode     *int   `json:"exit_code"`
		Teardown     string `json:"teardown_outcome"`
	}
	if json.Unmarshal(e.ResponseBytes, &wire) != nil || wire.MailboxID != mailboxID || wire.RequestID != requestID ||
		wire.Operation != "run" || wire.State != "complete" || wire.Revision != 2 || wire.Cursor != 7 ||
		wire.JobID != jobID || wire.SessionID != sessionID || wire.CommandID != commandID ||
		wire.Phase != "complete" || wire.CommandState != "succeeded" || wire.ExitCode == nil || *wire.ExitCode != 0 || wire.Teardown != "closed" {
		return errors.New("original result does not prove the successful, closed F09 job")
	}
	return nil
}
