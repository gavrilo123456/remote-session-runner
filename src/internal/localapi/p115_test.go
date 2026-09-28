package localapi

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"remote-session-runner/src/internal/domain"
	"remote-session-runner/src/internal/store"
)

func TestP115I03LocalAuthorityFollowHandoffAndOverflowResume(t *testing.T) {
	t.Run("handoff event committed between replay and delivery", func(t *testing.T) {
		server, authority, _, client := p063Server(t)
		sessionID := p064CreateSession(t, client, `{"environment":"mac-dev","execution_target":{"kind":"local","profile":"mac-workstation"}}`, "p115-local-handoff-session")
		commandID := p065CreateAuthoritativeRunningCommand(t, client, authority, sessionID, "p115-local-handoff-command", "printf p115")
		writer := newP115ResponseWriter()
		writer.beforeHeader = func() {
			p115AppendAuthorityEvent(t, authority, commandID, "stdout", []byte("handoff\n"))
			if _, err := authority.CompleteRunningCommand(context.Background(), store.CommandTransition{
				CommandID: domain.CommandID(commandID), NextState: domain.CommandStateSucceeded, ExitCode: p115Int(0), OutputComplete: true,
			}, domain.SessionStateReady, "P115 handoff", false); err != nil {
				t.Errorf("commit terminal event during replay handoff: %v", err)
			}
		}
		server.serveHTTP(writer, httptest.NewRequest(http.MethodGet, "http://local/v1/commands/"+commandID+"/events?after=0&follow=true", nil))
		got := p115DecodeEvents(t, writer.body.Bytes())
		if writer.status != http.StatusOK || len(got) != 4 || writer.header.Get("X-Runner-View") != "authority" || writer.header.Get("X-Runner-Stale") != "false" || writer.header.Get(localAPIEventLastSequenceHeader) != "4" {
			t.Fatalf("handoff status=%d headers=%v events=%+v", writer.status, writer.header, got)
		}
		p115RequireSequences(t, got, 1, 4)
		if got[2].Type != "stdout" || got[3].Type != "command_succeeded" {
			t.Fatalf("handoff suffix=%+v", got[2:])
		}
		complete := newP115ResponseWriter()
		server.serveHTTP(complete, httptest.NewRequest(http.MethodGet, "http://local/v1/commands/"+commandID+"/events?after=4&follow=true", nil))
		if complete.status != http.StatusOK || complete.body.Len() != 0 || complete.header.Get(localAPIEventLastSequenceHeader) != "4" {
			t.Fatalf("follow at terminal cursor status=%d body=%q cursor=%q", complete.status, complete.body.String(), complete.header.Get(localAPIEventLastSequenceHeader))
		}
	})

	t.Run("slow local subscriber drains buffered prefix and resumes", func(t *testing.T) {
		server, authority, _, client := p063Server(t)
		sessionID := p064CreateSession(t, client, `{"environment":"mac-dev","execution_target":{"kind":"local","profile":"mac-workstation"}}`, "p115-local-overflow-session")
		commandID := p065CreateAuthoritativeRunningCommand(t, client, authority, sessionID, "p115-local-overflow-command", "printf p115")
		writer := newP115ResponseWriter()
		writer.blockAtWrite = 2
		writer.blocked = make(chan struct{})
		writer.release = make(chan struct{})
		var blockOnce sync.Once
		writer.blockOnce = &blockOnce
		done := make(chan struct{})
		go func() {
			server.serveHTTP(writer, httptest.NewRequest(http.MethodGet, "http://local/v1/commands/"+commandID+"/events?after=0&follow=true", nil))
			close(done)
		}()
		select {
		case <-writer.blocked:
		case <-time.After(3 * time.Second):
			close(writer.release)
			t.Fatal("local follow writer did not pause during replay")
		}
		for index := 0; index < 256; index++ {
			p115AppendAuthorityEvent(t, authority, commandID, "stdout", []byte{byte(index)})
		}
		p115AppendAuthorityEvent(t, authority, commandID, "stderr", []byte("overflow"))
		completed, err := authority.CompleteRunningCommand(context.Background(), store.CommandTransition{
			CommandID: domain.CommandID(commandID), NextState: domain.CommandStateSucceeded, ExitCode: p115Int(0), OutputComplete: true,
		}, domain.SessionStateReady, "P115 overflow", false)
		if err != nil {
			t.Fatal(err)
		}
		close(writer.release)
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Fatal("local follow did not drain the overflow prefix")
		}
		first := p115DecodeEvents(t, writer.body.Bytes())
		const cursor = int64(258)
		if len(first) != int(cursor) || writer.header.Get(localAPIEventLastSequenceHeader) != strconv.FormatInt(cursor, 10) {
			t.Fatalf("overflow response count=%d cursor=%q, want %d", len(first), writer.header.Get(localAPIEventLastSequenceHeader), cursor)
		}
		p115RequireSequences(t, first, 1, cursor)
		if completed.FinalEventSequence == nil || *completed.FinalEventSequence != 260 {
			t.Fatalf("terminal sequence=%v, want 260", completed.FinalEventSequence)
		}

		resume := newP115ResponseWriter()
		server.serveHTTP(resume, httptest.NewRequest(http.MethodGet, "http://local/v1/commands/"+commandID+"/events?after=258&follow=true", nil))
		suffix := p115DecodeEvents(t, resume.body.Bytes())
		if len(suffix) != 2 || suffix[0].Sequence != 259 || suffix[1].Sequence != 260 || suffix[1].Type != "command_succeeded" || resume.header.Get(localAPIEventLastSequenceHeader) != "260" {
			t.Fatalf("resumed suffix=%+v cursor=%q", suffix, resume.header.Get(localAPIEventLastSequenceHeader))
		}
		p115RequireSequences(t, append(first, suffix...), 1, 260)
	})
}

