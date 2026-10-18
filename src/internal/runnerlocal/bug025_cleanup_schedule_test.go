package runnerlocal

import (
	"testing"
	"time"
)

func resetBUG025CleanupScheduleForTest(s *Service) {
	if s == nil {
		return
	}
	s.artifactCleanupMu.Lock()
	s.lastArtifactCleanup = time.Time{}
	s.artifactCleanupMu.Unlock()
}

func TestBUG025MailboxCleanupScheduleRotatesAndWaitsAfterCompletion(t *testing.T) {
	s := &Service{}
	runtimes := []mailboxRuntime{{id: "alpha"}, {id: "beta"}, {id: "gamma"}}
	start := time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)

	first, ok := s.nextMailboxArtifactCleanupRuntime(runtimes, start)
	if !ok || first.id != "alpha" {
		t.Fatalf("first cleanup turn=%+v ok=%t, want alpha", first, ok)
	}
	finished := start.Add(3 * time.Second)
	s.markMailboxArtifactCleanupComplete(finished)
	if _, ok := s.nextMailboxArtifactCleanupRuntime(runtimes, finished.Add(mailboxArtifactCleanupInterval-time.Nanosecond)); ok {
		t.Fatal("cleanup ran before the quiet interval after the prior pass completed")
	}
	second, ok := s.nextMailboxArtifactCleanupRuntime(runtimes, finished.Add(mailboxArtifactCleanupInterval))
	if !ok || second.id != "beta" {
		t.Fatalf("second cleanup turn=%+v ok=%t, want beta after the full interval", second, ok)
	}
	s.markMailboxArtifactCleanupComplete(finished.Add(2 * mailboxArtifactCleanupInterval))
	third, ok := s.nextMailboxArtifactCleanupRuntime(runtimes, finished.Add(3*mailboxArtifactCleanupInterval))
	if !ok || third.id != "gamma" {
		t.Fatalf("third cleanup turn=%+v ok=%t, want gamma after mailbox rotation", third, ok)
	}
}
