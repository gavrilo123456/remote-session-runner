package localapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"time"

	"remote-session-runner/src/internal/audit"
	"remote-session-runner/src/internal/domain"
	"remote-session-runner/src/internal/mailbox"
	"remote-session-runner/src/internal/store"
)

type localOperationFailure struct {
	status  int
	code    string
	message string
}

// SessionController identifies the local owner used by the shared API and
// mailbox session operations.
func (s *Server) SessionController() domain.ControllerIdentity {
	if s == nil {
		return domain.ControllerIdentity{}
	}
	return s.owner
}

func localFailure(status int, code, message string) *localOperationFailure {
	return &localOperationFailure{status: status, code: code, message: sanitizeErrorString(message)}
}

// CreateSessionIntent uses the same acceptance logic as Unix-socket API
// ingress. The file-only client itself remains filesystem-only.
func (s *Server) CreateSessionIntent(ctx context.Context, request mailbox.Request) (mailbox.SessionIntent, error) {
	ctx = audit.WithIngress(ctx, audit.IngressMailbox)
	if request.ExecutionIdempotencyKey == "" {
		return mailbox.SessionIntent{}, &mailbox.SessionOperationError{Code: "invalid_request", Message: "mailbox create request is invalid"}
	}
	body, err := mailboxCreateSessionBody(request.RawJSON)
	if err != nil {
		return mailbox.SessionIntent{}, &mailbox.SessionOperationError{Code: "invalid_request", Message: "mailbox create request is invalid"}
	}
	acceptance, failure := s.acceptCreateSessionIntent(ctx, request.ExecutionIdempotencyKey, body)
	if failure != nil {
		return mailbox.SessionIntent{}, mailboxOperationError(failure)
	}
	return mailbox.SessionIntent{SessionID: acceptance.SessionID, DeliveryState: acceptance.KnownState.DeliveryState}, nil
}

// SubmitCommandIntent uses the same immutable target, owner, script, timeout,
// canonical-hash, and idempotency path as the Unix-socket API. It records only
// a local intent; Router delivery remains a separate authority boundary.
func (s *Server) SubmitCommandIntent(ctx context.Context, request mailbox.Request) (mailbox.CommandIntent, error) {
	ctx = audit.WithIngress(ctx, audit.IngressMailbox)
	var input struct {
		RequestID      string          `json:"request_id"`
		IdempotencyKey string          `json:"idempotency_key"`
		Operation      string          `json:"operation"`
		SessionID      string          `json:"session_id"`
		Script         *string         `json:"script"`
		TimeoutSeconds json.RawMessage `json:"timeout_seconds,omitempty"`
	}
	decoder := json.NewDecoder(bytes.NewReader(request.RawJSON))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil || input.Operation != "submit_command" || input.RequestID != request.RequestID || input.IdempotencyKey != request.IdempotencyKey || input.SessionID != request.SessionID || input.Script == nil || request.ExecutionIdempotencyKey == "" {
		return mailbox.CommandIntent{}, &mailbox.SessionOperationError{Code: "invalid_request", Message: "mailbox submit request is invalid"}
	}
	body, err := json.Marshal(struct {
		Script         *string         `json:"script"`
		TimeoutSeconds json.RawMessage `json:"timeout_seconds,omitempty"`
	}{Script: input.Script, TimeoutSeconds: input.TimeoutSeconds})
	if err != nil {
		return mailbox.CommandIntent{}, &mailbox.SessionOperationError{Code: "invalid_request", Message: "mailbox submit request is invalid"}
	}
	acceptance, failure := s.acceptSubmitCommandIntent(ctx, request.ExecutionIdempotencyKey, request.SessionID, body)
	if failure != nil {
		return mailbox.CommandIntent{}, mailboxOperationError(failure)
	}
	return mailbox.CommandIntent{
		CommandID: acceptance.CommandID, SessionID: acceptance.SessionID,
		DeliveryState: acceptance.KnownState.DeliveryState,
	}, nil
}

