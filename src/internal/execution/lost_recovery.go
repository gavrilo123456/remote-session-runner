package execution

import (
	"context"
	"errors"
	"fmt"

	"remote-session-runner/src/internal/domain"
	"remote-session-runner/src/internal/store"
)

var (
	// ErrLostRuntimeRecoveryIneligible means the requested session and command
	// do not describe one isolated lost runtime whose capacity may be repaired.
	ErrLostRuntimeRecoveryIneligible = errors.New("lost runtime recovery is not eligible")
	// ErrLostRuntimeRecoveryUnconfirmed means the runtime adapter could not
	// prove that the recorded process group is gone while capacity remains held.
	ErrLostRuntimeRecoveryUnconfirmed = errors.New("lost runtime cleanup remains unconfirmed")
	// ErrLostRuntimeRecoveryFinalization means paired capacity release is
	// durable, but its retained ownership marker and workspace could not yet be
	// finalized. The same explicit repair may retry finalization later; normal
	// dispatch may proceed because the paired capacity release already committed.
	ErrLostRuntimeRecoveryFinalization = errors.New("lost runtime recovery finalization remains unconfirmed")
)

// LostRuntimeRecoveryRequest identifies one explicitly selected lost command
// and its session. It is an operator repair boundary: it never executes the
// command script and never changes the truthful lost result.
type LostRuntimeRecoveryRequest struct {
	SessionID domain.SessionID
	CommandID domain.CommandID
}

// LostRuntimeRecoveryResult records the authoritative state before or after a
// repair. A successful repair releases only capacity that follows a confirmed
// runtime reconciliation.
type LostRuntimeRecoveryResult struct {
	Session            store.SessionRecord
	Command            store.LostRuntimeRecoveryCommand
	SessionReservation store.SessionReservation
	CommandSlot        store.CommandSlotRecord
	Runtime            RuntimeReconcileResult
	AlreadyRecovered   bool
}

// CheckLostRuntimeRecoveryBatch validates a complete explicit repair set
// without inspecting process groups, changing capacity, or executing work.
// It is intended for the offline runnerd recovery preflight.
func (s *Service) CheckLostRuntimeRecoveryBatch(ctx context.Context, requests []LostRuntimeRecoveryRequest) ([]LostRuntimeRecoveryResult, error) {
	if s == nil || s.store == nil || s.runtime == nil {
		return nil, ErrExecutionServiceConfiguration
	}
	s.mutationMu.Lock()
	defer s.mutationMu.Unlock()
	results, _, err := s.checkLostRuntimeRecoveryBatch(ctx, requests)
	return results, err
}

// RecoverLostRuntimeBatch proves and releases a complete, explicit set of
// lost runtimes. It is an offline operator repair boundary: it never starts a
// session, claims a command, or sources stored script bytes. Every selected
// process boundary is proven before the paired capacity records are released
// together in one authority transaction.
func (s *Service) RecoverLostRuntimeBatch(ctx context.Context, requests []LostRuntimeRecoveryRequest) ([]LostRuntimeRecoveryResult, error) {
	if s == nil || s.store == nil || s.runtime == nil {
		return nil, ErrExecutionServiceConfiguration
	}
	s.mutationMu.Lock()
	defer s.mutationMu.Unlock()

	results, pairs, err := s.checkLostRuntimeRecoveryBatch(ctx, requests)
	if err != nil {
		return results, err
	}
	recoverer, ok := s.runtime.(LostRuntimeRecoverer)
	if !ok {
		return results, fmt.Errorf("%w: runtime does not support lost-runtime recovery", ErrLostRuntimeRecoveryIneligible)
	}
	for index := range results {
		if results[index].AlreadyRecovered {
			continue
		}
		runtimeResult, reconcileErr := recoverer.ReconcileLostRuntime(ctx, RuntimeReconcileRequest{Session: results[index].Session})
		results[index].Runtime = runtimeResult
		if reconcileErr != nil {
			s.store.RecordCleanupFailure()
			auditErr := s.recordRuntimeCleanupFailure(ctx, results[index].Session, string(requests[index].CommandID))
			return results, errors.Join(fmt.Errorf("%w: %v", ErrLostRuntimeRecoveryUnconfirmed, reconcileErr), auditErr)
		}
		if !runtimeResult.CleanupConfirmed {
			s.store.RecordCleanupFailure()
			auditErr := s.recordRuntimeCleanupFailure(ctx, results[index].Session, string(requests[index].CommandID))
			return results, errors.Join(ErrLostRuntimeRecoveryUnconfirmed, auditErr)
		}
	}
	if err := s.store.ConfirmLostRuntimeRecoveryBatch(ctx, pairs); err != nil {
		return results, fmt.Errorf("record recovered runtime capacity: %w", err)
	}
	var finalizationErrors []error
	for index := range results {
		finalized, finalizeErr := s.finalizeLostRuntimeRecovery(ctx, requests[index], results[index], recoverer)
		results[index] = finalized
		if finalizeErr != nil {
			finalizationErrors = append(finalizationErrors, finalizeErr)
		}
	}
	return results, errors.Join(finalizationErrors...)
}

