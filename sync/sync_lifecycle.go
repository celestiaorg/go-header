package sync

import (
	"context"
	"errors"
	"fmt"
	"sync"
)

var (
	// ErrSyncerStarting is returned when Start is called while another Start is in progress.
	ErrSyncerStarting = errors.New("sync: syncer is starting")
	// ErrSyncerRunning is returned when Start is called on a running Syncer.
	ErrSyncerRunning = errors.New("sync: syncer is already running")
	// ErrSyncerStopping is returned when Start is called before the current Stop completes.
	ErrSyncerStopping = errors.New("sync: syncer is stopping")
)

type lifecycleState uint8

const (
	stateStopped lifecycleState = iota
	stateStarting
	stateRunning
	stateStopping
)

// syncRun owns every resource whose lifetime is limited to one Start/Stop cycle.
// A goroutine must retain its run pointer instead of reloading s.run, because a
// later Start may install a different run before an older caller finishes.
type syncRun struct {
	ctx    context.Context
	cancel context.CancelFunc
	done   chan struct{}
	err    error
	finish sync.Once
}

// Lifecycle invariants:
//   - stateStopped implies run == nil.
//   - Every other state implies run != nil.
//   - At most one run may be starting, running, or stopping at a time.
//   - Only the owner of a run closes run.done.
//   - run.done is closed after the Syncer has published stateStopped.
//   - Stop cancellation is idempotent; completion is observed through run.done.

// Start initializes and starts one synchronization run. It returns a lifecycle
// error if another run is starting, running, or stopping. Canceling ctx cancels
// initialization and guarantees that no sync loop is left running.
func (s *Syncer[H]) Start(ctx context.Context) error {
	run, err := s.beginStart()
	if err != nil {
		return err
	}

	// A canceled Start must not leave initialization running in the background.
	stopCallerCancel := context.AfterFunc(ctx, run.cancel)
	defer stopCallerCancel()

	if err := s.metrics.Start(); err != nil {
		s.finishRun(run)
		return fmt.Errorf("starting metrics: %w", err)
	}

	if err := s.installVerifier(); err != nil {
		s.finishRun(run)
		return err
	}

	if _, err := s.Head(run.ctx); err != nil {
		s.finishRun(run)
		return fmt.Errorf("error getting latest head during Start: %w", err)
	}

	// Stop the caller-cancellation callback before publishing a running service.
	// If it already fired, this run was canceled and must not start its loop.
	if !stopCallerCancel() || ctx.Err() != nil || run.ctx.Err() != nil {
		s.finishRun(run)
		if err := ctx.Err(); err != nil {
			return err
		}
		return run.ctx.Err()
	}

	s.lifecycleMu.Lock()
	if s.run != run || s.lifecycleState != stateStarting {
		s.lifecycleMu.Unlock()
		s.finishRun(run)
		return context.Canceled
	}
	s.lifecycleState = stateRunning
	s.lifecycleMu.Unlock()

	go s.runSyncLoop(run)
	s.signalStarted()
	return nil
}

func (s *Syncer[H]) beginStart() (*syncRun, error) {
	s.lifecycleMu.Lock()
	defer s.lifecycleMu.Unlock()

	switch s.lifecycleState {
	case stateStarting:
		return nil, ErrSyncerStarting
	case stateRunning:
		return nil, ErrSyncerRunning
	case stateStopping:
		return nil, ErrSyncerStopping
	case stateStopped:
	}

	//nolint:gosec // G118: run.cancel is called by Stop or finishRun.
	runCtx, runCancel := context.WithCancel(context.Background())
	run := &syncRun{
		ctx:    runCtx,
		cancel: runCancel,
		done:   make(chan struct{}),
	}
	s.run = run
	s.lifecycleState = stateStarting
	return run, nil
}

// Stop requests cancellation and waits for the current run to finish. Stop is
// idempotent when the Syncer is stopped and safe for concurrent callers. If ctx
// expires, the run remains in stateStopping and continues its cleanup; a later
// Stop call may wait on the same run.
func (s *Syncer[H]) Stop(ctx context.Context) error {
	s.lifecycleMu.Lock()
	if s.lifecycleState == stateStopped {
		s.lifecycleMu.Unlock()
		return nil
	}

	run := s.run
	s.lifecycleState = stateStopping
	s.lifecycleMu.Unlock()

	run.cancel()
	select {
	case <-run.done:
		return run.err
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *Syncer[H]) runSyncLoop(run *syncRun) {
	defer s.finishRun(run)
	s.syncLoop(run.ctx)
}

// finishRun is the single completion path for both failed starts and running
// loops. The identity check prevents an obsolete run from clearing newer state.
func (s *Syncer[H]) finishRun(run *syncRun) {
	run.finish.Do(func() {
		run.cancel()
		run.err = s.metrics.Close()

		s.lifecycleMu.Lock()
		if s.run == run {
			s.run = nil
			s.lifecycleState = stateStopped
		}
		s.lifecycleMu.Unlock()

		close(run.done)
	})
}

func (s *Syncer[H]) installVerifier() error {
	s.verifierMu.Lock()
	defer s.verifierMu.Unlock()
	if s.verifierInstalled {
		return nil
	}

	// The verifier belongs to the Syncer instance, not to an individual run.
	// Subscriber implementations allow it to be installed only once.
	err := s.sub.SetVerifier(func(ctx context.Context, h H) error {
		// During subjective initialization Head is requested before Tail is stored.
		// Stall subscription delivery until the first successful Start closes started.
		select {
		case <-s.started:
		case <-ctx.Done():
			return ctx.Err()
		}

		if err := s.incomingNetworkHead(ctx, h); err != nil {
			return err
		}
		if _, err := s.subjectiveTail(ctx, h); err != nil {
			log.Errorw("subjective tail", "head", h.Height(), "err", err)
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("installing subscriber verifier: %w", err)
	}

	s.verifierInstalled = true
	return nil
}

func (s *Syncer[H]) signalStarted() {
	select {
	case <-s.started:
	default:
		close(s.started)
	}
}