// CancelCommandIntent uses the same keyed local-intent acceptance path as the
// Unix-socket cancel route. It does not claim that the target has applied the
// cancellation request.
func (s *Server) CancelCommandIntent(ctx context.Context, request mailbox.Request) (mailbox.CommandIntent, error) {
	ctx = audit.WithIngress(ctx, audit.IngressMailbox)
	if request.Operation != "cancel_command" || request.CommandID == "" || request.IdempotencyKey == "" || request.ExecutionIdempotencyKey == "" {
		return mailbox.CommandIntent{}, &mailbox.SessionOperationError{Code: "invalid_request", Message: "mailbox cancel request is invalid"}
	}
	if ctx == nil {
		ctx = context.Background()
	}
	accepted, failure := s.acceptCancelCommandIntent(ctx, request.ExecutionIdempotencyKey, request.CommandID, "")
	if failure != nil {
		return mailbox.CommandIntent{}, mailboxOperationError(failure)
	}
	return mailbox.CommandIntent{
		CommandID: accepted.CommandID, SessionID: accepted.SessionID,
		DeliveryState: accepted.KnownState.DeliveryState,
	}, nil
}

// GetCancelCommandSnapshot reloads the exact keyed cancel intent and the
// accepted command view through the owner-scoped Mac API boundary.
func (s *Server) GetCancelCommandSnapshot(ctx context.Context, commandIDText, idempotencyKey string) (mailbox.CancelCommandSnapshot, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	cancelIntent, err := s.authority.GetLocalIntentByIdempotency(ctx, "cancel_command", idempotencyKey, s.owner)
	if err != nil {
		if errors.Is(err, store.ErrLocalIntentNotFound) {
			return mailbox.CancelCommandSnapshot{}, &mailbox.SessionOperationError{Code: "resource_not_found", Message: "cancel intent is not available through this Mac ingress"}
		}
		status, code := statusForStoreError(err)
		return mailbox.CancelCommandSnapshot{}, mailboxOperationError(localFailure(status, code, sanitizeError(err)))
	}
	if string(cancelIntent.CommandID) != commandIDText || cancelIntent.ResourceID != commandIDText {
		return mailbox.CancelCommandSnapshot{}, &mailbox.SessionOperationError{Code: "runtime_unavailable", Message: "cancel intent identity does not match its request", Retryable: true}
	}
	command, err := s.GetCommandSnapshot(ctx, commandIDText)
	if err != nil {
		return mailbox.CancelCommandSnapshot{}, err
	}
	if string(command.SessionID) != string(cancelIntent.SessionID) {
		return mailbox.CancelCommandSnapshot{}, &mailbox.SessionOperationError{Code: "runtime_unavailable", Message: "cancel intent session does not match the command", Retryable: true}
	}
	observedAt := cancelIntent.UpdatedAt.UTC()
	if command.ObservedAt.After(observedAt) {
		observedAt = command.ObservedAt.UTC()
	}
	return mailbox.CancelCommandSnapshot{
		CommandID: commandIDText, SessionID: string(cancelIntent.SessionID),
		CancelDeliveryState:  string(cancelIntent.DeliveryState),
		CommandDeliveryState: command.DeliveryState, CommandState: string(command.State), ObservedAt: observedAt,
	}, nil
}

