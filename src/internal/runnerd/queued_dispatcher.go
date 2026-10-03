package runnerd

import "remote-session-runner/src/internal/queueworker"

// RunnerDispatcher remains the runnerd-facing name for the shared durable
// queue worker. The alias preserves the existing ingress and shutdown wiring
// while Mac runnerlocald can use the same implementation in a later phase.
type RunnerDispatcher = queueworker.Worker

// RunnerDispatcherOptions remains the runnerd-facing configuration name for
// the shared durable queue worker.
type RunnerDispatcherOptions = queueworker.Options

// ErrRunnerDispatcherConfiguration preserves the stable runnerd error used by
// existing callers and tests.
var ErrRunnerDispatcherConfiguration = queueworker.ErrConfiguration

// NewRunnerDispatcher constructs the shared queue worker through runnerd's
// compatibility facade.
func NewRunnerDispatcher(options RunnerDispatcherOptions) (*RunnerDispatcher, error) {
	return queueworker.New(options)
}
