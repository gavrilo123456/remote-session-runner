package runnerd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"sync"
	"testing"
	"time"

	"remote-session-runner/src/internal/domain"
	"remote-session-runner/src/internal/store"
)

func TestP114I03DirectFollowStreamsReplayAndHandoffEvents(t *testing.T) {
	controller := p107DirectController(t, "tomasz.walczuk")
	service, authority, _ := p112NewService(t, &p109Runtime{generation: "p114-handoff-generation"})
	handler, err := NewDirectHTTPSAPIHandler(service)
	if err != nil {
		t.Fatal(err)
	}
	commandID := p114RunningCommand(t, handler, authority, controller, "handoff")
	hookErrors := make(chan error, 2)

	server := p114StartHTTPServer(t, handler, controller, func(writer http.ResponseWriter) *p114HTTPResponseWriter {
		return &p114HTTPResponseWriter{ResponseWriter: writer, beforeWriteHeader: func() {
			if _, err := authority.AppendCommandEvent(context.Background(), store.CommandEventAppend{
				CommandID: commandID, Type: "stdout", Payload: []byte("live\n"), ByteCount: 5,
			}); err != nil {
				hookErrors <- fmt.Errorf("append event after replay query: %w", err)
			}
			if _, err := authority.TransitionCommand(context.Background(), store.CommandTransition{
				CommandID: commandID, NextState: domain.CommandStateSucceeded, ExitCode: p114IntPointer(0), OutputComplete: true,
			}); err != nil {
				hookErrors <- fmt.Errorf("complete command after replay query: %w", err)
			}
		}}
	})

	response, err := server.Client().Get(server.URL + "/v1/commands/" + string(commandID) + "/events?after=0&follow=true")
	if err != nil {
		t.Fatalf("direct follow request: %v", err)
	}
	body, err := io.ReadAll(response.Body)
	if err != nil {
		_ = response.Body.Close()
		t.Fatalf("read direct follow response: %v", err)
	}
	if err := response.Body.Close(); err != nil {
		t.Fatalf("close direct follow response: %v", err)
	}
	if response.StatusCode != http.StatusOK {
		t.Fatalf("direct follow status = %d, body = %s", response.StatusCode, body)
	}
	if len(hookErrors) > 0 {
		t.Fatal(<-hookErrors)
	}
	events := p113DecodeEventLines(t, body)
	if len(events) != 4 {
		t.Fatalf("direct follow returned %d events, want replay [1,2] plus live [3,4]: %#v", len(events), events)
	}
	for index, event := range events {
		if event.Sequence != int64(index+1) || event.CommandID != string(commandID) {
			t.Fatalf("event[%d] breaks the replay/live handoff: %#v", index, event)
		}
	}
	if events[2].Type != "stdout" || events[3].Type != "command_succeeded" {
		t.Fatalf("live handoff suffix = %#v, want stdout then terminal", events[2:])
	}
	if response.Trailer.Get(directEventsLastSequenceHeader) != "4" {
		t.Fatalf("follow cursor trailer = %q, want 4", response.Trailer.Get(directEventsLastSequenceHeader))
	}
	completed, err := server.Client().Get(server.URL + "/v1/commands/" + string(commandID) + "/events?after=4&follow=true")
	if err != nil {
		t.Fatalf("follow at terminal cursor: %v", err)
	}
	completedBody, err := io.ReadAll(completed.Body)
	if err != nil {
		_ = completed.Body.Close()
		t.Fatalf("read follow at terminal cursor: %v", err)
	}
	if err := completed.Body.Close(); err != nil {
		t.Fatalf("close follow at terminal cursor: %v", err)
	}
	if completed.StatusCode != http.StatusOK || len(completedBody) != 0 || completed.Header.Get(directEventsLastSequenceHeader) != "4" {
		t.Fatalf("follow at terminal cursor status=%d body=%q cursor=%q; want empty 200 at cursor 4", completed.StatusCode, completedBody, completed.Header.Get(directEventsLastSequenceHeader))
	}
}