// CloseSessionIntent uses the same keyed close-intent path and policy
// validation as the Unix-socket API. Omitted close_policy was normalized by
// the mailbox importer to the API's default "cancel" policy.
func (s *Server) CloseSessionIntent(ctx context.Context, request mailbox.Request) (mailbox.SessionIntent, error) {
	ctx = audit.WithIngress(ctx, audit.IngressMailbox)
	if request.Operation != "close_session" || request.SessionID == "" || request.IdempotencyKey == "" || request.ExecutionIdempotencyKey == "" {
		return mailbox.SessionIntent{}, &mailbox.SessionOperationError{Code: "invalid_request", Message: "mailbox close request is invalid"}
	}
	if ctx == nil {
		ctx = context.Background()
	}
	accepted, failure := s.acceptCloseSessionIntent(ctx, request.ExecutionIdempotencyKey, request.SessionID, request.ClosePolicy)
	if failure != nil {
		return mailbox.SessionIntent{}, mailboxOperationError(failure)
	}
	return mailbox.SessionIntent{SessionID: accepted.SessionID, DeliveryState: accepted.KnownState.DeliveryState}, nil
}

// RunJobIntent sends a file-only run through the same validator, canonical
// request hash, idempotency key, and durable local-intent path as /v1/jobs.
func (s *Server) RunJobIntent(ctx context.Context, request mailbox.Request) (mailbox.RunIntent, error) {
	ctx = audit.WithIngress(ctx, audit.IngressMailbox)
	if request.Operation != "run" || request.RequestID == "" || request.IdempotencyKey == "" || request.ExecutionIdempotencyKey == "" {
		return mailbox.RunIntent{}, &mailbox.SessionOperationError{Code: "invalid_request", Message: "mailbox run request is invalid"}
	}
	body, err := mailboxRunJobBody(request)
	if err != nil {
		return mailbox.RunIntent{}, &mailbox.SessionOperationError{Code: "invalid_request", Message: "mailbox run request is invalid"}
	}
	acceptance, failure := s.acceptCreateJobIntent(ctx, request.ExecutionIdempotencyKey, body)
	if failure != nil {
		return mailbox.RunIntent{}, mailboxOperationError(failure)
	}
	return mailbox.RunIntent{
		JobID: acceptance.JobID, SessionID: acceptance.SessionID,
		CommandID: acceptance.CommandID, DeliveryState: acceptance.KnownState.DeliveryState,
	}, nil
}

