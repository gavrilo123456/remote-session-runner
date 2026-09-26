package store

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"remote-session-runner/src/internal/domain"
	"remote-session-runner/src/internal/testfixture"
)

func TestP013CreateAcceptanceIdempotencyAndReservation(t *testing.T) {
	root := testfixture.New(t)
	databasePath := root.Path() + "/state/p013.db"
	db, err := Open(context.Background(), databasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	fixedNow := time.Date(2026, 9, 26, 13, 0, 0, 0, time.UTC)
	store, err := NewAuthorityStoreWithClock(db, func() time.Time { return fixedNow })
	if err != nil {
		t.Fatal(err)
	}

	input := p013Acceptance(t, "session-p013-1", "create-p013-1", "mac-dev")
	first, duplicate, err := store.AcceptSessionCreate(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	if duplicate {
		t.Fatal("first create reported duplicate")
	}
	if first.State != domain.SessionStateCreating || first.SessionID != input.SessionCreate.SessionID {
		t.Fatalf("first record = %+v", first)
	}
	if got, err := store.CountLiveSessionReservations(context.Background()); err != nil || got != 1 {
		t.Fatalf("live reservations = %d, err = %v, want 1", got, err)
	}
	reservation, err := store.GetSessionReservation(context.Background(), first.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if reservation.HostKey != reservationHostKey || reservation.CleanupConfirmedAt != nil || reservation.ReleasedAt != nil || !reservation.ReservedAt.Equal(fixedNow) {
		t.Fatalf("reservation = %+v", reservation)
	}

	retry, duplicate, err := store.AcceptSessionCreate(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	if !duplicate || retry.SessionID != first.SessionID || retry.CreatedAt != first.CreatedAt {
		t.Fatalf("same-key retry = %+v duplicate=%v, want original", retry, duplicate)
	}
	if got, err := store.CountLiveSessionReservations(context.Background()); err != nil || got != 1 {
		t.Fatalf("duplicate live reservations = %d, err = %v, want 1", got, err)
	}

	conflict := input
	conflict.RequestHash = p013Hash(t, `{"operation":"create_session","environment":"different"}`)
	if _, duplicate, err := store.AcceptSessionCreate(context.Background(), conflict); !errors.Is(err, ErrIdempotencyConflict) || duplicate {
		t.Fatalf("changed same-key create = duplicate %v, err %v, want conflict", duplicate, err)
	}
	if got, err := store.CountLiveSessionReservations(context.Background()); err != nil || got != 1 {
		t.Fatalf("conflict live reservations = %d, err = %v, want 1", got, err)
	}

	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	reopenedDB, err := Open(context.Background(), databasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer reopenedDB.Close()
	reopened, err := NewAuthorityStoreWithClock(reopenedDB, func() time.Time { return fixedNow })
	if err != nil {
		t.Fatal(err)
	}
	reopenedRecord, err := reopened.GetSession(context.Background(), first.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if reopenedRecord.SessionID != first.SessionID || reopenedRecord.State != domain.SessionStateCreating {
		t.Fatalf("reopened session = %+v", reopenedRecord)
	}
	if got, err := reopened.CountLiveSessionReservations(context.Background()); err != nil || got != 1 {
		t.Fatalf("reopened live reservations = %d, err = %v, want 1", got, err)
	}
}

func TestP013CreateAcceptanceCapsConcurrentCreatesAtTwenty(t *testing.T) {
	root := testfixture.New(t)
	db, err := Open(context.Background(), root.Path()+"/state/race.db")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store, err := NewAuthorityStoreWithClock(db, func() time.Time { return time.Date(2026, 9, 26, 14, 0, 0, 0, time.UTC) })
	if err != nil {
		t.Fatal(err)
	}

	const attempts = DefaultActiveSessionLimit + 4
	start := make(chan struct{})
	results := make(chan error, attempts)
	var wait sync.WaitGroup
	for i := 0; i < attempts; i++ {
		i := i
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start
			input := p013Acceptance(t, fmt.Sprintf("session-race-%02d", i), fmt.Sprintf("key-race-%02d", i), "linux-dev")
			input.MaxActiveSessions = DefaultActiveSessionLimit
			_, _, err := store.AcceptSessionCreate(context.Background(), input)
			results <- err
		}()
	}
	close(start)
	wait.Wait()
	close(results)

	accepted, capacityRejected := 0, 0
	for err := range results {
		switch {
		case err == nil:
			accepted++
		case errors.Is(err, ErrSessionCapacityExceeded):
			capacityRejected++
		default:
			t.Fatalf("concurrent create error = %v", err)
		}
	}
	if accepted != DefaultActiveSessionLimit || capacityRejected != attempts-DefaultActiveSessionLimit {
		t.Fatalf("accepted=%d capacity_rejected=%d, want %d and %d", accepted, capacityRejected, DefaultActiveSessionLimit, attempts-DefaultActiveSessionLimit)
	}
	if got, err := store.CountLiveSessionReservations(context.Background()); err != nil || got != DefaultActiveSessionLimit {
		t.Fatalf("live reservations = %d, err = %v, want %d", got, err, DefaultActiveSessionLimit)
	}
}

func TestP013ConfirmedCleanupReleasesExactlyOneReservation(t *testing.T) {
	root := testfixture.New(t)
	db, err := Open(context.Background(), root.Path()+"/state/release.db")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store, err := NewAuthorityStoreWithClock(db, func() time.Time { return time.Date(2026, 9, 26, 15, 0, 0, 0, time.UTC) })
	if err != nil {
		t.Fatal(err)
	}
	input := p013Acceptance(t, "session-release-1", "key-release-1", "linux-dev")
	input.MaxActiveSessions = 1
	created, _, err := store.AcceptSessionCreate(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	second := p013Acceptance(t, "session-release-2", "key-release-2", "linux-dev")
	second.MaxActiveSessions = 1
	if _, _, err := store.AcceptSessionCreate(context.Background(), second); !errors.Is(err, ErrSessionCapacityExceeded) {
		t.Fatalf("second create error = %v, want capacity error", err)
	}
	if err := store.ConfirmSessionCleanup(context.Background(), created.SessionID); err != nil {
		t.Fatal(err)
	}
	if err := store.ConfirmSessionCleanup(context.Background(), created.SessionID); err != nil {
		t.Fatalf("duplicate cleanup confirmation = %v", err)
	}
	reservation, err := store.GetSessionReservation(context.Background(), created.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if reservation.CleanupConfirmedAt == nil || reservation.ReleasedAt == nil {
		t.Fatalf("released reservation = %+v", reservation)
	}
	if got, err := store.CountLiveSessionReservations(context.Background()); err != nil || got != 0 {
		t.Fatalf("live reservations after cleanup = %d, err = %v, want 0", got, err)
	}
	if _, _, err := store.AcceptSessionCreate(context.Background(), p013Acceptance(t, "session-release-2", "key-release-2", "linux-dev")); err != nil {
		t.Fatalf("create after confirmed cleanup = %v", err)
	}
}

func TestP013ExpiredIdempotencyDoesNotReleaseLiveReservation(t *testing.T) {
	root := testfixture.New(t)
	db, err := Open(context.Background(), root.Path()+"/state/expiry.db")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	now := time.Date(2026, 9, 26, 16, 0, 0, 0, time.UTC)
	store, err := NewAuthorityStoreWithClock(db, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	first := p013Acceptance(t, "session-expiry-1", "key-expiry", "linux-dev")
	first.MaxActiveSessions = 1
	created, _, err := store.AcceptSessionCreate(context.Background(), first)
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(DefaultSessionIdempotencyRetention + time.Nanosecond)
	second := p013Acceptance(t, "session-expiry-2", "key-expiry", "linux-dev")
	second.MaxActiveSessions = 1
	if _, _, err := store.AcceptSessionCreate(context.Background(), second); !errors.Is(err, ErrSessionCapacityExceeded) {
		t.Fatalf("expired-key create error = %v, want live-reservation capacity error", err)
	}
	if got, err := store.CountLiveSessionReservations(context.Background()); err != nil || got != 1 {
		t.Fatalf("live reservations after key expiry = %d, err = %v, want 1", got, err)
	}
	if err := store.ConfirmSessionCleanup(context.Background(), created.SessionID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.AcceptSessionCreate(context.Background(), second); err != nil {
		t.Fatalf("create after confirmed cleanup and key expiry = %v", err)
	}
}

func p013Acceptance(t *testing.T, sessionID, key, environment string) SessionCreateAcceptance {
	t.Helper()
	targetKind := domain.TargetKindRemote
	profile := "linux-host"
	controllerType := domain.ControllerTypeDirectMTLS
	if environment == "mac-dev" {
		targetKind = domain.TargetKindLocal
		profile = "mac-workstation"
		controllerType = domain.ControllerTypeLocalUser
	}
	target, err := domain.NewExecutionTarget(targetKind, profile)
	if err != nil {
		t.Fatal(err)
	}
	controller, err := newController(controllerType, "tomasz.walczuk")
	if err != nil {
		t.Fatal(err)
	}
	return SessionCreateAcceptance{
		SessionCreate: SessionCreate{
			SessionID:   domain.SessionID(sessionID),
			Target:      target,
			Environment: environment,
			Controller:  controller,
			Source:      domain.NewEmptySource(),
			Limits:      testSessionLimits(),
		},
		IdempotencyKey: key,
		RequestHash:    p013Hash(t, fmt.Sprintf(`{"operation":"create_session","environment":%q}`, environment)),
	}
}

func p013Hash(t *testing.T, request string) domain.CanonicalHash {
	t.Helper()
	hash, err := domain.HashMutationRequestJSON("create_session", []byte(request), domain.CanonicalizationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	return hash
}
