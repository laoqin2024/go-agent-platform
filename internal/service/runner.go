package service

import "context"

// Runner is the core loop that should stop when ctx is cancelled.
type Runner interface {
	Run(ctx context.Context)
}