// GetRunSnapshot reads only a run intent owned by this Mac API controller,
// then joins its matching local job or queued-remote projection and command.
func (s *Server) GetRunSnapshot(ctx context.Context, jobIDText string) (mailbox.RunSnapshot, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	jobID, err := domain.NewJobID(jobIDText)
	if err != nil {
		return mailbox.RunSnapshot{}, &mailbox.SessionOperationError{Code: "invalid_request", Message: "job ID is invalid"}
	}
	intent, err := s.authority.GetLocalIntentByResource(ctx, "run", string(jobID), s.owner)
	if err != nil {
		if errors.Is(err, store.ErrLocalIntentNotFound) {
			return mailbox.RunSnapshot{}, &mailbox.SessionOperationError{Code: "resource_not_found", Message: "job is not available through this Mac ingress"}
		}
		status, code := statusForStoreError(err)
		return mailbox.RunSnapshot{}, mailboxOperationError(localFailure(status, code, sanitizeError(err)))
	}
	if intent.JobID != jobID || intent.ResourceID != string(jobID) || intent.SessionID == "" || intent.CommandID == "" || intent.Operation != "run" ||
		intent.Controller.Type() != s.owner.Type() || intent.Controller.ID() != s.owner.ID() {
		return mailbox.RunSnapshot{}, &mailbox.SessionOperationError{Code: "runtime_unavailable", Message: "run intent identity does not match its owner-scoped resource", Retryable: true}
	}
	snapshot := mailbox.RunSnapshot{
		JobID: string(intent.JobID), SessionID: string(intent.SessionID), CommandID: string(intent.CommandID),
		DeliveryState: string(intent.DeliveryState), ObservedAt: intent.UpdatedAt.UTC(),
	}
	if intent.DeliveryState == store.LocalIntentNotDelivered {
		return snapshot, nil
	}
	if remoteProjectionEligible(intent) {
		// A queued remote one-off is published to the mailbox only after the
		// dispatcher has read and cross-checked its job, command, and retained
		// output boundary. This prevents an initial acceptance projection (or a
		// stale row left across a Mac restart) from being rendered as terminal.
		if !store.HasRemoteTerminalProof(intent) {
			return snapshot, nil
		}
		projection, projectionErr := s.authority.GetRemoteJobProjection(ctx, jobID)
		if errors.Is(projectionErr, store.ErrRemoteProjectionNotFound) {
			return snapshot, nil
		}
		if projectionErr != nil {
			status, code := statusForStoreError(projectionErr)
			return mailbox.RunSnapshot{}, mailboxOperationError(localFailure(status, code, sanitizeError(projectionErr)))
		}
		if projection.JobID != intent.JobID || projection.SessionID != intent.SessionID || projection.CommandID != intent.CommandID ||
			projection.Target.Kind() != domain.TargetKindRemote || projection.Target.Profile() != intent.Target.Profile() ||
			projection.Controller.Type() != s.owner.Type() || projection.Controller.ID() != s.owner.ID() ||
			projection.Environment != intent.Environment || projection.Source != intent.Source {
			return mailbox.RunSnapshot{}, &mailbox.SessionOperationError{Code: "runtime_unavailable", Message: "remote job projection does not match its accepted intent", Retryable: true}
		}
		snapshot.JobPhase = string(projection.Phase)
		snapshot.TeardownOutcome = mailboxJobTeardownOutcome(projection.TeardownState)
		snapshot.ObservedAt = projection.ObservedAt.UTC()
		if projection.CommandState != nil {
			commandProjection, events, commandErr := s.authority.GetRemoteCommandWithEvents(ctx, projection.CommandID)
			if commandErr != nil {
				status, code := statusForStoreError(commandErr)
				if errors.Is(commandErr, store.ErrRemoteProjectionNotFound) {
					return mailbox.RunSnapshot{}, &mailbox.SessionOperationError{Code: "runtime_unavailable", Message: "remote run command snapshot is not available yet", Retryable: true}
				}
				return mailbox.RunSnapshot{}, mailboxOperationError(localFailure(status, code, sanitizeError(commandErr)))
			}
			if commandProjection.CommandID != intent.CommandID || commandProjection.SessionID != intent.SessionID ||
				commandProjection.Target.Kind() != domain.TargetKindRemote || commandProjection.Target.Profile() != intent.Target.Profile() ||
				commandProjection.Controller.Type() != s.owner.Type() || commandProjection.Controller.ID() != s.owner.ID() ||
				commandProjection.Environment != intent.Environment || commandProjection.Source != intent.Source ||
				commandProjection.State != *projection.CommandState ||
				!sameLocalInt(commandProjection.ExitCode, projection.ExitCode) ||
				!sameLocalInt64(commandProjection.FinalEventSequence, projection.FinalEventSequence) ||
				commandProjection.OutputComplete != projection.OutputComplete || commandProjection.OutputTruncated != projection.OutputTruncated ||
				commandProjection.OutputUnavailableReason != projection.OutputUnavailableReason {
				return mailbox.RunSnapshot{}, &mailbox.SessionOperationError{Code: "runtime_unavailable", Message: "remote run command does not match its job projection", Retryable: true}
			}
			command, snapshotErr := mailbox.SnapshotRemoteCommand(commandProjection, events, commandProjection.ObservedAt.UTC())
			if snapshotErr != nil {
				return mailbox.RunSnapshot{}, &mailbox.SessionOperationError{Code: "runtime_unavailable", Message: "remote run command snapshot is temporarily unavailable", Retryable: true}
			}
			command.DeliveryState = string(intent.DeliveryState)
			snapshot.Command = &command
		}
		return snapshot, nil
	}
	if intent.Target.Kind() != domain.TargetKindLocal || (intent.DeliveryState != store.LocalIntentAccepted && intent.DeliveryState != store.LocalIntentReconciled) {
		return snapshot, nil
	}
	job, err := s.authority.GetJob(ctx, jobID)
	if errors.Is(err, store.ErrJobNotFound) {
		return snapshot, nil
	}
	if err != nil {
		status, code := statusForStoreError(err)
		return mailbox.RunSnapshot{}, mailboxOperationError(localFailure(status, code, sanitizeError(err)))
	}
	if job.JobID != intent.JobID || job.SessionID != intent.SessionID || job.CommandID != intent.CommandID ||
		job.Controller.Type() != s.owner.Type() || job.Controller.ID() != s.owner.ID() ||
		job.Target != intent.Target || job.Environment != intent.Environment || job.Source != intent.Source ||
		domain.CompareIdempotency(job.RequestHash, intent.RequestHash) != domain.IdempotencySamePayload ||
		!bytes.Equal(job.CanonicalPayload, intent.PayloadJSON) || !bytes.Equal(job.ScriptBytes, intent.ScriptBytes) {
		return mailbox.RunSnapshot{}, &mailbox.SessionOperationError{Code: "runtime_unavailable", Message: "local job authority does not match its accepted intent", Retryable: true}
	}
	snapshot.JobPhase = string(job.Phase)
	snapshot.TeardownOutcome = mailboxJobTeardownOutcome(job.TeardownState)
	snapshot.ObservedAt = job.UpdatedAt.UTC()
	if job.CommandState != nil {
		command, events, commandErr := s.authority.GetCommandWithEvents(ctx, intent.CommandID)
		if commandErr != nil {
			if errors.Is(commandErr, store.ErrCommandNotFound) {
				return mailbox.RunSnapshot{}, &mailbox.SessionOperationError{Code: "runtime_unavailable", Message: "local run command snapshot is not available yet", Retryable: true}
			}
			status, code := statusForStoreError(commandErr)
			return mailbox.RunSnapshot{}, mailboxOperationError(localFailure(status, code, sanitizeError(commandErr)))
		}
		if command.CommandID != intent.CommandID || command.SessionID != intent.SessionID || command.State != *job.CommandState ||
			!bytes.Equal(command.ScriptBytes, intent.ScriptBytes) || !sameLocalInt(command.ExitCode, job.ExitCode) ||
			!sameLocalInt64(command.FinalEventSequence, job.FinalEventSequence) || command.OutputComplete != job.OutputComplete ||
			command.OutputTruncated != job.OutputTruncated {
			return mailbox.RunSnapshot{}, &mailbox.SessionOperationError{Code: "runtime_unavailable", Message: "local run command does not match its job authority", Retryable: true}
		}
		commandSnapshot, snapshotErr := mailbox.SnapshotCommand(command, events, command.UpdatedAt.UTC())
		if snapshotErr != nil {
			return mailbox.RunSnapshot{}, &mailbox.SessionOperationError{Code: "runtime_unavailable", Message: "local run command snapshot is temporarily unavailable", Retryable: true}
		}
		commandSnapshot.DeliveryState = string(intent.DeliveryState)
		snapshot.Command = &commandSnapshot
	}
	return snapshot, nil
}