// CheckLostRuntimeRecoveryBatchPreservingQueuedOneOffs validates the narrow
// online-repair shape that can retain ready one-off sessions with queued work
// only when the complete configured command capacity is retained by selected
// terminal-lost pairs. It neither reconciles a runtime nor changes any durable
// record. The existing CheckLostRuntimeRecoveryBatch remains the stricter
// offline operation.
func (s *Service) CheckLostRuntimeRecoveryBatchPreservingQueuedOneOffs(ctx context.Context, requests []LostRuntimeRecoveryRequest) ([]LostRuntimeRecoveryResult, error) {
	if s == nil || s.store == nil || s.runtime == nil {
		return nil, ErrExecutionServiceConfiguration
	}
	s.mutationMu.Lock()
	defer s.mutationMu.Unlock()
	results, _, err := s.checkLostRuntimeRecoveryBatchPreservingQueuedOneOffs(ctx, requests)
	return results, err
}

// RecoverLostRuntimeBatchPreservingQueuedOneOffs proves and atomically
// releases explicitly selected terminal-lost runtime capacity while leaving
// unrelated, already-queued one-off work untouched. It never claims a queued
// command, starts a session, executes stored script bytes, or changes the
// preserved job/session/command identities.
//
// The store confirms the same scoped shape in the release transaction after
// every runtime proof. This means an unsafe preflight cannot invoke runtime
// cleanup, while a concurrent durable change cannot turn a prior safe read
// into an unsafe paired release.
func (s *Service) RecoverLostRuntimeBatchPreservingQueuedOneOffs(ctx context.Context, requests []LostRuntimeRecoveryRequest) ([]LostRuntimeRecoveryResult, error) {
	if s == nil || s.store == nil || s.runtime == nil {
		return nil, ErrExecutionServiceConfiguration
	}
	s.mutationMu.Lock()
	defer s.mutationMu.Unlock()

	results, pairs, err := s.checkLostRuntimeRecoveryBatchPreservingQueuedOneOffs(ctx, requests)
	if err != nil {
		return results, err
	}
	recoverer, ok := s.runtime.(LostRuntimeRecoverer)
	if !ok {
		return results, fmt.Errorf("%w: runtime does not support lost-runtime recovery", ErrLostRuntimeRecoveryIneligible)
	}
	for index := range results {
		if results[index].AlreadyRecovered {
			continue
		}
		runtimeResult, reconcileErr := recoverer.ReconcileLostRuntime(ctx, RuntimeReconcileRequest{Session: results[index].Session})
		results[index].Runtime = runtimeResult
		if reconcileErr != nil {
			s.store.RecordCleanupFailure()
			auditErr := s.recordRuntimeCleanupFailure(ctx, results[index].Session, string(requests[index].CommandID))
			return results, errors.Join(fmt.Errorf("%w: %v", ErrLostRuntimeRecoveryUnconfirmed, reconcileErr), auditErr)
		}
		if !runtimeResult.CleanupConfirmed {
			s.store.RecordCleanupFailure()
			auditErr := s.recordRuntimeCleanupFailure(ctx, results[index].Session, string(requests[index].CommandID))
			return results, errors.Join(ErrLostRuntimeRecoveryUnconfirmed, auditErr)
		}
	}
	if err := s.store.ConfirmLostRuntimeRecoveryBatchPreservingQueuedOneOffs(ctx, pairs); err != nil {
		return results, fmt.Errorf("record queue-preserving recovered runtime capacity: %w", err)
	}
	var finalizationErrors []error
	for index := range results {
		finalized, finalizeErr := s.finalizeLostRuntimeRecovery(ctx, requests[index], results[index], recoverer)
		results[index] = finalized
		if finalizeErr != nil {
			finalizationErrors = append(finalizationErrors, finalizeErr)
		}
	}
	return results, errors.Join(finalizationErrors...)
}

