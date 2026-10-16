package runnerlocald

import (
	"context"
	"errors"
	"fmt"
	"io"

	"remote-session-runner/src/internal/execution"
	"remote-session-runner/src/internal/queueworker"
	"remote-session-runner/src/internal/store"
)

func parseMacCompletedTeardownRecoveryPairs(values []string) ([]execution.CompletedTeardownRecoveryRequest, error) {
	pairs, err := parseMacLostRecoveryPairs(values)
	if err != nil {
		return nil, err
	}
	requests := make([]execution.CompletedTeardownRecoveryRequest, 0, len(pairs))
	for _, pair := range pairs {
		requests = append(requests, execution.CompletedTeardownRecoveryRequest{SessionID: pair.SessionID, CommandID: pair.CommandID})
	}
	return requests, nil
}

func recoverMacCompletedTeardownSet(ctx context.Context, authority *store.AuthorityStore, service *execution.Service, runtimeAdapter execution.RuntimeOwnershipAuditor, requests []execution.CompletedTeardownRecoveryRequest, stdout, stderr io.Writer) int {
	if err := requireMacCompletedTeardownRecoveryInventory(ctx, authority, service, runtimeAdapter, requests); err != nil {
		fmt.Fprintln(stderr, "runner-locald recover-stalled: completed teardown preflight refused")
		return 1
	}
	results, err := service.RecoverCompletedTeardownRecoveryBatch(ctx, requests)
	if err != nil {
		fmt.Fprintf(stderr, "runner-locald recover-stalled: completed teardown recovery failed: reason=%s\n", queueworker.RetainedCapacityRecoveryFailureReason(err))
		return 1
	}
	if err := requireMacStalledRecoveryPostflight(ctx, authority); err != nil {
		fmt.Fprintln(stderr, "runner-locald recover-stalled: completed teardown postflight refused")
		return 1
	}
	var recovered, alreadyRecovered int
	for _, result := range results {
		if result.Recovery.AlreadyRecovered {
			alreadyRecovered++
		} else {
			recovered++
		}
	}
	fmt.Fprintf(stdout, "runner-locald recover-stalled: recovered_teardown_pairs=%d already_recovered_teardown_pairs=%d\n", recovered, alreadyRecovered)
	return 0
}

// A completed command and a lost session teardown are different durable facts.
// Require the whole idle authority and exact selected ownership set before the
// execution service proves cleanup; never route these pairs through lost-command
// recovery or infer a command loss from a retained session reservation.
func requireMacCompletedTeardownRecoveryInventory(ctx context.Context, authority *store.AuthorityStore, service *execution.Service, runtimeAdapter execution.RuntimeOwnershipAuditor, requests []execution.CompletedTeardownRecoveryRequest) error {
	if authority == nil || service == nil || runtimeAdapter == nil || len(requests) == 0 {
		return errors.New("recovery dependencies are incomplete")
	}
	if active, err := authority.CountActiveSessions(ctx); err != nil || active != 0 {
		return errors.New("active work is present")
	}
	if running, err := authority.CountRunningCommands(ctx); err != nil || running != 0 {
		return errors.New("running work is present")
	}
	if slots, err := authority.CountLiveCommandSlots(ctx); err != nil || slots != 0 {
		return errors.New("live command capacity is present")
	}
	if jobs, err := authority.ListNonterminalJobIDs(ctx); err != nil || len(jobs) != 0 {
		return errors.New("nonterminal jobs are present")
	}
	metrics, err := authority.ReadRestartPreflightMetrics(ctx)
	if err != nil || metrics.QueuedCommands != 0 {
		return errors.New("queued work is present or unavailable")
	}
	if pending, err := authority.ListPendingLostRuntimeRecoveryFinalizations(ctx); err != nil || len(pending) != 0 {
		return errors.New("lost command finalization is pending")
	}
	if pending, err := authority.ListPendingCommandlessLostRuntimeRecoveryFinalizations(ctx); err != nil || len(pending) != 0 {
		return errors.New("commandless lost finalization is pending")
	}
	selected := make(map[store.CompletedTeardownRecoveryPair]struct{}, len(requests))
	attributable := make(map[string]struct{}, len(requests))
	for _, request := range requests {
		pair := store.CompletedTeardownRecoveryPair{SessionID: request.SessionID, CommandID: request.CommandID}
		if _, exists := selected[pair]; exists {
			return errors.New("duplicate completed teardown selection")
		}
		selected[pair] = struct{}{}
		attributable[string(request.SessionID)] = struct{}{}
	}
	pending, err := authority.ListPendingCompletedTeardownRecoveryFinalizations(ctx)
	if err != nil {
		return err
	}
	pendingPairs := make(map[store.CompletedTeardownRecoveryPair]struct{}, len(pending))
	for _, result := range pending {
		if _, ok := selected[result.Pair]; !ok {
			return errors.New("pending completed teardown finalization is not selected")
		}
		pendingPairs[result.Pair] = struct{}{}
	}
	checked, err := service.CheckCompletedTeardownRecoveryBatch(ctx, requests)
	if err != nil {
		return err
	}
	if len(checked) != len(requests) {
		return errors.New("completed teardown inventory is incomplete")
	}
	unreleased := 0
	for _, result := range checked {
		if _, ok := selected[result.Recovery.Pair]; !ok {
			return errors.New("completed teardown identity does not match selection")
		}
		if !result.Recovery.AlreadyRecovered {
			unreleased++
			continue
		}
		if _, ok := pendingPairs[result.Recovery.Pair]; !ok || !result.Recovery.FinalizationPending {
			return errors.New("selected released completed teardown has no pending finalization")
		}
	}
	if reservations, err := authority.CountLiveSessionReservations(ctx); err != nil || reservations != unreleased {
		return errors.New("retained session inventory does not match selection")
	}
	return runtimeAdapter.AuditOwnership(ctx, attributable)
}