func mailboxRunJobBody(request mailbox.Request) ([]byte, error) {
	var input struct {
		RequestID       string          `json:"request_id"`
		IdempotencyKey  string          `json:"idempotency_key"`
		Operation       string          `json:"operation"`
		Environment     string          `json:"environment"`
		ExecutionTarget targetRequest   `json:"execution_target"`
		Source          *sourceRequest  `json:"source,omitempty"`
		Script          *string         `json:"script"`
		TimeoutSeconds  json.RawMessage `json:"timeout_seconds,omitempty"`
		Limits          json.RawMessage `json:"limits,omitempty"`
		Policy          json.RawMessage `json:"policy,omitempty"`
	}
	decoder := json.NewDecoder(bytes.NewReader(request.RawJSON))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		return nil, err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF || input.RequestID != request.RequestID || input.IdempotencyKey != request.IdempotencyKey ||
		input.Operation != "run" || input.Environment != request.Environment || input.Script == nil || *input.Script != request.Script {
		return nil, ErrConfiguration
	}
	body, err := json.Marshal(createJobRequest{
		Environment: input.Environment, ExecutionTarget: input.ExecutionTarget, Source: input.Source,
		Script: input.Script, TimeoutSeconds: input.TimeoutSeconds, Limits: input.Limits, Policy: input.Policy,
	})
	if err != nil {
		return nil, err
	}
	return body, nil
}