// FinalizeReleasedLostRuntimeRecoveryBatch retries only the post-release
// ownership-marker and workspace finalization for already recovered terminal
// lost pairs. It cannot inspect or signal an old PID, release capacity, claim
// a queued command, or execute a stored script.
func (s *Service) FinalizeReleasedLostRuntimeRecoveryBatch(ctx context.Context, requests []LostRuntimeRecoveryRequest) ([]LostRuntimeRecoveryResult, error) {
	if s == nil || s.store == nil || s.runtime == nil {
		return nil, ErrExecutionServiceConfiguration
	}
	s.mutationMu.Lock()
	defer s.mutationMu.Unlock()
	return s.finalizeReleasedLostRuntimeRecoveryBatch(ctx, requests)
}

func (s *Service) finalizeReleasedLostRuntimeRecoveryBatch(ctx context.Context, requests []LostRuntimeRecoveryRequest) ([]LostRuntimeRecoveryResult, error) {
	if len(requests) == 0 {
		return nil, fmt.Errorf("%w: at least one released lost runtime is required", ErrLostRuntimeRecoveryIneligible)
	}
	recoverer, ok := s.runtime.(LostRuntimeRecoverer)
	if !ok {
		return nil, fmt.Errorf("%w: runtime does not support lost-runtime finalization", ErrLostRuntimeRecoveryIneligible)
	}
	results := make([]LostRuntimeRecoveryResult, 0, len(requests))
	seenSessions := make(map[domain.SessionID]struct{}, len(requests))
	seenCommands := make(map[domain.CommandID]struct{}, len(requests))
	for _, request := range requests {
		if request.SessionID == "" || request.CommandID == "" {
			return results, fmt.Errorf("%w: session and command IDs are required", ErrLostRuntimeRecoveryIneligible)
		}
		if _, exists := seenSessions[request.SessionID]; exists {
			return results, fmt.Errorf("%w: duplicate session %s", ErrLostRuntimeRecoveryIneligible, request.SessionID)
		}
		if _, exists := seenCommands[request.CommandID]; exists {
			return results, fmt.Errorf("%w: duplicate command %s", ErrLostRuntimeRecoveryIneligible, request.CommandID)
		}
		seenSessions[request.SessionID] = struct{}{}
		seenCommands[request.CommandID] = struct{}{}

		result, err := s.checkLostRuntimeRecoveryTarget(ctx, request)
		if err != nil {
			return results, err
		}
		if !result.AlreadyRecovered {
			return results, fmt.Errorf("%w: capacity release is not durable", ErrLostRuntimeRecoveryIneligible)
		}
		results = append(results, result)
	}
	var finalizationErrors []error
	for index, request := range requests {
		finalized, err := s.finalizeLostRuntimeRecovery(ctx, request, results[index], recoverer)
		results[index] = finalized
		if err != nil {
			finalizationErrors = append(finalizationErrors, err)
		}
	}
	return results, errors.Join(finalizationErrors...)
}