func TestP114I03DirectFollowOverflowReturnsWrittenCursorAndResumes(t *testing.T) {
	controller := p107DirectController(t, "tomasz.walczuk")
	service, authority, _ := p112NewService(t, &p109Runtime{generation: "p114-overflow-generation"})
	handler, err := NewDirectHTTPSAPIHandler(service)
	if err != nil {
		t.Fatal(err)
	}
	commandID := p114RunningCommand(t, handler, authority, controller, "overflow")

	blocked := make(chan struct{})
	release := make(chan struct{})
	var blockOnce sync.Once
	server := p114StartHTTPServer(t, handler, controller, func(writer http.ResponseWriter) *p114HTTPResponseWriter {
		return &p114HTTPResponseWriter{
			ResponseWriter: writer, blockAtWrite: 2, blocked: blocked, release: release, blockOnce: &blockOnce,
		}
	})
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()

	response, err := server.Client().Get(server.URL + "/v1/commands/" + string(commandID) + "/events?after=0&follow=true")
	if err != nil {
		t.Fatalf("direct follow request: %v", err)
	}
	select {
	case <-blocked:
	case <-time.After(3 * time.Second):
		_ = response.Body.Close()
		t.Fatal("follow writer did not pause at the second replay event")
	}

	const queuedWhileBlocked = directEventsSubscriberCapacity
	for index := 0; index < queuedWhileBlocked; index++ {
		payload := []byte{byte(index)}
		if _, err := authority.AppendCommandEvent(context.Background(), store.CommandEventAppend{
			CommandID: commandID, Type: "stdout", Payload: payload, ByteCount: int64(len(payload)),
		}); err != nil {
			t.Fatalf("append queued live event %d: %v", index, err)
		}
	}
	if _, err := authority.AppendCommandEvent(context.Background(), store.CommandEventAppend{
		CommandID: commandID, Type: "stderr", Payload: []byte("overflow"), ByteCount: int64(len("overflow")),
	}); err != nil {
		t.Fatalf("append event that overflows subscriber: %v", err)
	}
	completed, err := authority.TransitionCommand(context.Background(), store.CommandTransition{
		CommandID: commandID, NextState: domain.CommandStateSucceeded, ExitCode: p114IntPointer(0), OutputComplete: true,
	})
	if err != nil {
		t.Fatalf("complete command after subscriber overflow: %v", err)
	}
	wantFinalSequence := int64(directEventsSubscriberCapacity + 4)
	if completed.FinalEventSequence == nil || *completed.FinalEventSequence != wantFinalSequence {
		t.Fatalf("terminal sequence = %#v, want %d", completed.FinalEventSequence, wantFinalSequence)
	}
	close(release)

	firstBody, err := io.ReadAll(response.Body)
	if err != nil {
		_ = response.Body.Close()
		t.Fatalf("read overflowed follow response: %v", err)
	}
	if err := response.Body.Close(); err != nil {
		t.Fatalf("close overflowed follow response: %v", err)
	}
	if response.StatusCode != http.StatusOK {
		t.Fatalf("overflowed follow status = %d, body = %s", response.StatusCode, firstBody)
	}
	firstEvents := p113DecodeEventLines(t, firstBody)
	wantCursor := int64(directEventsSubscriberCapacity + 2)
	if len(firstEvents) != int(wantCursor) || response.Trailer.Get(directEventsLastSequenceHeader) != strconv.FormatInt(wantCursor, 10) {
		t.Fatalf("overflow stream delivered %d events and cursor %q; want %d", len(firstEvents), response.Trailer.Get(directEventsLastSequenceHeader), wantCursor)
	}
	for index, event := range firstEvents {
		if event.Sequence != int64(index+1) {
			t.Fatalf("overflow prefix event[%d] sequence=%d, want %d", index, event.Sequence, index+1)
		}
	}

	resumed, err := server.Client().Get(server.URL + "/v1/commands/" + string(commandID) + "/events?after=" + strconv.FormatInt(wantCursor, 10) + "&follow=true")
	if err != nil {
		t.Fatalf("resume direct follow request: %v", err)
	}
	resumedBody, err := io.ReadAll(resumed.Body)
	if err != nil {
		_ = resumed.Body.Close()
		t.Fatalf("read resumed follow response: %v", err)
	}
	if err := resumed.Body.Close(); err != nil {
		t.Fatalf("close resumed follow response: %v", err)
	}
	resumedEvents := p113DecodeEventLines(t, resumedBody)
	if len(resumedEvents) != 2 || resumedEvents[0].Sequence != wantCursor+1 || resumedEvents[1].Sequence != wantCursor+2 || resumedEvents[1].Type != "command_succeeded" {
		t.Fatalf("resumed event suffix = %#v, want sequences %d and %d ending terminal", resumedEvents, wantCursor+1, wantCursor+2)
	}
	if resumed.Trailer.Get(directEventsLastSequenceHeader) != strconv.FormatInt(*completed.FinalEventSequence, 10) {
		t.Fatalf("resumed cursor trailer = %q, want %d", resumed.Trailer.Get(directEventsLastSequenceHeader), *completed.FinalEventSequence)
	}
	combined := append(firstEvents, resumedEvents...)
	if int64(len(combined)) != *completed.FinalEventSequence {
		t.Fatalf("combined replay/follow history has %d events, terminal cursor is %d", len(combined), *completed.FinalEventSequence)
	}
	for index, event := range combined {
		if event.Sequence != int64(index+1) {
			t.Fatalf("combined history event[%d] sequence=%d, want %d", index, event.Sequence, index+1)
		}
	}
}