func mailboxJobTeardownOutcome(state store.JobTeardownState) string {
	switch state {
	case store.JobTeardownClosed:
		return "closed"
	case store.JobTeardownNotCreated:
		return "not_created"
	case store.JobTeardownFailed, store.JobTeardownLost:
		return "lost"
	default:
		return ""
	}
}

func sameLocalInt(left, right *int) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

func sameLocalInt64(left, right *int64) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

// GetCloseSessionSnapshot reloads the exact keyed close intent separately
// from the session's original create delivery and authoritative lifecycle.
func (s *Server) GetCloseSessionSnapshot(ctx context.Context, sessionIDText, idempotencyKey string) (mailbox.CloseSessionSnapshot, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	closeIntent, err := s.authority.GetLocalIntentByIdempotency(ctx, "close_session", idempotencyKey, s.owner)
	if err != nil {
		if errors.Is(err, store.ErrLocalIntentNotFound) {
			return mailbox.CloseSessionSnapshot{}, &mailbox.SessionOperationError{Code: "resource_not_found", Message: "close intent is not available through this Mac ingress"}
		}
		status, code := statusForStoreError(err)
		return mailbox.CloseSessionSnapshot{}, mailboxOperationError(localFailure(status, code, sanitizeError(err)))
	}
	if string(closeIntent.SessionID) != sessionIDText || closeIntent.ResourceID != sessionIDText {
		return mailbox.CloseSessionSnapshot{}, &mailbox.SessionOperationError{Code: "runtime_unavailable", Message: "close intent identity does not match its request", Retryable: true}
	}
	session, err := s.GetSession(ctx, sessionIDText)
	if err != nil {
		return mailbox.CloseSessionSnapshot{}, err
	}
	if session.SessionID != sessionIDText {
		return mailbox.CloseSessionSnapshot{}, &mailbox.SessionOperationError{Code: "runtime_unavailable", Message: "close intent session does not match its snapshot", Retryable: true}
	}
	observedAt := closeIntent.UpdatedAt.UTC()
	if session.ObservedAt.After(observedAt) {
		observedAt = session.ObservedAt.UTC()
	}
	return mailbox.CloseSessionSnapshot{
		SessionID: sessionIDText, CloseDeliveryState: string(closeIntent.DeliveryState),
		SessionDeliveryState: session.DeliveryState, SessionState: session.SessionState, ObservedAt: observedAt,
	}, nil
}

func mailboxCreateSessionBody(raw []byte) ([]byte, error) {
	var input struct {
		Environment     json.RawMessage `json:"environment"`
		ExecutionTarget json.RawMessage `json:"execution_target"`
		Source          json.RawMessage `json:"source,omitempty"`
		Limits          json.RawMessage `json:"limits,omitempty"`
		Policy          json.RawMessage `json:"policy,omitempty"`
	}
	if err := json.Unmarshal(raw, &input); err != nil || len(input.Environment) == 0 || len(input.ExecutionTarget) == 0 {
		return nil, ErrConfiguration
	}
	fields := map[string]json.RawMessage{
		"environment": input.Environment, "execution_target": input.ExecutionTarget,
	}
	if len(input.Source) > 0 {
		fields["source"] = input.Source
	}
	if len(input.Limits) > 0 {
		fields["limits"] = input.Limits
	}
	if len(input.Policy) > 0 {
		fields["policy"] = input.Policy
	}
	return json.Marshal(fields)
}