// finalizePendingLostRuntimeRecoveries is called while mutationMu is already
// held during startup reconciliation. It clears only durable post-release
// finalization work before host ownership is audited, so a crash between
// capacity release and marker removal cannot make the next runnerd start
// reject its own provably recovered marker.
func (s *Service) finalizePendingLostRuntimeRecoveries(ctx context.Context) error {
	pairs, err := s.store.ListPendingLostRuntimeRecoveryFinalizations(ctx)
	if err != nil {
		return err
	}
	sessions, err := s.store.ListPendingCommandlessLostRuntimeRecoveryFinalizations(ctx)
	if err != nil {
		return err
	}
	if len(pairs) == 0 && len(sessions) == 0 {
		return nil
	}
	var finalizationErrors []error
	if len(pairs) != 0 {
		requests := make([]LostRuntimeRecoveryRequest, 0, len(pairs))
		for _, pair := range pairs {
			requests = append(requests, LostRuntimeRecoveryRequest{SessionID: pair.SessionID, CommandID: pair.CommandID})
		}
		if _, err := s.finalizeReleasedLostRuntimeRecoveryBatch(ctx, requests); err != nil {
			finalizationErrors = append(finalizationErrors, err)
		}
	}
	if len(sessions) != 0 {
		recoverer, ok := s.runtime.(LostRuntimeRecoverer)
		if !ok {
			finalizationErrors = append(finalizationErrors, fmt.Errorf("%w: runtime does not support lost-runtime finalization", ErrLostRuntimeRecoveryIneligible))
		} else {
			for _, recovery := range sessions {
				_, err := s.finalizeCommandlessLostRuntimeRecovery(ctx, CommandlessLostRuntimeRecoveryResult{Recovery: recovery}, recoverer)
				if err != nil {
					finalizationErrors = append(finalizationErrors, err)
				}
			}
		}
	}
	return errors.Join(finalizationErrors...)
}

// CheckLostRuntimeRecovery verifies either one fully retained lost runtime or
// a previously released pair whose marker may still need finalization. It
// performs no runtime action and no durable write.
func (s *Service) CheckLostRuntimeRecovery(ctx context.Context, request LostRuntimeRecoveryRequest) (LostRuntimeRecoveryResult, error) {
	if s == nil || s.store == nil || s.runtime == nil {
		return LostRuntimeRecoveryResult{}, ErrExecutionServiceConfiguration
	}
	s.mutationMu.Lock()
	defer s.mutationMu.Unlock()
	return s.checkLostRuntimeRecovery(ctx, request)
}

// RecoverLostRuntime runs one explicit, no-replay recovery attempt for an
// already-lost command. It durably marks the proven process-group boundary
// before atomically releasing both capacity records, then finalizes the owner
// marker and workspace. Any failed or unconfirmed pre-release check leaves
// capacity retained.
func (s *Service) RecoverLostRuntime(ctx context.Context, request LostRuntimeRecoveryRequest) (LostRuntimeRecoveryResult, error) {
	if s == nil || s.store == nil || s.runtime == nil {
		return LostRuntimeRecoveryResult{}, ErrExecutionServiceConfiguration
	}
	s.mutationMu.Lock()
	defer s.mutationMu.Unlock()

	result, err := s.checkLostRuntimeRecovery(ctx, request)
	if err != nil {
		return result, err
	}
	recoverer, ok := s.runtime.(LostRuntimeRecoverer)
	if !ok {
		return result, fmt.Errorf("%w: runtime does not support lost-runtime recovery", ErrLostRuntimeRecoveryIneligible)
	}
	if result.AlreadyRecovered {
		return s.finalizeLostRuntimeRecovery(ctx, request, result, recoverer)
	}

	// The runtime writes a durable cleanup proof into the retained ownership
	// marker before this paired SQLite write. If storage rejects the write, a
	// retry recognizes that proof without touching a potentially reused PID.
	runtimeResult, reconcileErr := recoverer.ReconcileLostRuntime(ctx, RuntimeReconcileRequest{Session: result.Session})
	result.Runtime = runtimeResult
	if reconcileErr != nil {
		s.store.RecordCleanupFailure()
		auditErr := s.recordRuntimeCleanupFailure(ctx, result.Session, string(request.CommandID))
		return result, errors.Join(fmt.Errorf("%w: %v", ErrLostRuntimeRecoveryUnconfirmed, reconcileErr), auditErr)
	}
	if !runtimeResult.CleanupConfirmed {
		s.store.RecordCleanupFailure()
		auditErr := s.recordRuntimeCleanupFailure(ctx, result.Session, string(request.CommandID))
		return result, errors.Join(ErrLostRuntimeRecoveryUnconfirmed, auditErr)
	}
	if err := s.store.ConfirmLostRuntimeRecovery(ctx, request.SessionID, request.CommandID); err != nil {
		return result, fmt.Errorf("record recovered runtime capacity: %w", err)
	}
	return s.finalizeLostRuntimeRecovery(ctx, request, result, recoverer)
}

