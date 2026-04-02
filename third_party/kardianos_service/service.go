package service

import (
	"errors"
	"os"
	"os/signal"
	"syscall"
)

// Service is a placeholder interface for the service manager handle.
// The real github.com/kardianos/service provides additional methods.
type Service interface{}

// Interface matches the subset of the kardianos/service API that our agent wrapper uses.
type Interface interface {
	Start(Service) error
	Stop(Service) error
}

type Config struct {
	Name        string
	DisplayName string
	Description string
}

type runner struct {
	program Interface
}

// New returns a minimal runner that starts the program and stops it on SIGINT/SIGTERM.
// This exists only to allow offline compilation/execution in constrained environments.
func New(p Interface, _ *Config) (*runner, error) {
	if p == nil {
		return nil, errors.New("service: nil program")
	}
	return &runner{program: p}, nil
}

func (r *runner) Run() error {
	// Start is expected to be non-blocking; our wrapper does the work in a goroutine.
	if err := r.program.Start(nil); err != nil {
		return err
	}

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
	<-sigCh
	return r.program.Stop(nil)
}