// GetSession uses the same owner-scoped session read logic as Unix-socket API
// ingress, without creating a socket client or network request.
func (s *Server) GetSession(ctx context.Context, sessionID string) (mailbox.SessionSnapshot, error) {
	read, failure := s.readSession(ctx, sessionID)
	if failure != nil {
		return mailbox.SessionSnapshot{}, mailboxOperationError(failure)
	}
	var snapshot mailbox.SessionSnapshot
	switch read.View {
	case "local_intent":
		resource, ok := read.Resource.(sessionIntentResource)
		if !ok {
			return mailbox.SessionSnapshot{}, &mailbox.SessionOperationError{Code: "runtime_unavailable", Message: "local session intent snapshot is invalid", Retryable: true}
		}
		snapshot.SessionID, snapshot.DeliveryState, snapshot.ObservedAt = resource.SessionID, resource.DeliveryState, resource.ObservedAt
	case "projection", "authority":
		resource, ok := read.Resource.(sessionProjectionResource)
		if !ok {
			return mailbox.SessionSnapshot{}, &mailbox.SessionOperationError{Code: "runtime_unavailable", Message: "session authority snapshot is invalid", Retryable: true}
		}
		snapshot.SessionID, snapshot.SessionState, snapshot.ObservedAt = resource.SessionID, resource.SessionState, resource.ObservedAt
	case "":
		return mailbox.SessionSnapshot{}, &mailbox.SessionOperationError{Code: "runtime_unavailable", Message: "local session API returned no view", Retryable: true}
	default:
		return mailbox.SessionSnapshot{}, &mailbox.SessionOperationError{Code: "runtime_unavailable", Message: "local session API returned an unsupported view", Retryable: true}
	}
	return snapshot, nil
}