// finalizeLostRuntimeRecovery removes the retained ownership evidence only
// after the paired durable capacity release exists. A failure here leaves the
// retained ownership evidence for a later idempotent finalization retry;
// normal dispatch may proceed because capacity is already released. A retry
// never replays a script or writes capacity again.
func (s *Service) finalizeLostRuntimeRecovery(ctx context.Context, request LostRuntimeRecoveryRequest, result LostRuntimeRecoveryResult, recoverer LostRuntimeRecoverer) (LostRuntimeRecoveryResult, error) {
	finalized, finalizeErr := recoverer.FinalizeLostRuntime(ctx, RuntimeReconcileRequest{Session: result.Session})
	result.Runtime = finalized
	if finalizeErr != nil {
		s.store.RecordCleanupFailure()
		auditErr := s.recordRuntimeCleanupFailure(ctx, result.Session, string(request.CommandID))
		return result, errors.Join(fmt.Errorf("%w: %v", ErrLostRuntimeRecoveryFinalization, finalizeErr), auditErr)
	}
	if !finalized.CleanupConfirmed {
		s.store.RecordCleanupFailure()
		auditErr := s.recordRuntimeCleanupFailure(ctx, result.Session, string(request.CommandID))
		return result, errors.Join(ErrLostRuntimeRecoveryFinalization, auditErr)
	}
	if err := s.store.CompleteLostRuntimeRecoveryFinalization(ctx, store.LostRuntimeRecoveryPair{SessionID: request.SessionID, CommandID: request.CommandID}); err != nil {
		s.store.RecordCleanupFailure()
		auditErr := s.recordRuntimeCleanupFailure(ctx, result.Session, string(request.CommandID))
		return result, errors.Join(fmt.Errorf("%w: record finalization: %v", ErrLostRuntimeRecoveryFinalization, err), auditErr)
	}
	var err error
	result.CommandSlot, err = s.store.GetCommandSlot(ctx, request.CommandID)
	if err != nil {
		return result, fmt.Errorf("read released command slot: %w", err)
	}
	result.SessionReservation, err = s.store.GetSessionReservation(ctx, request.SessionID)
	if err != nil {
		return result, fmt.Errorf("read released session reservation: %w", err)
	}
	return result, nil
}

func (s *Service) checkLostRuntimeRecovery(ctx context.Context, request LostRuntimeRecoveryRequest) (LostRuntimeRecoveryResult, error) {
	result, err := s.checkLostRuntimeRecoveryTarget(ctx, request)
	if err != nil || result.AlreadyRecovered {
		return result, err
	}
	liveSlots, err := s.store.CountLiveCommandSlots(ctx)
	if err != nil {
		return result, err
	}
	liveReservations, err := s.store.CountLiveSessionReservations(ctx)
	if err != nil {
		return result, err
	}
	if liveSlots != 1 || liveReservations != 1 {
		return result, fmt.Errorf("%w: live command slots=%d live session reservations=%d, want one matching retained runtime", ErrLostRuntimeRecoveryIneligible, liveSlots, liveReservations)
	}
	return result, nil
}

