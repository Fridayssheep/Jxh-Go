package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"sync"
	"time"
)

var ErrAlreadyRun = errors.New("app has already been run")

var (
	ErrComponentFailed = errors.New("application component failed")
	ErrShutdownFailed  = errors.New("application shutdown failed")
	ErrShutdownTimeout = errors.New("application shutdown timed out")
)

var errComponentPanic = errors.New("component panic")

// Component is a long-running process owned by App. Critical components stop
// the whole application when they exit unexpectedly; degradable components
// are reported and leave the remaining message path running.
type Component struct {
	Name     string
	Critical bool
	Run      func(context.Context) error
	Shutdown func(context.Context) error
}

type Options struct {
	Components      []Component
	Closers         []io.Closer
	ShutdownTimeout time.Duration
	Logger          *log.Logger
}

type App struct {
	components      []Component
	closers         []io.Closer
	shutdownTimeout time.Duration
	logger          *log.Logger

	mu      sync.Mutex
	started bool
}

func New(options Options) (*App, error) {
	if options.ShutdownTimeout <= 0 {
		return nil, fmt.Errorf("shutdown timeout must be positive")
	}
	seen := make(map[string]struct{}, len(options.Components))
	components := make([]Component, len(options.Components))
	for index, component := range options.Components {
		if component.Name == "" || component.Run == nil {
			return nil, fmt.Errorf("component %d is invalid", index)
		}
		if _, exists := seen[component.Name]; exists {
			return nil, fmt.Errorf("duplicate component %q", component.Name)
		}
		seen[component.Name] = struct{}{}
		components[index] = component
	}
	closers := append([]io.Closer(nil), options.Closers...)
	for index, closer := range closers {
		if closer == nil {
			return nil, fmt.Errorf("closer %d is nil", index)
		}
	}
	if options.Logger == nil {
		options.Logger = log.New(io.Discard, "", 0)
	}
	return &App{
		components: components, closers: closers,
		shutdownTimeout: options.ShutdownTimeout, logger: options.Logger,
	}, nil
}

type componentResult struct {
	index int
	err   error
}

func (a *App) Run(ctx context.Context) error {
	if ctx == nil {
		return fmt.Errorf("run context is required")
	}
	a.mu.Lock()
	if a.started {
		a.mu.Unlock()
		return ErrAlreadyRun
	}
	a.started = true
	a.mu.Unlock()

	runCtx, cancel := context.WithCancel(ctx)
	results := make(chan componentResult, len(a.components))
	for index := range a.components {
		component := a.components[index]
		go func() {
			results <- componentResult{index: index, err: runComponent(runCtx, component)}
		}()
	}

	var runErr error
	completed := make([]bool, len(a.components))
	completedCount := 0
	for completedCount < len(a.components) && runErr == nil {
		select {
		case <-ctx.Done():
			runErr = context.Cause(ctx)
		case result := <-results:
			if completed[result.index] {
				continue
			}
			completed[result.index] = true
			completedCount++
			if ctx.Err() != nil {
				runErr = context.Cause(ctx)
				continue
			}
			component := a.components[result.index]
			if component.Critical {
				runErr = componentFailure(component.Name, result.err)
				continue
			}
			if result.err != nil {
				a.logger.Printf("degradable component stopped name=%s status=failed", component.Name)
			} else {
				a.logger.Printf("degradable component stopped name=%s", component.Name)
			}
		}
	}

	cancel()
	shutdownErr := a.shutdownComponents()
	waitErr := a.waitForComponents(results, completed, completedCount)
	closeErr := a.closeResources()
	if runErr != nil && !errors.Is(runErr, context.Canceled) {
		return errors.Join(runErr, shutdownErr, waitErr, closeErr)
	}
	return errors.Join(shutdownErr, waitErr, closeErr)
}

func runComponent(ctx context.Context, component Component) (err error) {
	defer func() {
		if recover() != nil {
			err = errComponentPanic
		}
	}()
	return component.Run(ctx)
}

func componentFailure(name string, err error) error {
	reason := ErrComponentFailed
	if errors.Is(err, errComponentPanic) {
		reason = errComponentPanic
	}
	return fmt.Errorf("component %s stopped: %w", name, reason)
}

func (a *App) shutdownComponents() error {
	ctx, cancel := context.WithTimeout(context.Background(), a.shutdownTimeout)
	defer cancel()

	var joined error
	for _, component := range a.components {
		if component.Shutdown == nil {
			continue
		}
		if err := runBounded(ctx, component.Shutdown); err != nil {
			if errors.Is(err, context.DeadlineExceeded) {
				return errors.Join(joined, fmt.Errorf("shutdown component %s: %w", component.Name, ErrShutdownTimeout))
			}
			joined = errors.Join(joined, fmt.Errorf("shutdown component %s: %w", component.Name, ErrShutdownFailed))
		}
	}
	return joined
}

func (a *App) waitForComponents(results <-chan componentResult, completed []bool, completedCount int) error {
	ctx, cancel := context.WithTimeout(context.Background(), a.shutdownTimeout)
	defer cancel()
	var joined error
	for completedCount < len(a.components) {
		select {
		case result := <-results:
			if completed[result.index] {
				continue
			}
			completed[result.index] = true
			completedCount++
			if result.err != nil && !errors.Is(result.err, context.Canceled) && !errors.Is(result.err, context.DeadlineExceeded) {
				joined = errors.Join(joined, componentFailure(a.components[result.index].Name, result.err))
			}
		case <-ctx.Done():
			return errors.Join(joined, ErrShutdownTimeout)
		}
	}
	return joined
}

func (a *App) closeResources() error {
	ctx, cancel := context.WithTimeout(context.Background(), a.shutdownTimeout)
	defer cancel()
	var joined error
	for index := len(a.closers) - 1; index >= 0; index-- {
		if err := runBounded(ctx, func(context.Context) error { return a.closers[index].Close() }); err != nil {
			if errors.Is(err, context.DeadlineExceeded) {
				return errors.Join(joined, fmt.Errorf("close resource %d: %w", index, ErrShutdownTimeout))
			}
			joined = errors.Join(joined, fmt.Errorf("close resource %d: %w", index, ErrShutdownFailed))
		}
	}
	return joined
}

func runBounded(ctx context.Context, operation func(context.Context) error) error {
	result := make(chan error, 1)
	go func() { result <- operation(ctx) }()
	select {
	case err := <-result:
		return err
	case <-ctx.Done():
		return context.Cause(ctx)
	}
}
