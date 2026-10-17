package localapi

import (
	"bytes"
	"context"
	"database/sql"
	"net/http"
	"strings"
	"testing"

	"remote-session-runner/src/internal/laneadmission"
	"remote-session-runner/src/internal/testfixture"
)

func TestLanesL111LocalAPIAndMailboxErrorsAreSafe(t *testing.T) {
	ctx := context.Background()
	root := testfixture.New(t)
	db, authority := p080Open(t, root.Path()+"/state/l111-localapi.db")
	defer db.Close()
	if err := authority.ConfigureLaneAdmission(ctx, laneadmission.LaneDev, laneadmission.HostMacControlPlane); err != nil {
		t.Fatal(err)
	}
	owner := p080Owner(t)
	server, client, serveErr := p080StartAPI(t, authority, owner, p080SocketDir(t)+"/l111.sock")
	request, err := http.NewRequest(http.MethodPost, "http://local/v1/sessions", bytes.NewBufferString(`{"environment":"mac-dev","execution_target":{"kind":"local","profile":"mac-workstation"}}`))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Idempotency-Key", "key-l111-localapi")
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	var body errorEnvelope
	responseBody := p080ReadResponse(t, response, &body)
	if response.StatusCode != http.StatusLocked || body.Code != "lane_admission_closed" || strings.Contains(responseBody, root.Path()) {
		t.Fatalf("closed local API response status=%d body=%q", response.StatusCode, responseBody)
	}
	if got := localapiCountIntents(t, db); got != 0 {
		t.Fatalf("closed local API admitted %d intents", got)
	}
	if err := server.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := <-serveErr; err != nil {
		t.Fatal(err)
	}

	failure := localFailure(http.StatusLocked, "lane_admission_closed", "lane admission is closed")
	mailboxErr := mailboxOperationError(failure)
	if mailboxErr.Code != "runtime_unavailable" || !mailboxErr.Retryable || mailboxErr.Message != failure.message {
		t.Fatalf("mailbox admission failure=%+v, want safe retryable runtime_unavailable", mailboxErr)
	}
}

func localapiCountIntents(t *testing.T, db *sql.DB) int {
	t.Helper()
	var count int
	if err := db.QueryRowContext(context.Background(), `SELECT count(*) FROM local_intents`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}
