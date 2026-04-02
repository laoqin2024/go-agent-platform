package service

import (
	"context"
	"log/slog"
	"sync"
	"time"

	kservice "github.com/kardianos/service"
)

type AgentService struct {
	logger *slog.Logger
	runner Runner
	dispatcher Runner

	stopTimeout time.Duration

	mu     sync.Mutex
	cancel context.CancelFunc
	exitCh chan struct{}
}

type AgentOption func(*AgentService)

func WithStopTimeout(d time.Duration) AgentOption {
	return func(s *AgentService) {
		if d > 0 {
			s.stopTimeout = d
		}
	}
}

func NewAgentService(runner Runner, logger *slog.Logger, opts ...AgentOption) *AgentService {
	s := &AgentService{
		logger:      logger,
		runner:      runner,
		stopTimeout: 10 * time.Second,
	}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

// WithDispatcher configures an optional dispatcher runner started/stopped with the service.
func WithDispatcher(dispatcher Runner) AgentOption {
	return func(s *AgentService) {
		s.dispatcher = dispatcher
	}
}

// Start must be non-blocking. The run loop runs in a dedicated goroutine.
func (s *AgentService) Start(_ kservice.Service) error {
	s.mu.Lock()

	// Protect against double Start calls.
	if s.cancel != nil {
		s.mu.Unlock()
		s.logger.Warn("agent service already started")
		return nil
	}

	exitCh := make(chan struct{})
	s.exitCh = exitCh

	runCtx, cancel := context.WithCancel(context.Background())
	s.cancel = cancel
	dispatcher := s.dispatcher
	runner := s.runner
	s.mu.Unlock()

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		s.logger.Info("agent service starting collection engine")
		runner.Run(runCtx)
		s.logger.Info("collection engine goroutine exited")
	}()

	if dispatcher != nil {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s.logger.Info("agent service starting data dispatcher")
			dispatcher.Run(runCtx)
			s.logger.Info("data dispatcher goroutine exited")
		}()
	}

	go func() {
		wg.Wait()
		close(exitCh)
		s.logger.Info("agent service goroutines exited")
	}()

	return nil
}

// Stop should gracefully shut down and wait for the exit channel.
func (s *AgentService) Stop(_ kservice.Service) error {
	s.mu.Lock()
	cancel := s.cancel
	exitCh := s.exitCh
	s.cancel = nil
	s.exitCh = nil
	s.mu.Unlock()

	if cancel == nil || exitCh == nil {
		return nil
	}

	// 1) Send stop signal via context cancel.
	cancel()

	// 2) Wait for the goroutine to exit with a deadline.
	ctx, cancelWait := context.WithTimeout(context.Background(), s.stopTimeout)
	defer cancelWait()

	select {
	case <-exitCh:
		s.logger.Info("agent service stopped cleanly")
		return nil
	case <-ctx.Done():
		s.logger.Error("agent service stop timed out", "err", ctx.Err())
		return ctx.Err()
	}
}