// GetCommandSnapshot returns only a command intent owned by the local Mac
// controller, its local authority snapshot, or its queued-remote projection.
// Direct-created Linux resources have no local intent and remain inaccessible.
func (s *Server) GetCommandSnapshot(ctx context.Context, commandIDText string) (mailbox.CommandSnapshot, error) {
	commandID, err := domain.NewCommandID(commandIDText)
	if err != nil {
		return mailbox.CommandSnapshot{}, &mailbox.SessionOperationError{Code: "invalid_request", Message: "command ID is invalid"}
	}
	intent, err := s.authority.GetLocalIntentByResource(ctx, "submit_command", string(commandID), s.owner)
	if err != nil {
		if errors.Is(err, store.ErrLocalIntentNotFound) {
			return mailbox.CommandSnapshot{}, &mailbox.SessionOperationError{Code: "resource_not_found", Message: "command is not available through this Mac ingress"}
		}
		status, code := statusForStoreError(err)
		return mailbox.CommandSnapshot{}, mailboxOperationError(localFailure(status, code, sanitizeError(err)))
	}
	base := mailbox.CommandSnapshot{
		CommandID: intent.CommandID, SessionID: intent.SessionID,
		DeliveryState: string(intent.DeliveryState), ObservedAt: intent.UpdatedAt.UTC(),
	}
	if intent.DeliveryState == store.LocalIntentNotDelivered {
		return base, nil
	}
	if remoteProjectionEligible(intent) {
		projection, events, projectionErr := s.authority.GetRemoteCommandWithEvents(ctx, commandID)
		if errors.Is(projectionErr, store.ErrRemoteProjectionNotFound) {
			return base, nil
		}
		if projectionErr != nil {
			status, code := statusForStoreError(projectionErr)
			return mailbox.CommandSnapshot{}, mailboxOperationError(localFailure(status, code, sanitizeError(projectionErr)))
		}
		if projection.CommandID != commandID || projection.SessionID != intent.SessionID || projection.Target.Kind() != domain.TargetKindRemote || projection.Target.Profile() != intent.Target.Profile() ||
			projection.Controller.Type() != s.owner.Type() || projection.Controller.ID() != s.owner.ID() || projection.Environment != intent.Environment {
			return mailbox.CommandSnapshot{}, &mailbox.SessionOperationError{Code: "runtime_unavailable", Message: "remote command projection does not match its accepted intent", Retryable: true}
		}
		// Mutation replies can be intentionally sparse-compatible. A terminal
		// remote projection therefore becomes mailbox-visible only after the
		// Router has completed its authoritative reconciliation; otherwise an
		// accepted mutation reply could be mistaken for terminal proof.
		if projection.State.IsTerminal() && !store.HasRemoteTerminalProof(intent) {
			return base, nil
		}
		snapshot, err := mailbox.SnapshotRemoteCommand(projection, events, time.Now().UTC())
		if err != nil {
			return mailbox.CommandSnapshot{}, &mailbox.SessionOperationError{Code: "runtime_unavailable", Message: "remote command snapshot is temporarily unavailable", Retryable: true}
		}
		snapshot.DeliveryState = string(intent.DeliveryState)
		return snapshot, nil
	}
	if intent.Target.Kind() != domain.TargetKindLocal || (intent.DeliveryState != store.LocalIntentAccepted && intent.DeliveryState != store.LocalIntentReconciled) {
		return base, nil
	}
	command, events, err := s.authority.GetCommandWithEvents(ctx, commandID)
	if err != nil {
		status, code := statusForStoreError(err)
		return mailbox.CommandSnapshot{}, mailboxOperationError(localFailure(status, code, sanitizeError(err)))
	}
	session, err := s.authority.GetSession(ctx, command.SessionID)
	if err != nil {
		status, code := statusForStoreError(err)
		return mailbox.CommandSnapshot{}, mailboxOperationError(localFailure(status, code, sanitizeError(err)))
	}
	if command.CommandID != intent.CommandID || command.SessionID != intent.SessionID || session.SessionID != intent.SessionID ||
		session.Target.Kind() != domain.TargetKindLocal || session.Target.Profile() != intent.Target.Profile() ||
		session.Controller.Type() != s.owner.Type() || session.Controller.ID() != s.owner.ID() ||
		domain.CompareIdempotency(command.RequestHash, intent.RequestHash) != domain.IdempotencySamePayload || !bytes.Equal(command.ScriptBytes, intent.ScriptBytes) {
		return mailbox.CommandSnapshot{}, &mailbox.SessionOperationError{Code: "runtime_unavailable", Message: "local command authority does not match its accepted intent", Retryable: true}
	}
	if intent.IntentOrdinal != nil && (command.IntentOrdinal == nil || *command.IntentOrdinal != *intent.IntentOrdinal) {
		return mailbox.CommandSnapshot{}, &mailbox.SessionOperationError{Code: "runtime_unavailable", Message: "local command ordinal does not match its accepted intent", Retryable: true}
	}
	snapshot, err := mailbox.SnapshotCommand(command, events, time.Now().UTC())
	if err != nil {
		return mailbox.CommandSnapshot{}, &mailbox.SessionOperationError{Code: "runtime_unavailable", Message: "local command snapshot is temporarily unavailable", Retryable: true}
	}
	snapshot.DeliveryState = string(intent.DeliveryState)
	return snapshot, nil
}

func mailboxOperationError(failure *localOperationFailure) *mailbox.SessionOperationError {
	code := failure.code
	switch code {
	case "session_not_found":
		code = "resource_not_found"
	case "command_not_found":
		code = "resource_not_found"
	case "invalid_script":
		code = "invalid_request"
	case "database_unavailable":
		code = "runtime_unavailable"
	}
	return &mailbox.SessionOperationError{Code: code, Message: failure.message, Retryable: failure.status >= 500}
}
