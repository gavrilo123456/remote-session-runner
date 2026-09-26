package store

import (
	"context"
	"errors"
	"sort"
	"sync"

	"remote-session-runner/src/internal/domain"
)

var (
	// ErrSubscriberOverflow means a subscriber's bounded queue could not accept
	// another event. The last successfully queued sequence remains resumable.
	ErrSubscriberOverflow = errors.New("command event subscriber buffer overflow")
	// ErrInvalidSubscriber means a subscription request is malformed.
	ErrInvalidSubscriber = errors.New("invalid command event subscriber")
)

// CommandEventSubscription is a bounded command-event stream. Events are
// replayed from the requested cursor before buffered live events are handed
// off. A closed Errors channel with ErrSubscriberOverflow means the caller
// should resume from LastSequence.
type CommandEventSubscription struct {
	owner     *AuthorityStore
	commandID domain.CommandID
	capacity  int
	events    chan CommandEventRecord
	errors    chan error

	mu           sync.Mutex
	replaying    bool
	pending      map[int64]CommandEventRecord
	lastSequence int64
	closed       bool
}

// SubscribeCommandEvents registers a bounded subscriber before reading its
// replay range. Events committed while the replay query is in flight are
// buffered and sequence de-duplicated at the replay/live handoff.
func (s *AuthorityStore) SubscribeCommandEvents(ctx context.Context, id domain.CommandID, afterSequence int64, capacity int) (*CommandEventSubscription, error) {
	validatedID, err := domain.NewCommandID(string(id))
	if err != nil {
		return nil, err
	}
	if afterSequence < 0 || capacity <= 0 {
		return nil, ErrInvalidSubscriber
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	subscription := &CommandEventSubscription{
		owner:        s,
		commandID:    validatedID,
		capacity:     capacity,
		events:       make(chan CommandEventRecord, capacity),
		errors:       make(chan error, 1),
		replaying:    true,
		pending:      make(map[int64]CommandEventRecord),
		lastSequence: afterSequence,
	}
	s.addSubscriber(subscription)
	if done := ctx.Done(); done != nil {
		go func() {
			<-done
			subscription.Close()
		}()
	}

	replay, err := s.ReplayCommandEvents(ctx, validatedID, afterSequence)
	if err != nil {
		s.removeSubscriber(subscription)
		subscription.terminate(nil)
		return nil, err
	}
	if !subscription.replay(replay) {
		s.removeSubscriber(subscription)
		return subscription, nil
	}
	subscription.finishReplay()
	return subscription, nil
}

// Events returns the bounded event channel. The channel closes when Close is
// called or when the subscriber overflows.
func (s *CommandEventSubscription) Events() <-chan CommandEventRecord { return s.events }

// Errors returns the terminal subscriber error channel. It contains
// ErrSubscriberOverflow when the bounded queue disconnects a slow consumer.
func (s *CommandEventSubscription) Errors() <-chan error { return s.errors }

// LastSequence is the highest event sequence successfully queued for the
// subscriber. It is the cursor to use when resuming after overflow.
func (s *CommandEventSubscription) LastSequence() int64 {
	if s == nil {
		return 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lastSequence
}

// Close removes the subscriber and closes its channels. It is idempotent.
func (s *CommandEventSubscription) Close() {
	if s == nil {
		return
	}
	s.owner.removeSubscriber(s)
	s.terminate(nil)
}

func (s *AuthorityStore) addSubscriber(subscription *CommandEventSubscription) {
	s.subscribersMu.Lock()
	defer s.subscribersMu.Unlock()
	byCommand := s.subscribersByID[subscription.commandID]
	if byCommand == nil {
		byCommand = make(map[*CommandEventSubscription]struct{})
		s.subscribersByID[subscription.commandID] = byCommand
	}
	byCommand[subscription] = struct{}{}
}

func (s *AuthorityStore) removeSubscriber(subscription *CommandEventSubscription) {
	s.subscribersMu.Lock()
	defer s.subscribersMu.Unlock()
	byCommand := s.subscribersByID[subscription.commandID]
	delete(byCommand, subscription)
	if len(byCommand) == 0 {
		delete(s.subscribersByID, subscription.commandID)
	}
}

func (s *AuthorityStore) publishCommandEvent(event CommandEventRecord) {
	s.subscribersMu.Lock()
	byCommand := s.subscribersByID[event.CommandID]
	subscribers := make([]*CommandEventSubscription, 0, len(byCommand))
	for subscription := range byCommand {
		subscribers = append(subscribers, subscription)
	}
	s.subscribersMu.Unlock()
	for _, subscription := range subscribers {
		subscription.publish(event)
	}
}

func (s *CommandEventSubscription) replay(events []CommandEventRecord) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return false
	}
	for _, event := range events {
		if !s.enqueueLocked(event) {
			return false
		}
	}
	return true
}

func (s *CommandEventSubscription) finishReplay() {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.replaying = false
	sequences := make([]int64, 0, len(s.pending))
	for sequence := range s.pending {
		sequences = append(sequences, sequence)
	}
	sort.Slice(sequences, func(i, j int) bool { return sequences[i] < sequences[j] })
	for _, sequence := range sequences {
		if !s.enqueueLocked(s.pending[sequence]) {
			break
		}
	}
	s.pending = nil
	closed := s.closed
	s.mu.Unlock()
	if closed {
		s.owner.removeSubscriber(s)
	}
}

func (s *CommandEventSubscription) publish(event CommandEventRecord) {
	s.mu.Lock()
	if s.closed || event.Sequence <= s.lastSequence {
		s.mu.Unlock()
		return
	}
	if s.replaying {
		if _, exists := s.pending[event.Sequence]; !exists {
			if len(s.pending) >= s.capacity {
				s.terminateLocked(ErrSubscriberOverflow)
				s.mu.Unlock()
				s.owner.removeSubscriber(s)
				return
			}
			s.pending[event.Sequence] = cloneCommandEvent(event)
		}
		s.mu.Unlock()
		return
	}
	if !s.enqueueLocked(event) {
		s.mu.Unlock()
		s.owner.removeSubscriber(s)
		return
	}
	s.mu.Unlock()
}

func (s *CommandEventSubscription) enqueueLocked(event CommandEventRecord) bool {
	if s.closed || event.Sequence <= s.lastSequence {
		return !s.closed
	}
	select {
	case s.events <- cloneCommandEvent(event):
		s.lastSequence = event.Sequence
		return true
	default:
		s.terminateLocked(ErrSubscriberOverflow)
		return false
	}
}

func (s *CommandEventSubscription) terminate(err error) {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.terminateLocked(err)
	s.mu.Unlock()
}

func (s *CommandEventSubscription) terminateLocked(err error) {
	if s.closed {
		return
	}
	s.closed = true
	if err != nil {
		s.errors <- err
	}
	close(s.events)
	close(s.errors)
}

func cloneCommandEvent(event CommandEventRecord) CommandEventRecord {
	event.Payload = append([]byte(nil), event.Payload...)
	return event
}
