package dispatcher

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"remote-session-runner/src/internal/domain"
	"remote-session-runner/src/internal/store"
	"remote-session-runner/src/internal/testfixture"
)

type p068FakeAcceptor struct {
	mu       sync.Mutex
	requests []AcceptIntentRequest
	err      error
}

func (f *p068FakeAcceptor) AcceptIntent(_ context.Context, request AcceptIntentRequest) (IntentAcceptance, error) {
	f.mu.Lock()
	f.requests = append(f.requests, request)
	f.mu.Unlock()
	if f.err != nil {
		return IntentAcceptance{}, f.err
	}
	return IntentAcceptance{IntentID: string(request.IntentID), Operation: "submit_command", ResourceID: "command-" + strings.TrimPrefix(string(request.IntentID), "intent-"), AcceptanceScope: "target_authority", ExecutionTarget: TargetAcceptance{Kind: "local", Profile: "mac-workstation"}}, nil
}

func TestP068LocalDriverClaimsAndSendsIdentityOnly(t *testing.T) {
	authority := p068Authority(t)
	intent := p068SubmitIntent(t, "intent-p068-local", "session-p068-local", "command-p068-local", domain.TargetKindLocal, "echo local")
	if _, err := authority.CreateLocalIntent(context.Background(), intent); err != nil {
		t.Fatal(err)
	}
	fake := &p068FakeAcceptor{}
	driver, err := NewLocalDriver(authority, fake, "router-p068", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	record, acceptance, err := driver.DispatchNext(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if record.DeliveryState != store.LocalIntentAccepted || acceptance.IntentID != string(intent.IntentID) {
		t.Fatalf("dispatch result = %+v/%+v", record, acceptance)
	}
	if len(fake.requests) != 1 || fake.requests[0].IntentID != intent.IntentID || fake.requests[0].RequestHash.String() != intent.RequestHash.String() || fake.requests[0].IntentOrdinal == nil || *fake.requests[0].IntentOrdinal != *intent.IntentOrdinal {
		t.Fatalf("identity request = %+v", fake.requests)
	}
	if string(intent.ScriptBytes) == "" {
		t.Fatal("fixture must contain script bytes")
	}
	lifecycle, err := authority.ListLocalIntentLifecycle(context.Background(), intent.IntentID)
	if err != nil {
		t.Fatal(err)
	}
	if len(lifecycle) != 3 || lifecycle[1].NewState != store.LocalIntentDispatching || lifecycle[2].NewState != store.LocalIntentAccepted {
		t.Fatalf("lifecycle = %+v", lifecycle)
	}
}

func TestP068LocalDriverDoesNotClaimRemoteOrRunIntents(t *testing.T) {
	authority := p068Authority(t)
	remote := p068SubmitIntent(t, "intent-p068-remote", "session-p068-remote", "command-p068-remote", domain.TargetKindRemote, "echo remote")
	if _, err := authority.CreateLocalIntent(context.Background(), remote); err != nil {
		t.Fatal(err)
	}
	localRun := p068RunIntent(t, "intent-p068-run", "job-p068-run", domain.TargetKindLocal)
	if _, err := authority.CreateLocalIntent(context.Background(), localRun); err != nil {
		t.Fatal(err)
	}
	fake := &p068FakeAcceptor{}
	driver, err := NewLocalDriver(authority, fake, "router-p068", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := driver.DispatchNext(context.Background()); !errors.Is(err, ErrNoLocalDispatchWork) {
		t.Fatalf("dispatch unsupported work = %v, want ErrNoLocalDispatchWork", err)
	}
	for _, id := range []domain.IntentID{remote.IntentID, localRun.IntentID} {
		current, err := authority.GetLocalIntent(context.Background(), id)
		if err != nil {
			t.Fatal(err)
		}
		if current.DeliveryState != store.LocalIntentRecorded {
			t.Fatalf("unsupported intent %s state = %s", id, current.DeliveryState)
		}
	}
	if len(fake.requests) != 0 {
		t.Fatalf("unsupported work reached locald: %+v", fake.requests)
	}
}

func TestP068LocalDriverRacingClaimsHaveOneLocaldCall(t *testing.T) {
	authority := p068Authority(t)
	intent := p068SubmitIntent(t, "intent-p068-race", "session-p068-race", "command-p068-race", domain.TargetKindLocal, "echo race")
	if _, err := authority.CreateLocalIntent(context.Background(), intent); err != nil {
		t.Fatal(err)
	}
	fake := &p068FakeAcceptor{}
	first, err := NewLocalDriver(authority, fake, "router-p068-a", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	second, err := NewLocalDriver(authority, fake, "router-p068-b", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	type result struct{ err error }
	results := make(chan result, 2)
	for _, driver := range []*LocalDriver{first, second} {
		go func(driver *LocalDriver) {
			<-start
			_, _, err := driver.DispatchIntent(context.Background(), intent.IntentID)
			results <- result{err: err}
		}(driver)
	}
	close(start)
	var successes, losers int
	for range 2 {
		if err := (<-results).err; err == nil {
			successes++
		} else if errors.Is(err, store.ErrNoEligibleLocalIntent) || errors.Is(err, store.ErrLocalIntentLeaseHeld) {
			losers++
		} else {
			t.Fatalf("race error = %v", err)
		}
	}
	if successes != 1 || losers != 1 || len(fake.requests) != 1 {
		t.Fatalf("race successes=%d losers=%d locald_calls=%d", successes, losers, len(fake.requests))
	}
}

func TestP068LocalDriverClassifiesRejectedAndTransportOutcomes(t *testing.T) {
	for _, test := range []struct {
		name  string
		err   error
		state store.LocalIntentDeliveryState
	}{
		{name: "rejected", err: ErrLocaldRejected, state: store.LocalIntentNotDelivered},
		{name: "transport", err: ErrLocaldTransport, state: store.LocalIntentUncertain},
	} {
		t.Run(test.name, func(t *testing.T) {
			authority := p068Authority(t)
			intent := p068SubmitIntent(t, "intent-p068-"+test.name, "session-p068-"+test.name, "command-p068-"+test.name, domain.TargetKindLocal, "echo outcome")
			if _, err := authority.CreateLocalIntent(context.Background(), intent); err != nil {
				t.Fatal(err)
			}
			fake := &p068FakeAcceptor{err: test.err}
			driver, err := NewLocalDriver(authority, fake, "router-p068", time.Minute)
			if err != nil {
				t.Fatal(err)
			}
			if _, _, err := driver.DispatchIntent(context.Background(), intent.IntentID); !errors.Is(err, test.err) {
				t.Fatalf("dispatch error = %v, want %v", err, test.err)
			}
			current, err := authority.GetLocalIntent(context.Background(), intent.IntentID)
			if err != nil {
				t.Fatal(err)
			}
			if current.DeliveryState != test.state {
				t.Fatalf("delivery state = %s, want %s", current.DeliveryState, test.state)
			}
		})
	}
}

func TestP068LocaldClientSendsOnlyIntentIdentity(t *testing.T) {
	var requestBody []byte
	client, err := NewLocaldClientWithHTTP(&http.Client{Transport: p068RoundTripFunc(func(request *http.Request) (*http.Response, error) {
		requestBody, _ = io.ReadAll(request.Body)
		body := `{"intent_id":"intent-p068-http","operation":"submit_command","resource_id":"command-p068-http","acceptance_scope":"target_authority","execution_target":{"kind":"local","profile":"mac-workstation"}}`
		return &http.Response{StatusCode: http.StatusAccepted, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
	})})
	if err != nil {
		t.Fatal(err)
	}
	ordinal := int64(7)
	hash := p068Hash(t, `{"operation":"submit_command","script":"secret"}`)
	if _, err := client.AcceptIntent(context.Background(), AcceptIntentRequest{IntentID: "intent-p068-http", RequestHash: hash, IntentOrdinal: &ordinal}); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(requestBody, []byte("secret")) || bytes.Contains(requestBody, []byte("script")) || !bytes.Contains(requestBody, []byte("intent-p068-http")) || !bytes.Contains(requestBody, []byte(hash.String())) {
		t.Fatalf("identity-only request body = %s", requestBody)
	}
}

func p068Authority(t *testing.T) *store.AuthorityStore {
	t.Helper()
	db, err := store.Open(context.Background(), testfixture.New(t).Path()+"/state/p068.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	authority, err := store.NewAuthorityStore(db)
	if err != nil {
		t.Fatal(err)
	}
	return authority
}

func p068SubmitIntent(t *testing.T, intentID, sessionID, commandID string, targetKind domain.TargetKind, script string) store.LocalIntentCreate {
	t.Helper()
	target, err := domain.NewExecutionTarget(targetKind, map[domain.TargetKind]string{domain.TargetKindLocal: "mac-workstation", domain.TargetKindRemote: "linux-host"}[targetKind])
	if err != nil {
		t.Fatal(err)
	}
	controllerType := domain.ControllerTypeQueuedMac
	if targetKind == domain.TargetKindLocal {
		controllerType = domain.ControllerTypeLocalUser
	}
	controller, err := domain.NewControllerIdentity(controllerType, "tomasz.walczuk")
	if err != nil {
		t.Fatal(err)
	}
	payload := []byte(fmt.Sprintf(`{"environment":"dev","execution_target":{"kind":%q,"profile":%q},"operation":"submit_command","script":%q,"session_id":%q}`, targetKind, target.Profile(), script, sessionID))
	canonical, err := domain.CanonicalizeMutationRequestJSON("submit_command", payload, domain.CanonicalizationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	hash, err := domain.HashMutationRequestJSON("submit_command", canonical, domain.CanonicalizationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	ordinal := int64(1)
	return store.LocalIntentCreate{IntentID: domain.IntentID(intentID), Operation: "submit_command", ResourceID: commandID, SessionID: domain.SessionID(sessionID), CommandID: domain.CommandID(commandID), Target: target, Environment: "dev", Controller: controller, Source: domain.NewEmptySource(), RequestHash: hash, IdempotencyKey: "key-" + intentID, PayloadJSON: canonical, ScriptBytes: []byte(script), IntentOrdinal: &ordinal}
}

func p068RunIntent(t *testing.T, intentID, jobID string, targetKind domain.TargetKind) store.LocalIntentCreate {
	t.Helper()
	target, err := domain.NewExecutionTarget(targetKind, "mac-workstation")
	if err != nil {
		t.Fatal(err)
	}
	controller, err := domain.NewControllerIdentity(domain.ControllerTypeLocalUser, "tomasz.walczuk")
	if err != nil {
		t.Fatal(err)
	}
	payload := []byte(fmt.Sprintf(`{"environment":"dev","execution_target":{"kind":"local","profile":"mac-workstation"},"job_id":%q,"operation":"run","script":"echo run"}`, jobID))
	canonical, err := domain.CanonicalizeMutationRequestJSON("run", payload, domain.CanonicalizationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	hash, err := domain.HashMutationRequestJSON("run", canonical, domain.CanonicalizationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	return store.LocalIntentCreate{IntentID: domain.IntentID(intentID), Operation: "run", ResourceID: jobID, JobID: domain.JobID(jobID), Target: target, Environment: "dev", Controller: controller, Source: domain.NewEmptySource(), RequestHash: hash, IdempotencyKey: "key-" + intentID, PayloadJSON: canonical, ScriptBytes: []byte("echo run")}
}

func p068Hash(t *testing.T, raw string) domain.CanonicalHash {
	t.Helper()
	canonical, err := domain.CanonicalizeMutationRequestJSON("submit_command", []byte(raw), domain.CanonicalizationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	hash, err := domain.HashMutationRequestJSON("submit_command", canonical, domain.CanonicalizationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	return hash
}

type p068RoundTripFunc func(*http.Request) (*http.Response, error)

func (f p068RoundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}
