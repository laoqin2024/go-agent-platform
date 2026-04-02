package service

import (
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"
)

type testRunner struct {
	stopped chan struct{}
}

func (r *testRunner) Run(ctx context.Context) {
	<-ctx.Done()
	close(r.stopped)
}

func TestAgentService_StartStop(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(testDiscard{}, &slog.HandlerOptions{Level: slog.LevelDebug}))
	r := &testRunner{stopped: make(chan struct{})}

	svc := NewAgentService(r, logger, WithStopTimeout(2*time.Second))

	if err := svc.Start(nil); err != nil {
		t.Fatalf("Start returned error: %v", err)
	}

	stopErr := svc.Stop(nil)
	if stopErr != nil {
		t.Fatalf("Stop returned error: %v", stopErr)
	}

	select {
	case <-r.stopped:
		// ok
	case <-time.After(2 * time.Second):
		t.Fatal("runner did not stop in time")
	}
}

type testDiscard struct{}

func (testDiscard) Write(p []byte) (n int, err error) {
	if len(p) == 0 {
		return 0, nil
	}
	return len(p), nil
}

func TestAgentService_StopTimeout(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(testDiscard{}, &slog.HandlerOptions{Level: slog.LevelDebug}))

	// Runner never returns even if ctx is cancelled.
	r := runnerNeverStops{stopped: make(chan struct{})}
	svc := NewAgentService(&r, logger, WithStopTimeout(100*time.Millisecond))

	if err := svc.Start(nil); err != nil {
		t.Fatalf("Start returned error: %v", err)
	}

	err := svc.Stop(nil)
	if err == nil || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected deadline exceeded error, got: %v", err)
	}
}

type runnerNeverStops struct {
	stopped chan struct{}
}

func (r *runnerNeverStops) Run(ctx context.Context) {
	// ignore ctx cancellation
	time.Sleep(500 * time.Millisecond)
	close(r.stopped)
}
