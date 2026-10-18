package deliver

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/zapsaang/push-transfer/internal/model"
)

// ErrAllTargetsFailed is returned when every configured target fails to deliver.
var ErrAllTargetsFailed = errors.New("all delivery targets failed")

// Target defines the interface for delivery targets.
type Target interface {
	Name() string
	Send(ctx context.Context, msg *model.Message) error
}

// DispatcherOptions configures the delivery Dispatcher.
type DispatcherOptions struct {
	Targets []Target
	Timeout time.Duration
	Logger  *slog.Logger
}

// Dispatcher concurrently dispatches messages to registered delivery targets.
type Dispatcher struct {
	targets []Target
	timeout time.Duration
	logger  *slog.Logger
}

// NewDispatcher creates a new Dispatcher with the given options.
func NewDispatcher(opts DispatcherOptions) *Dispatcher {
	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}
	return &Dispatcher{
		targets: opts.Targets,
		timeout: timeout,
		logger:  logger,
	}
}

// Deliver concurrently sends msg to all registered targets.
// If any target succeeds, it returns nil.
// If all targets fail, ErrAllTargetsFailed is returned.
// If context expires before targets finish, ctx.Err() is returned.
func (d *Dispatcher) Deliver(ctx context.Context, msg *model.Message) error {
	if len(d.targets) == 0 {
		return nil
	}

	var cancel context.CancelFunc
	if d.timeout > 0 {
		ctx, cancel = context.WithTimeout(ctx, d.timeout)
		defer cancel()
	}

	type targetResult struct {
		name    string
		err     error
		elapsed time.Duration
	}

	results := make(chan targetResult, len(d.targets))
	for _, t := range d.targets {
		target := t
		msgClone := msg.Clone()
		go func() {
			start := time.Now()
			err := target.Send(ctx, msgClone)
			elapsed := time.Since(start)
			results <- targetResult{name: target.Name(), err: err, elapsed: elapsed}
		}()
	}

	var (
		successCount int
		errs         []error
	)

	for i := 0; i < len(d.targets); i++ {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case res := <-results:
			if res.err != nil {
				d.logger.Warn("target deliver failed", "target", res.name, "elapsed", res.elapsed, "error", res.err)
				errs = append(errs, fmt.Errorf("%s: %w", res.name, res.err))
			} else {
				successCount++
				d.logger.Debug("target deliver succeeded", "target", res.name, "elapsed", res.elapsed)
			}
		}
	}

	if successCount == 0 && len(d.targets) > 0 {
		return fmt.Errorf("%w: %w", ErrAllTargetsFailed, errors.Join(errs...))
	}

	return nil
}

// Send is an alias for Deliver, allowing Dispatcher itself to satisfy Target.
func (d *Dispatcher) Send(ctx context.Context, msg *model.Message) error {
	return d.Deliver(ctx, msg)
}