func TestP115I03QueuedRemoteMirrorHandoffAndSlowReader(t *testing.T) {
	server, authority, _, client := p063Server(t)
	sessionID := p064CreateSession(t, client, `{"environment":"linux-dev","execution_target":{"kind":"remote","profile":"linux-host"}}`, "p115-remote-handoff-session")
	commandID := p077APICommand(t, client, sessionID)
	intent := p115AcceptRemoteIntent(t, authority, commandID)
	when := time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)
	p115UpsertRemoteProjection(t, authority, intent, domain.CommandStateRunning, nil, "", when)
	if _, err := authority.MirrorRemoteEvents(context.Background(), []store.RemoteEventRecord{
		{CommandID: intent.CommandID, Sequence: 1, Type: "command_queued", OccurredAt: when},
		{CommandID: intent.CommandID, Sequence: 2, Type: "command_started", OccurredAt: when.Add(time.Second)},
	}); err != nil {
		t.Fatal(err)
	}
	initialEvents, err := authority.ListRemoteEvents(context.Background(), intent.CommandID, 0)
	if err != nil || len(initialEvents) != 2 {
		t.Fatalf("initial mirrored events=%+v err=%v", initialEvents, err)
	}

	writer := newP115ResponseWriter()
	writer.blockAtWrite = 1
	writer.blocked = make(chan struct{})
	writer.release = make(chan struct{})
	var blockOnce sync.Once
	writer.blockOnce = &blockOnce
	done := make(chan struct{})
	go func() {
		server.serveHTTP(writer, httptest.NewRequest(http.MethodGet, "http://local/v1/commands/"+commandID+"/events?after=0&follow=true", nil))
		close(done)
	}()
	select {
	case <-writer.blocked:
	case <-time.After(3 * time.Second):
		close(writer.release)
		t.Fatalf("queued-remote follow writer did not pause during mirrored replay: status=%d headers=%v body=%s", writer.status, writer.header, writer.body.String())
	}
	newEvents := make([]store.RemoteEventRecord, 0, 258)
	for sequence := int64(3); sequence <= 258; sequence++ {
		newEvents = append(newEvents, store.RemoteEventRecord{CommandID: intent.CommandID, Sequence: sequence, Type: "stdout", Payload: []byte{byte(sequence)}, ByteCount: 1, OccurredAt: when.Add(time.Duration(sequence) * time.Second)})
	}
	newEvents = append(newEvents,
		store.RemoteEventRecord{CommandID: intent.CommandID, Sequence: 259, Type: "stderr", Payload: []byte("tail"), ByteCount: 4, OccurredAt: when.Add(259 * time.Second)},
		store.RemoteEventRecord{CommandID: intent.CommandID, Sequence: 260, Type: "command_succeeded", OccurredAt: when.Add(260 * time.Second)},
	)
	if _, err := authority.MirrorRemoteEvents(context.Background(), newEvents); err != nil {
		t.Fatal(err)
	}
	final := int64(260)
	p115UpsertRemoteProjection(t, authority, intent, domain.CommandStateSucceeded, &final, "", when.Add(5*time.Minute))
	close(writer.release)
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("queued-remote mirror follow did not catch up from durable cursor")
	}
	events := p115DecodeEvents(t, writer.body.Bytes())
	if writer.status != http.StatusOK || writer.header.Get("X-Runner-View") != "projection" || writer.header.Get(localAPIEventLastSequenceHeader) != "260" {
		t.Fatalf("queued-remote status=%d headers=%v", writer.status, writer.header)
	}
	p115RequireSequences(t, events, 1, 260)
	finite := newP115ResponseWriter()
	server.serveHTTP(finite, httptest.NewRequest(http.MethodGet, "http://local/v1/commands/"+commandID+"/events?after=1", nil))
	finiteEvents := p115DecodeEvents(t, finite.body.Bytes())
	if finite.status != http.StatusOK || finite.header.Get(localAPIEventLastSequenceHeader) != "260" || len(finiteEvents) != 259 || finiteEvents[0].Sequence != 2 || finiteEvents[len(finiteEvents)-1].Sequence != 260 {
		t.Fatalf("queued-remote finite replay status=%d cursor=%q events=%+v", finite.status, finite.header.Get(localAPIEventLastSequenceHeader), finiteEvents)
	}
	complete := newP115ResponseWriter()
	server.serveHTTP(complete, httptest.NewRequest(http.MethodGet, "http://local/v1/commands/"+commandID+"/events?after=260&follow=true", nil))
	if complete.status != http.StatusOK || complete.body.Len() != 0 || complete.header.Get(localAPIEventLastSequenceHeader) != "260" {
		t.Fatalf("queued-remote follow at terminal cursor status=%d body=%q cursor=%q", complete.status, complete.body.String(), complete.header.Get(localAPIEventLastSequenceHeader))
	}
}

