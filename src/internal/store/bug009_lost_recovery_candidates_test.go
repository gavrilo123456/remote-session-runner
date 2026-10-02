package store

import (
	"context"
	"reflect"
	"testing"
	"time"

	"remote-session-runner/src/internal/domain"
)

func TestBUG009ListRetainedLostRuntimeRecoveryPairsReturnsOnlyRetainedLostPairs(t *testing.T) {
	clock := &p019Clock{value: time.Date(2026, 10, 2, 8, 0, 0, 0, time.UTC)}
	authority := newP019Store(t, clock)
	want := pBUG008LostPairs(t, authority, "bug009-listed")

	got, err := authority.ListRetainedLostRuntimeRecoveryPairs(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("retained lost pairs=%+v, want %+v", got, want)
	}
	for _, pair := range want {
		pStalledRecoveryAssertLostPairRetained(t, authority, pair)
	}
}

func TestBUG009ListRetainedLostRuntimeRecoveryPairsExcludesUnsafeCandidates(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(t *testing.T, authority *AuthorityStore, pair LostRuntimeRecoveryPair)
	}{
		{
			name: "non-lost command",
			mutate: func(t *testing.T, authority *AuthorityStore, pair LostRuntimeRecoveryPair) {
				t.Helper()
				if _, err := authority.db.Exec(`UPDATE exec_commands SET state = ? WHERE command_id = ?`, string(domain.CommandStateFailed), string(pair.CommandID)); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "partially released command slot",
			mutate: func(t *testing.T, authority *AuthorityStore, pair LostRuntimeRecoveryPair) {
				t.Helper()
				if err := authority.ConfirmCommandSlotRelease(context.Background(), pair.CommandID); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "partially released session reservation",
			mutate: func(t *testing.T, authority *AuthorityStore, pair LostRuntimeRecoveryPair) {
				t.Helper()
				if err := authority.ConfirmSessionCleanup(context.Background(), pair.SessionID); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "missing terminal lost event",
			mutate: func(t *testing.T, authority *AuthorityStore, pair LostRuntimeRecoveryPair) {
				t.Helper()
				if _, err := authority.db.Exec(`DELETE FROM exec_command_events WHERE command_id = ? AND event_type = 'command_lost'`, string(pair.CommandID)); err != nil {
					t.Fatal(err)
				}
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			clock := &p019Clock{value: time.Date(2026, 10, 2, 8, 1, 0, 0, time.UTC)}
			authority := newP019Store(t, clock)
			pairs := pBUG008LostPairs(t, authority, "bug009-exclude")
			excluded := pairs[1]
			test.mutate(t, authority, excluded)

			got, err := authority.ListRetainedLostRuntimeRecoveryPairs(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			want := append(append([]LostRuntimeRecoveryPair{}, pairs[:1]...), pairs[2:]...)
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("retained lost pairs=%+v, want %+v", got, want)
			}
			for _, pair := range got {
				pStalledRecoveryAssertLostPairRetained(t, authority, pair)
			}
		})
	}
}

func TestBUG009ListRetainedLostRuntimeRecoveryPairsReturnsEmptyWithoutLostCapacity(t *testing.T) {
	clock := &p019Clock{value: time.Date(2026, 10, 2, 8, 2, 0, 0, time.UTC)}
	authority := newP019Store(t, clock)
	sessionID := p019ReadySession(t, authority, "session-bug009-no-lost", "key-bug009-no-lost")
	p019Command(t, authority, sessionID, "command-bug009-no-lost", "key-bug009-no-lost")

	got, err := authority.ListRetainedLostRuntimeRecoveryPairs(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("retained lost pairs=%+v, want empty", got)
	}
}
