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
	var workers sync.WaitGroup
	for index := range a.components {
		component := a.components[index]
		workers.Add(1)
		go func() {
			defer workers.Done()
			results <- componentResult{index: index, err: runComponent(runCtx, component)}
		}()
	}

	var runErr error
	remaining := len(a.components)
	for remaining > 0 && runErr == nil {
		select {
		case <-ctx.Done():
			runErr = context.Cause(ctx)
		case result := <-results:
			remaining--
			component := a.components[result.index]
			if runCtx.Err() != nil {
				continue
			}
			if component.Critical {
				if result.err == nil {
					result.err = errors.New("component exited unexpectedly")
				}
				runErr = fmt.Errorf("%s: %w", component.Name, result.err)
				continue
			}
			if result.err != nil {
				a.logger.Printf("degradable component stopped name=%s error=%v", component.Name, result.err)
			} else {
				a.logger.Printf("degradable component stopped name=%s", component.Name)
			}
		}
	}

	cancel()
	shutdownErr := a.shutdown()
	workers.Wait()
	if runErr != nil && !errors.Is(runErr, context.Canceled) {
		return errors.Join(runErr, shutdownErr)
	}
	return shutdownErr
}

func runComponent(ctx context.Context, component Component) (err error) {
	defer func() {
		if recover() != nil {
			err = errors.New("component panic")
		}
	}()
	return component.Run(ctx)
}

func (a *App) shutdown() error {
	ctx, cancel := context.WithTimeout(context.Background(), a.shutdownTimeout)
	defer cancel()

	var joined error
	for _, component := range a.components {
		if component.Shutdown == nil {
			continue
		}
		if err := component.Shutdown(ctx); err != nil {
			joined = errors.Join(joined, fmt.Errorf("shutdown %s: %w", component.Name, err))
		}
	}
	for index := len(a.closers) - 1; index >= 0; index-- {
		if err := a.closers[index].Close(); err != nil {
			joined = errors.Join(joined, fmt.Errorf("close resource %d: %w", index, err))
		}
	}
	return joined
}