func TestP115I03QueuedRemoteGapAndRetentionReturnStructuredGone(t *testing.T) {
	for _, reason := range []string{"remote_event_gap", "retention_expired"} {
		t.Run(reason, func(t *testing.T) {
			server, authority, _, client := p063Server(t)
			sessionID := p064CreateSession(t, client, `{"environment":"linux-dev","execution_target":{"kind":"remote","profile":"linux-host"}}`, "p115-remote-error-session-"+reason)
			commandID := p077APICommand(t, client, sessionID)
			intent := p115AcceptRemoteIntent(t, authority, commandID)
			when := time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)
			final := int64(4)
			p115UpsertRemoteProjection(t, authority, intent, domain.CommandStateSucceeded, &final, reason, when)
			if reason == "remote_event_gap" {
				if _, err := authority.MirrorRemoteEvents(context.Background(), []store.RemoteEventRecord{
					{CommandID: intent.CommandID, Sequence: 1, Type: "command_queued", OccurredAt: when},
					{CommandID: intent.CommandID, Sequence: 2, Type: "command_started", OccurredAt: when.Add(time.Second)},
					{CommandID: intent.CommandID, Sequence: 3, Type: "stdout", Payload: []byte("prefix"), ByteCount: 6, OccurredAt: when.Add(2 * time.Second)},
				}); err != nil {
					t.Fatal(err)
				}
				if _, err := authority.RecordRemoteEventGap(context.Background(), store.RemoteEventGapRecord{
					CommandID: intent.CommandID, MissingFrom: 4, MissingTo: 4, AvailableSequence: 3, FinalSequence: final,
					TerminalState: domain.CommandStateSucceeded, OutputComplete: false, OutputUnavailableReason: reason, ConfirmedAt: when.Add(4 * time.Second),
				}); err != nil {
					t.Fatal(err)
				}
			}
			writer := newP115ResponseWriter()
			server.serveHTTP(writer, httptest.NewRequest(http.MethodGet, "http://local/v1/commands/"+commandID+"/events?after=0", nil))
			if writer.status != http.StatusGone || strings.Contains(writer.body.String(), "command_queued") {
				t.Fatalf("unavailable history status=%d body=%s", writer.status, writer.body.String())
			}
			var envelope errorEnvelope
			if err := json.Unmarshal(writer.body.Bytes(), &envelope); err != nil {
				t.Fatal(err)
			}
			if envelope.Code != "event_history_unavailable" || envelope.ResourceID != commandID || envelope.Details == nil || envelope.Details.OutputComplete || envelope.Details.OutputUnavailableReason != reason {
				t.Fatalf("unavailable history error=%+v, want %q", envelope, reason)
			}
		})
	}
}