func (s *Service) checkLostRuntimeRecoveryBatch(ctx context.Context, requests []LostRuntimeRecoveryRequest) ([]LostRuntimeRecoveryResult, []store.LostRuntimeRecoveryPair, error) {
	if len(requests) == 0 {
		return nil, nil, fmt.Errorf("%w: at least one lost runtime is required", ErrLostRuntimeRecoveryIneligible)
	}
	results := make([]LostRuntimeRecoveryResult, 0, len(requests))
	pairs := make([]store.LostRuntimeRecoveryPair, 0, len(requests))
	seenSessions := make(map[domain.SessionID]struct{}, len(requests))
	seenCommands := make(map[domain.CommandID]struct{}, len(requests))
	retained := 0
	for _, request := range requests {
		if request.SessionID == "" || request.CommandID == "" {
			return results, nil, fmt.Errorf("%w: session and command IDs are required", ErrLostRuntimeRecoveryIneligible)
		}
		if _, exists := seenSessions[request.SessionID]; exists {
			return results, nil, fmt.Errorf("%w: duplicate session %s", ErrLostRuntimeRecoveryIneligible, request.SessionID)
		}
		if _, exists := seenCommands[request.CommandID]; exists {
			return results, nil, fmt.Errorf("%w: duplicate command %s", ErrLostRuntimeRecoveryIneligible, request.CommandID)
		}
		seenSessions[request.SessionID] = struct{}{}
		seenCommands[request.CommandID] = struct{}{}
		result, err := s.checkLostRuntimeRecoveryTarget(ctx, request)
		if err != nil {
			return results, nil, err
		}
		if !result.AlreadyRecovered {
			retained++
		}
		results = append(results, result)
		pairs = append(pairs, store.LostRuntimeRecoveryPair{SessionID: request.SessionID, CommandID: request.CommandID})
	}
	if retained == 0 {
		return results, pairs, nil
	}
	liveSlots, err := s.store.CountLiveCommandSlots(ctx)
	if err != nil {
		return results, nil, err
	}
	liveReservations, err := s.store.CountLiveSessionReservations(ctx)
	if err != nil {
		return results, nil, err
	}
	if liveSlots != retained || liveReservations != retained {
		return results, nil, fmt.Errorf("%w: live command slots=%d live session reservations=%d, want %d selected retained runtimes", ErrLostRuntimeRecoveryIneligible, liveSlots, liveReservations, retained)
	}
	return results, pairs, nil
}

func (s *Service) checkLostRuntimeRecoveryBatchPreservingQueuedOneOffs(ctx context.Context, requests []LostRuntimeRecoveryRequest) ([]LostRuntimeRecoveryResult, []store.LostRuntimeRecoveryPair, error) {
	if len(requests) == 0 {
		return nil, nil, fmt.Errorf("%w: at least one lost runtime is required", ErrLostRuntimeRecoveryIneligible)
	}
	results := make([]LostRuntimeRecoveryResult, 0, len(requests))
	pairs := make([]store.LostRuntimeRecoveryPair, 0, len(requests))
	seenSessions := make(map[domain.SessionID]struct{}, len(requests))
	seenCommands := make(map[domain.CommandID]struct{}, len(requests))
	for _, request := range requests {
		if request.SessionID == "" || request.CommandID == "" {
			return results, nil, fmt.Errorf("%w: session and command IDs are required", ErrLostRuntimeRecoveryIneligible)
		}
		if _, exists := seenSessions[request.SessionID]; exists {
			return results, nil, fmt.Errorf("%w: duplicate session %s", ErrLostRuntimeRecoveryIneligible, request.SessionID)
		}
		if _, exists := seenCommands[request.CommandID]; exists {
			return results, nil, fmt.Errorf("%w: duplicate command %s", ErrLostRuntimeRecoveryIneligible, request.CommandID)
		}
		seenSessions[request.SessionID] = struct{}{}
		seenCommands[request.CommandID] = struct{}{}
		result, err := s.checkLostRuntimeRecoveryTarget(ctx, request)
		if err != nil {
			return results, nil, err
		}
		results = append(results, result)
		pairs = append(pairs, store.LostRuntimeRecoveryPair{SessionID: request.SessionID, CommandID: request.CommandID})
	}
	if err := s.store.CheckLostRuntimeRecoveryBatchPreservingQueuedOneOffs(ctx, pairs); err != nil {
		return results, nil, fmt.Errorf("check queue-preserving lost runtime recovery: %w", err)
	}
	return results, pairs, nil
}