func TestP114DirectEventCursorParsesStrictFollowBoolean(t *testing.T) {
	for _, test := range []struct {
		query string
		want  bool
	}{
		{query: "", want: false},
		{query: "follow=false", want: false},
		{query: "after=7&follow=true", want: true},
	} {
		parsed, err := url.ParseQuery(test.query)
		if err != nil {
			t.Fatalf("parse query %q: %v", test.query, err)
		}
		_, follow, err := directEventCursor(parsed)
		if err != nil || follow != test.want {
			t.Errorf("query %q parsed follow=%t err=%v, want %t", test.query, follow, err, test.want)
		}
	}
}

func p114RunningCommand(t *testing.T, handler http.Handler, authority *store.AuthorityStore, controller domain.ControllerIdentity, suffix string) domain.CommandID {
	t.Helper()
	created := p107Do(handler, controller, true, http.MethodPost, "/v1/sessions", []byte(p107CreateBody), "p114-create-"+suffix)
	if created.Code != http.StatusAccepted {
		t.Fatalf("create session status = %d, body = %s", created.Code, created.Body.String())
	}
	var session directSessionAcceptance
	p107Decode(t, created, &session)
	sessionID := domain.SessionID(session.SessionID)
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		current, err := authority.GetSession(context.Background(), sessionID)
		if err == nil && current.State == domain.SessionStateReady {
			break
		}
		if err != nil && !errors.Is(err, store.ErrSessionNotFound) {
			t.Fatalf("read session while waiting for ready: %v", err)
		}
		time.Sleep(5 * time.Millisecond)
	}
	current, err := authority.GetSession(context.Background(), sessionID)
	if err != nil || current.State != domain.SessionStateReady {
		t.Fatalf("session state = %v, err = %v; want ready", current.State, err)
	}

	commandID := domain.CommandID("cmd-p114-" + suffix)
	script := "p114 controlled output"
	request := fmt.Sprintf(`{"operation":"submit_command","session_id":%q,"script":%q}`, string(sessionID), script)
	hash, err := domain.HashMutationRequestJSON("submit_command", []byte(request), domain.CanonicalizationOptions{})
	if err != nil {
		t.Fatalf("hash test command request: %v", err)
	}
	if _, _, err := authority.AcceptCommand(context.Background(), store.CommandAcceptance{
		CommandID: commandID, SessionID: sessionID, IdempotencyKey: "p114-submit-" + suffix,
		RequestHash: hash, IdempotencyRetention: store.DefaultSessionIdempotencyRetention, Script: script, Timeout: time.Minute,
	}); err != nil {
		t.Fatalf("accept test command: %v", err)
	}
	if _, err := authority.TransitionCommand(context.Background(), store.CommandTransition{CommandID: commandID, NextState: domain.CommandStateRunning}); err != nil {
		t.Fatalf("start test command: %v", err)
	}
	return commandID
}

func p114StartHTTPServer(t *testing.T, handler http.Handler, controller domain.ControllerIdentity, wrap func(http.ResponseWriter) *p114HTTPResponseWriter) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		request = request.WithContext(context.WithValue(request.Context(), directPrincipalContextKey{}, DirectPrincipal{Controller: controller}))
		handler.ServeHTTP(wrap(writer), request)
	}))
	t.Cleanup(server.Close)
	return server
}

type p114HTTPResponseWriter struct {
	http.ResponseWriter
	beforeWriteHeader func()
	writeCount        int
	blockAtWrite      int
	blocked           chan struct{}
	release           chan struct{}
	blockOnce         *sync.Once
}

func (w *p114HTTPResponseWriter) WriteHeader(status int) {
	if w.beforeWriteHeader != nil {
		hook := w.beforeWriteHeader
		w.beforeWriteHeader = nil
		hook()
	}
	w.ResponseWriter.WriteHeader(status)
}

func (w *p114HTTPResponseWriter) Write(value []byte) (int, error) {
	w.writeCount++
	if w.blockAtWrite > 0 && w.writeCount == w.blockAtWrite {
		w.blockOnce.Do(func() {
			close(w.blocked)
			<-w.release
		})
	}
	return w.ResponseWriter.Write(value)
}

func (w *p114HTTPResponseWriter) Flush() {
	if flusher, ok := w.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

func p114IntPointer(value int) *int { return &value }