func p115AcceptRemoteIntent(t *testing.T, authority *store.AuthorityStore, commandID string) store.LocalIntentRecord {
	t.Helper()
	intent, err := authority.GetLocalIntentByResource(context.Background(), "submit_command", commandID, p063Owner(t))
	if err != nil {
		t.Fatal(err)
	}
	intent, err = authority.TransitionLocalIntent(context.Background(), intent.IntentID, store.LocalIntentDispatching, "P115 dispatching")
	if err != nil {
		t.Fatal(err)
	}
	intent, err = authority.TransitionLocalIntent(context.Background(), intent.IntentID, store.LocalIntentAccepted, "P115 accepted")
	if err != nil {
		t.Fatal(err)
	}
	return intent
}

func p115UpsertRemoteProjection(t *testing.T, authority *store.AuthorityStore, intent store.LocalIntentRecord, state domain.CommandState, final *int64, reason string, observed time.Time) {
	t.Helper()
	target, err := domain.NewExecutionTarget(domain.TargetKindRemote, "linux-host")
	if err != nil {
		t.Fatal(err)
	}
	ordinal := int64(1)
	if intent.IntentOrdinal != nil {
		ordinal = *intent.IntentOrdinal
	}
	projection := store.RemoteCommandProjection{
		CommandID: intent.CommandID, SessionID: intent.SessionID, Ordinal: ordinal, State: state, FinalEventSequence: final,
		OutputComplete: state.IsTerminal() && reason == "", OutputUnavailableReason: reason, Target: target, Controller: intent.Controller,
		Environment: intent.Environment, Source: intent.Source, Capabilities: p076APICapabilities(), ObservedAt: observed,
	}
	if state == domain.CommandStateSucceeded {
		projection.ExitCode = p115Int(0)
	}
	if _, err := authority.UpsertRemoteCommandProjection(context.Background(), projection); err != nil {
		t.Fatal(err)
	}
}

func p115AppendAuthorityEvent(t *testing.T, authority *store.AuthorityStore, commandID, eventType string, payload []byte) {
	t.Helper()
	if _, err := authority.AppendCommandEvent(context.Background(), store.CommandEventAppend{
		CommandID: domain.CommandID(commandID), Type: eventType, Payload: payload, ByteCount: int64(len(payload)),
	}); err != nil {
		t.Fatal(err)
	}
}

type p115Event struct {
	Sequence int64  `json:"sequence"`
	Type     string `json:"type"`
}

func p115DecodeEvents(t *testing.T, body []byte) []p115Event {
	t.Helper()
	var events []p115Event
	for _, line := range bytes.Split(bytes.TrimSpace(body), []byte{'\n'}) {
		if len(line) == 0 {
			continue
		}
		var event p115Event
		if err := json.Unmarshal(line, &event); err != nil {
			t.Fatalf("decode event frame %q: %v", line, err)
		}
		events = append(events, event)
	}
	return events
}

func p115RequireSequences(t *testing.T, events []p115Event, first, last int64) {
	t.Helper()
	if int64(len(events)) != last-first+1 {
		t.Fatalf("event count=%d, want %d..%d (%d records)", len(events), first, last, last-first+1)
	}
	for index, event := range events {
		if event.Sequence != first+int64(index) {
			t.Fatalf("event[%d] sequence=%d, want %d", index, event.Sequence, first+int64(index))
		}
	}
}

func p115Int(value int) *int { return &value }

type p115ResponseWriter struct {
	header       http.Header
	body         bytes.Buffer
	status       int
	writeCount   int
	blockAtWrite int
	blocked      chan struct{}
	release      chan struct{}
	blockOnce    *sync.Once
	beforeHeader func()
}

func newP115ResponseWriter() *p115ResponseWriter {
	return &p115ResponseWriter{header: make(http.Header)}
}

func (w *p115ResponseWriter) Header() http.Header { return w.header }

func (w *p115ResponseWriter) WriteHeader(status int) {
	if w.beforeHeader != nil {
		hook := w.beforeHeader
		w.beforeHeader = nil
		hook()
	}
	w.status = status
}

func (w *p115ResponseWriter) Write(data []byte) (int, error) {
	w.writeCount++
	if w.blockAtWrite > 0 && w.writeCount == w.blockAtWrite {
		w.blockOnce.Do(func() {
			close(w.blocked)
			<-w.release
		})
	}
	return w.body.Write(data)
}

func (w *p115ResponseWriter) Flush() {}

var _ http.Flusher = (*p115ResponseWriter)(nil)