func (s *Service) checkLostRuntimeRecoveryTarget(ctx context.Context, request LostRuntimeRecoveryRequest) (LostRuntimeRecoveryResult, error) {
	if request.SessionID == "" || request.CommandID == "" {
		return LostRuntimeRecoveryResult{}, fmt.Errorf("%w: session and command IDs are required", ErrLostRuntimeRecoveryIneligible)
	}
	result := LostRuntimeRecoveryResult{}
	var err error
	result.Session, err = s.store.GetSession(ctx, request.SessionID)
	if err != nil {
		return result, err
	}
	result.Command, err = s.store.GetLostRuntimeRecoveryCommand(ctx, request.CommandID)
	if err != nil {
		return result, err
	}
	if result.Command.SessionID != result.Session.SessionID {
		return result, fmt.Errorf("%w: command does not belong to session", ErrLostRuntimeRecoveryIneligible)
	}
	result.CommandSlot, err = s.store.GetCommandSlot(ctx, request.CommandID)
	if err != nil {
		return result, err
	}
	result.SessionReservation, err = s.store.GetSessionReservation(ctx, request.SessionID)
	if err != nil {
		return result, err
	}
	if result.Session.State != domain.SessionStateLost || result.Command.State != domain.CommandStateLost {
		return result, fmt.Errorf("%w: session=%s command=%s, want lost/lost", ErrLostRuntimeRecoveryIneligible, result.Session.State, result.Command.State)
	}
	commands, err := s.store.ListSessionCommandStates(ctx, request.SessionID)
	if err != nil {
		return result, err
	}
	for _, command := range commands {
		if command.CommandID != request.CommandID && !command.State.IsTerminal() {
			return result, fmt.Errorf("%w: session has nonterminal command %s", ErrLostRuntimeRecoveryIneligible, command.CommandID)
		}
	}
	commandReleased := result.CommandSlot.StopConfirmedAt != nil && result.CommandSlot.ReleasedAt != nil
	sessionReleased := result.SessionReservation.CleanupConfirmedAt != nil && result.SessionReservation.ReleasedAt != nil
	if commandReleased && sessionReleased {
		// Event retention may later remove the command_lost record. The paired
		// release itself is enough to resume only post-release finalization.
		result.AlreadyRecovered = true
		return result, nil
	}
	if result.CommandSlot.StopConfirmedAt != nil || result.CommandSlot.ReleasedAt != nil || result.SessionReservation.CleanupConfirmedAt != nil || result.SessionReservation.ReleasedAt != nil {
		return result, fmt.Errorf("%w: capacity has already been partially released", ErrLostRuntimeRecoveryIneligible)
	}
	if result.Command.OutputComplete || result.Command.FinalEventSequence == nil {
		return result, fmt.Errorf("%w: output_complete=%t final_event_present=%t", ErrLostRuntimeRecoveryIneligible, result.Command.OutputComplete, result.Command.FinalEventSequence != nil)
	}
	eventTail, err := s.store.GetCommandEventTail(ctx, request.CommandID)
	if err != nil {
		return result, err
	}
	if eventTail.Sequence != *result.Command.FinalEventSequence || eventTail.Type != "command_lost" {
		return result, fmt.Errorf("%w: command has no final command_lost event", ErrLostRuntimeRecoveryIneligible)
	}
	return result, nil
}
