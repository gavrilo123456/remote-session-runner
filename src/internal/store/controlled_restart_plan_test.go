package store

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"remote-session-runner/src/internal/domain"
)

func TestControlledRestartPlanPreparesReadsAndClearsExactShape(t *testing.T) {
	ctx := context.Background()
	authority := pControlledRestartPlanAuthority(t)
	pairs := pBUG008LostPairs(t, authority, "controlled-restart-prepare")
	queued := pControlledRestartQueuedOneOff(t, authority, "controlled-restart-prepare")

	plan, err := authority.PrepareControlledRestartPlan(ctx, pairs)
	if err != nil {
		t.Fatal(err)
	}
	pControlledRestartAssertPlanForQueuedOneOff(t, authority, plan, pairs, queued)
	pControlledRestartAssertQueuedOneOffUntouched(t, authority, queued)
	pControlledRestartAssertLostPairsRetained(t, authority, pairs)

	// Pair order is caller input, not plan identity. A retry after the durable
	// record exists must return that plan without reinterpreting its state.
	reversed := append([]LostRuntimeRecoveryPair(nil), pairs...)
	for left, right := 0, len(reversed)-1; left < right; left, right = left+1, right-1 {
		reversed[left], reversed[right] = reversed[right], reversed[left]
	}
	repeated, err := authority.PrepareControlledRestartPlan(ctx, reversed)
	if err != nil {
		t.Fatal(err)
	}
	if !sameControlledRestartPlan(plan, repeated) {
		t.Fatal("same recovery shape did not return the original controlled restart plan")
	}

	stored, err := authority.ReadControlledRestartPlan(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !sameControlledRestartPlan(plan, stored) {
		t.Fatal("read controlled restart plan differs from prepared plan")
	}
	if err := authority.ClearControlledRestartPlan(ctx, stored); err != nil {
		t.Fatal(err)
	}
	if _, err := authority.ReadControlledRestartPlan(ctx); !errors.Is(err, ErrControlledRestartPlanNotFound) {
		t.Fatalf("cleared plan read error=%v, want %v", err, ErrControlledRestartPlanNotFound)
	}
	pControlledRestartAssertQueuedOneOffUntouched(t, authority, queued)
	pControlledRestartAssertLostPairsRetained(t, authority, pairs)
}

func TestControlledRestartPlanRejectsEveryOtherRecoveryShapeWithoutWrites(t *testing.T) {
	ctx := context.Background()
	for _, test := range []struct {
		name        string
		newQueued   func(t *testing.T, authority *AuthorityStore, suffix string) JobRecord
		setup       func(t *testing.T, authority *AuthorityStore, pairs []LostRuntimeRecoveryPair, queued JobRecord)
		selectPairs func([]LostRuntimeRecoveryPair) []LostRuntimeRecoveryPair
	}{
		{
			name: "fewer than all retained lost pairs",
			selectPairs: func(pairs []LostRuntimeRecoveryPair) []LostRuntimeRecoveryPair {
				return pairs[:len(pairs)-1]
			},
		},
		{
			name: "no queued one-off",
			setup: func(t *testing.T, authority *AuthorityStore, _ []LostRuntimeRecoveryPair, queued JobRecord) {
				t.Helper()
				if _, err := authority.TransitionCommand(context.Background(), CommandTransition{
					CommandID: queued.CommandID, NextState: domain.CommandStateCancelled, OutputComplete: true,
				}); err != nil {
					t.Fatal(err)
				}
				if _, err := authority.TransitionSession(context.Background(), queued.SessionID, domain.SessionStateClosing, "controlled_restart_test_close"); err != nil {
					t.Fatal(err)
				}
				if _, err := authority.TransitionSession(context.Background(), queued.SessionID, domain.SessionStateClosed, "controlled_restart_test_closed"); err != nil {
					t.Fatal(err)
				}
				if err := authority.ConfirmSessionCleanup(context.Background(), queued.SessionID); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "more than one queued one-off",
			setup: func(t *testing.T, authority *AuthorityStore, _ []LostRuntimeRecoveryPair, _ JobRecord) {
				t.Helper()
				_ = pControlledRestartQueuedOneOff(t, authority, "controlled-restart-extra")
			},
		},
		{
			name:      "queued one-off has non-empty source",
			newQueued: pControlledRestartGitQueuedOneOff,
		},
		{
			name:      "queued one-off has no runtime generation",
			newQueued: pBUG008QueuedOneOff,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			authority := pControlledRestartPlanAuthority(t)
			pairs := pBUG008LostPairs(t, authority, "controlled-restart-reject")
			newQueued := pControlledRestartQueuedOneOff
			if test.newQueued != nil {
				newQueued = test.newQueued
			}
			queued := newQueued(t, authority, "controlled-restart-reject")
			if test.setup != nil {
				test.setup(t, authority, pairs, queued)
			}
			selected := pairs
			if test.selectPairs != nil {
				selected = test.selectPairs(pairs)
			}

			if _, err := authority.PrepareControlledRestartPlan(ctx, selected); !errors.Is(err, ErrLostRuntimeRecoveryNotReleasable) {
				t.Fatalf("prepare error=%v, want %v", err, ErrLostRuntimeRecoveryNotReleasable)
			}
			if _, err := authority.ReadControlledRestartPlan(ctx); !errors.Is(err, ErrControlledRestartPlanNotFound) {
				t.Fatalf("rejected shape left plan read error=%v, want %v", err, ErrControlledRestartPlanNotFound)
			}
			pControlledRestartAssertLostPairsRetained(t, authority, pairs)
			if test.name != "no queued one-off" {
				pControlledRestartAssertQueuedOneOffUntouched(t, authority, queued)
			}
		})
	}
}

func TestControlledRestartPlanReadFailsClosedWhenRuntimeGenerationChanges(t *testing.T) {
	ctx := context.Background()
	authority := pControlledRestartPlanAuthority(t)
	pairs := pBUG008LostPairs(t, authority, "controlled-restart-generation-mismatch")
	queued := pControlledRestartQueuedOneOff(t, authority, "controlled-restart-generation-mismatch")
	plan, err := authority.PrepareControlledRestartPlan(ctx, pairs)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := authority.db.Exec(`
UPDATE exec_sessions
SET runtime_generation = ?
WHERE session_id = ?`, plan.RuntimeGeneration+"-changed", string(plan.SessionID)); err != nil {
		t.Fatal(err)
	}
	if _, err := authority.ReadControlledRestartPlan(ctx); !errors.Is(err, ErrControlledRestartPlanCorrupt) {
		t.Fatalf("generation-mismatch read error=%v, want %v", err, ErrControlledRestartPlanCorrupt)
	}
	if _, err := authority.StartNextEligibleCommand(ctx, DefaultRunningCommandLimit); !errors.Is(err, ErrControlledRestartPlanCorrupt) {
		t.Fatalf("generation-mismatch scheduler error=%v, want %v", err, ErrControlledRestartPlanCorrupt)
	}
	command, err := authority.GetCommand(ctx, queued.CommandID)
	if err != nil {
		t.Fatal(err)
	}
	if command.State != domain.CommandStateQueued {
		t.Fatal("generation mismatch allowed the queued command to start")
	}
}

func TestControlledRestartPlanClearRequiresExactActivationState(t *testing.T) {
	ctx := context.Background()
	authority := pControlledRestartPlanAuthority(t)
	pairs := pBUG008LostPairs(t, authority, "controlled-restart-clear-active")
	_ = pControlledRestartQueuedOneOff(t, authority, "controlled-restart-clear-active")
	prepared, err := authority.PrepareControlledRestartPlan(ctx, pairs)
	if err != nil {
		t.Fatal(err)
	}
	active, err := authority.ActivateControlledRestartPlan(ctx, prepared)
	if err != nil {
		t.Fatal(err)
	}
	if err := authority.ClearControlledRestartPlan(ctx, prepared); !errors.Is(err, ErrControlledRestartPlanConflict) {
		t.Fatalf("stale prepared clear error=%v, want %v", err, ErrControlledRestartPlanConflict)
	}
	if err := authority.ClearControlledRestartPlan(ctx, active); err != nil {
		t.Fatal(err)
	}
	if _, err := authority.ReadControlledRestartPlan(ctx); !errors.Is(err, ErrControlledRestartPlanNotFound) {
		t.Fatalf("active clear read error=%v, want %v", err, ErrControlledRestartPlanNotFound)
	}
}

func TestControlledRestartPlanPrepareRejectsActivePlan(t *testing.T) {
	ctx := context.Background()
	authority := pControlledRestartPlanAuthority(t)
	pairs := pBUG008LostPairs(t, authority, "controlled-restart-active-prepare")
	_ = pControlledRestartQueuedOneOff(t, authority, "controlled-restart-active-prepare")
	prepared, err := authority.PrepareControlledRestartPlan(ctx, pairs)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := authority.ActivateControlledRestartPlan(ctx, prepared); err != nil {
		t.Fatal(err)
	}
	if _, err := authority.PrepareControlledRestartPlan(ctx, pairs); !errors.Is(err, ErrControlledRestartPlanConflict) {
		t.Fatalf("prepare active plan error=%v, want %v", err, ErrControlledRestartPlanConflict)
	}
}

func TestControlledRestartPlanSchedulerClaimsOnlyPlannedCommandThenConsumesPlan(t *testing.T) {
	ctx := context.Background()
	authority := pControlledRestartPlanAuthority(t)
	pairs := pBUG008LostPairs(t, authority, "controlled-restart-scheduler")
	planned := pControlledRestartQueuedOneOff(t, authority, "controlled-restart-scheduler-planned")
	plan, err := authority.PrepareControlledRestartPlan(ctx, pairs)
	if err != nil {
		t.Fatal(err)
	}
	pControlledRestartAssertPlanForQueuedOneOff(t, authority, plan, pairs, planned)
	if err := authority.ConfirmLostRuntimeRecoveryBatchPreservingQueuedOneOffs(ctx, pairs); err != nil {
		t.Fatal(err)
	}
	other := pControlledRestartQueuedOneOff(t, authority, "controlled-restart-scheduler-other")
	if _, err := authority.StartNextEligibleCommand(ctx, DefaultRunningCommandLimit); !errors.Is(err, ErrControlledRestartPlanNotActive) {
		t.Fatalf("inactive scheduler error=%v, want %v", err, ErrControlledRestartPlanNotActive)
	}
	pControlledRestartAssertQueuedOneOffUntouched(t, authority, planned)
	pControlledRestartAssertQueuedOneOffUntouched(t, authority, other)
	wrongGeneration := plan
	wrongGeneration.RuntimeGeneration += "-wrong"
	if _, err := authority.ActivateControlledRestartPlan(ctx, wrongGeneration); !errors.Is(err, ErrControlledRestartPlanConflict) {
		t.Fatalf("wrong-generation activation error=%v, want %v", err, ErrControlledRestartPlanConflict)
	}
	activePlan, err := authority.ActivateControlledRestartPlan(ctx, plan)
	if err != nil {
		t.Fatal(err)
	}
	if !activePlan.Activated || activePlan.ActivatedAt == nil || !sameControlledRestartPlanIdentity(plan, activePlan) {
		t.Fatal("activation did not preserve the exact plan identity")
	}
	if repeatedActivation, err := authority.ActivateControlledRestartPlan(ctx, plan); err != nil || !sameControlledRestartPlan(activePlan, repeatedActivation) {
		t.Fatalf("idempotent activation err=%v", err)
	}

	claimed, err := authority.StartNextEligibleCommand(ctx, DefaultRunningCommandLimit)
	if err != nil {
		t.Fatal(err)
	}
	if claimed.CommandID != plan.CommandID || claimed.SessionID != plan.SessionID || claimed.State != domain.CommandStateRunning {
		t.Fatal("scheduler did not claim the exact controlled-restart command")
	}
	if _, err := authority.ReadControlledRestartPlan(ctx); !errors.Is(err, ErrControlledRestartPlanNotFound) {
		t.Fatalf("claimed plan read error=%v, want %v", err, ErrControlledRestartPlanNotFound)
	}
	otherBefore, err := authority.GetCommand(ctx, other.CommandID)
	if err != nil {
		t.Fatal(err)
	}
	if otherBefore.State != domain.CommandStateQueued {
		t.Fatal("unplanned queued command changed before plan was consumed")
	}

	next, err := authority.StartNextEligibleCommand(ctx, DefaultRunningCommandLimit)
	if err != nil {
		t.Fatal(err)
	}
	if next.CommandID != other.CommandID || next.State != domain.CommandStateRunning {
		t.Fatal("unplanned queued command was not eligible after plan consumption")
	}
}

func TestControlledRestartPlanSchedulerDoesNotBypassUnclaimablePlan(t *testing.T) {
	ctx := context.Background()
	authority := pControlledRestartPlanAuthority(t)
	pairs := pBUG008LostPairs(t, authority, "controlled-restart-no-bypass")
	planned := pControlledRestartQueuedOneOff(t, authority, "controlled-restart-no-bypass-planned")
	plan, err := authority.PrepareControlledRestartPlan(ctx, pairs)
	if err != nil {
		t.Fatal(err)
	}
	if err := authority.ConfirmLostRuntimeRecoveryBatchPreservingQueuedOneOffs(ctx, pairs); err != nil {
		t.Fatal(err)
	}
	other := pControlledRestartQueuedOneOff(t, authority, "controlled-restart-no-bypass-other")
	if _, err := authority.StartNextEligibleCommand(ctx, DefaultRunningCommandLimit); !errors.Is(err, ErrControlledRestartPlanNotActive) {
		t.Fatalf("inactive scheduler error=%v, want %v", err, ErrControlledRestartPlanNotActive)
	}
	activePlan, err := authority.ActivateControlledRestartPlan(ctx, plan)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := authority.TransitionCommand(ctx, CommandTransition{
		CommandID: planned.CommandID, NextState: domain.CommandStateCancelled, OutputComplete: true,
	}); err != nil {
		t.Fatal(err)
	}

	if _, err := authority.StartNextEligibleCommand(ctx, DefaultRunningCommandLimit); !errors.Is(err, ErrCommandNotEligible) {
		t.Fatalf("scheduler bypass error=%v, want %v", err, ErrCommandNotEligible)
	}
	stored, err := authority.ReadControlledRestartPlan(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !sameControlledRestartPlan(activePlan, stored) {
		t.Fatal("unclaimable plan changed while scheduler considered a different command")
	}
	otherCommand, err := authority.GetCommand(ctx, other.CommandID)
	if err != nil {
		t.Fatal(err)
	}
	if otherCommand.State != domain.CommandStateQueued {
		t.Fatal("scheduler bypassed the controlled restart plan")
	}
}

func TestControlledRestartPlanSchedulerRollsBackClaimWhenPlanDeletionFails(t *testing.T) {
	ctx := context.Background()
	authority := pControlledRestartPlanAuthority(t)
	pairs := pBUG008LostPairs(t, authority, "controlled-restart-atomic")
	planned := pControlledRestartQueuedOneOff(t, authority, "controlled-restart-atomic")
	plan, err := authority.PrepareControlledRestartPlan(ctx, pairs)
	if err != nil {
		t.Fatal(err)
	}
	if err := authority.ConfirmLostRuntimeRecoveryBatchPreservingQueuedOneOffs(ctx, pairs); err != nil {
		t.Fatal(err)
	}
	activePlan, err := authority.ActivateControlledRestartPlan(ctx, plan)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := authority.db.Exec(`
CREATE TRIGGER controlled_restart_plan_block_delete
BEFORE DELETE ON exec_controlled_restart_plans
BEGIN
    SELECT RAISE(ABORT, 'controlled restart fixture delete failure');
END`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = authority.db.Exec(`DROP TRIGGER IF EXISTS controlled_restart_plan_block_delete`)
	})

	if _, err := authority.StartNextEligibleCommand(ctx, DefaultRunningCommandLimit); err == nil {
		t.Fatal("scheduler claim unexpectedly committed when plan deletion failed")
	}
	stored, err := authority.ReadControlledRestartPlan(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !sameControlledRestartPlan(activePlan, stored) {
		t.Fatal("plan changed after failed atomic scheduler claim")
	}
	command, err := authority.GetCommand(ctx, planned.CommandID)
	if err != nil {
		t.Fatal(err)
	}
	if command.State != domain.CommandStateQueued {
		t.Fatal("failed plan deletion left planned command claimed")
	}
	session, err := authority.GetSession(ctx, planned.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if session.State != domain.SessionStateReady {
		t.Fatal("failed plan deletion left planned session busy")
	}
	if _, err := authority.GetCommandSlot(ctx, planned.CommandID); !errors.Is(err, ErrCommandSlotNotFound) {
		t.Fatalf("failed plan deletion slot error=%v, want %v", err, ErrCommandSlotNotFound)
	}
}

func pControlledRestartPlanAuthority(t *testing.T) *AuthorityStore {
	t.Helper()
	clock := &p019Clock{value: time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC)}
	return newP019Store(t, clock)
}

func pControlledRestartQueuedOneOff(t *testing.T, authority *AuthorityStore, suffix string) JobRecord {
	t.Helper()
	return pControlledRestartQueuedOneOffWithSource(t, authority, suffix, domain.NewEmptySource())
}

func pControlledRestartGitQueuedOneOff(t *testing.T, authority *AuthorityStore, suffix string) JobRecord {
	t.Helper()
	source, err := domain.NewGitRevisionSource("fixture-repository", "fixture-revision")
	if err != nil {
		t.Fatal(err)
	}
	return pControlledRestartQueuedOneOffWithSource(t, authority, suffix, source)
}

func pControlledRestartQueuedOneOffWithSource(t *testing.T, authority *AuthorityStore, suffix string, source domain.Source) JobRecord {
	t.Helper()
	input := p024Acceptance(t,
		"job-controlled-restart-"+suffix,
		"session-controlled-restart-"+suffix,
		"command-controlled-restart-"+suffix,
		"key-controlled-restart-"+suffix,
		"echo never-run",
	)
	sourceJSON := `{"mode":"empty"}`
	if source.Mode() == domain.SourceModeGitRevision {
		sourceJSON = fmt.Sprintf(`{"mode":"git_revision","repository_alias":%q,"requested_revision":%q}`, source.RepositoryAlias(), source.RequestedRevision())
	}
	raw := []byte(fmt.Sprintf(`{"operation":"run","environment":"linux-dev","execution_target":{"kind":"remote","profile":"linux-host"},"source":%s,"script":%q}`,
		sourceJSON, input.Script))
	canonical, err := domain.CanonicalizeMutationRequestJSON("run", raw, domain.CanonicalizationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	hash, err := domain.HashMutationRequestJSON("run", canonical, domain.CanonicalizationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	input.Source = source
	input.CanonicalPayload = canonical
	input.RequestHash = hash
	job, duplicate, err := authority.AcceptJob(context.Background(), input)
	if err != nil || duplicate {
		t.Fatalf("accept controlled restart queued one-off duplicate=%v err=%v", duplicate, err)
	}
	sessionAcceptance := p013Acceptance(t, string(job.SessionID), "create-controlled-restart-"+suffix, "linux-dev")
	sessionAcceptance.SessionCreate.Source = source
	sessionAcceptance.SessionCreate.RuntimeGeneration = "controlled-restart-generation-" + suffix
	if _, duplicate, err := authority.AcceptSessionCreate(context.Background(), sessionAcceptance); err != nil || duplicate {
		t.Fatalf("create controlled restart queued one-off session duplicate=%v err=%v", duplicate, err)
	}
	if _, err := authority.TransitionSession(context.Background(), job.SessionID, domain.SessionStateReady, "agent_ready"); err != nil {
		t.Fatal(err)
	}
	command := p019Command(t, authority, job.SessionID, job.CommandID, "command-controlled-restart-"+suffix)
	job, err = authority.CheckpointJob(context.Background(), job.JobID, JobCheckpoint{ExpectedPhase: JobPhaseCreatingSession, NextPhase: JobPhaseAcceptingCommand})
	if err != nil {
		t.Fatal(err)
	}
	job, err = authority.CheckpointJob(context.Background(), job.JobID, JobCheckpoint{ExpectedPhase: JobPhaseAcceptingCommand, NextPhase: JobPhaseAwaitingCommand, Command: &command})
	if err != nil {
		t.Fatal(err)
	}
	return job
}

func pControlledRestartAssertPlanForQueuedOneOff(t *testing.T, authority *AuthorityStore, plan ControlledRestartPlan, pairs []LostRuntimeRecoveryPair, queued JobRecord) {
	t.Helper()
	if plan.JobID != queued.JobID || plan.SessionID != queued.SessionID || plan.CommandID != queued.CommandID || plan.PreparedAt.IsZero() || plan.RuntimeGeneration == "" || plan.Activated || plan.ActivatedAt != nil {
		t.Fatal("prepared plan does not identify the untouched queued one-off")
	}
	session, err := authority.GetSession(context.Background(), queued.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if plan.RuntimeGeneration != session.RuntimeGeneration {
		t.Fatal("prepared plan does not retain the exact queued session runtime generation")
	}
	wantPairs, err := validateControlledRestartPlanPairs(pairs)
	if err != nil {
		t.Fatal(err)
	}
	if !sameControlledRestartLostPairs(plan.LostPairs, wantPairs) {
		t.Fatal("prepared plan does not contain the exact four lost pairs")
	}
}

func pControlledRestartAssertLostPairsRetained(t *testing.T, authority *AuthorityStore, pairs []LostRuntimeRecoveryPair) {
	t.Helper()
	for _, pair := range pairs {
		pStalledRecoveryAssertLostPairRetained(t, authority, pair)
	}
}

func pControlledRestartAssertQueuedOneOffUntouched(t *testing.T, authority *AuthorityStore, queued JobRecord) {
	t.Helper()
	ctx := context.Background()
	job, err := authority.GetJob(ctx, queued.JobID)
	if err != nil {
		t.Fatal(err)
	}
	if job.Phase != JobPhaseAwaitingCommand || job.CommandState == nil || *job.CommandState != domain.CommandStateQueued || job.FinalEventSequence != nil || job.ExitCode != nil || job.OutputComplete || job.OutputTruncated || job.TeardownState != JobTeardownPending || job.TeardownReason != "" {
		t.Fatal("queued one-off job crossed an execution boundary")
	}
	session, err := authority.GetSession(ctx, queued.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if session.State != domain.SessionStateReady {
		t.Fatal("queued one-off session changed")
	}
	command, err := authority.GetCommand(ctx, queued.CommandID)
	if err != nil {
		t.Fatal(err)
	}
	if command.State != domain.CommandStateQueued || command.FinalEventSequence != nil || command.ExitCode != nil || command.OutputComplete || command.OutputTruncated {
		t.Fatal("queued one-off command crossed an execution boundary")
	}
	events, err := authority.ListCommandEvents(ctx, queued.CommandID)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].Sequence != 1 || events[0].Type != "command_queued" {
		t.Fatal("queued one-off event history changed")
	}
	if _, err := authority.GetCommandSlot(ctx, queued.CommandID); !errors.Is(err, ErrCommandSlotNotFound) {
		t.Fatalf("queued one-off command slot error=%v, want %v", err, ErrCommandSlotNotFound)
	}
	reservation, err := authority.GetSessionReservation(ctx, queued.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if reservation.CleanupConfirmedAt != nil || reservation.ReleasedAt != nil {
		t.Fatal("queued one-off reservation was released")
	}
}
