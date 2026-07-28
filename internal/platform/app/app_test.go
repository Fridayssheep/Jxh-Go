package app

import (
	"context"
	"errors"
	"io"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestRunShutsComponentsBeforeResourcesInDeterministicOrder(t *testing.T) {
	var mu sync.Mutex
	order := make([]string, 0, 4)
	record := func(value string) {
		mu.Lock()
		defer mu.Unlock()
		order = append(order, value)
	}
	started := make(chan struct{})
	application, err := New(Options{
		ShutdownTimeout: time.Second,
		Components: []Component{
			{
				Name: "admin-http", Critical: true,
				Run: func(ctx context.Context) error {
					close(started)
					<-ctx.Done()
					return nil
				},
				Shutdown: func(context.Context) error { record("admin-http"); return nil },
			},
			{
				Name: "health-http", Critical: true,
				Run:      func(ctx context.Context) error { <-ctx.Done(); return nil },
				Shutdown: func(context.Context) error { record("health-http"); return nil },
			},
		},
		Closers: []io.Closer{
			closerFunc(func() error { record("database"); return nil }),
			closerFunc(func() error { record("subscriptions"); return nil }),
		},
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- application.Run(ctx) }()
	<-started
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	want := []string{"admin-http", "health-http", "subscriptions", "database"}
	if !reflect.DeepEqual(order, want) {
		t.Fatalf("shutdown order = %v, want %v", order, want)
	}
}

func TestCriticalExitStopsApplicationAndClosesResources(t *testing.T) {
	closed := false
	application, err := New(Options{
		ShutdownTimeout: time.Second,
		Components: []Component{{
			Name: "napcat", Critical: true,
			Run: func(context.Context) error { return errors.New("connection loop failed") },
		}},
		Closers: []io.Closer{closerFunc(func() error { closed = true; return nil })},
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	err = application.Run(t.Context())
	if !errors.Is(err, ErrComponentFailed) || strings.Contains(err.Error(), "connection loop failed") {
		t.Fatalf("Run() error = %v", err)
	}
	if !closed {
		t.Fatal("resource was not closed")
	}
}

func TestRunWaitsForWorkerBeforeClosingResources(t *testing.T) {
	workerExited := make(chan struct{})
	application, err := New(Options{
		ShutdownTimeout: time.Second,
		Components: []Component{{
			Name: "worker", Critical: true,
			Run: func(ctx context.Context) error {
				<-ctx.Done()
				time.Sleep(10 * time.Millisecond)
				close(workerExited)
				return nil
			},
		}},
		Closers: []io.Closer{closerFunc(func() error {
			select {
			case <-workerExited:
				return nil
			default:
				return errors.New("closed before worker exit")
			}
		})},
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := application.Run(ctx); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
}

func TestRunReturnsSanitizedWorkerShutdownFailure(t *testing.T) {
	application, err := New(Options{
		ShutdownTimeout: time.Second,
		Components: []Component{{
			Name: "telemetry", Critical: true,
			Run: func(ctx context.Context) error {
				<-ctx.Done()
				return errors.New("dsn password secret")
			},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	err = application.Run(ctx)
	if !errors.Is(err, ErrComponentFailed) || strings.Contains(err.Error(), "password") {
		t.Fatalf("Run() error = %v", err)
	}
}

func TestRunBoundsComponentAndCloserShutdown(t *testing.T) {
	block := make(chan struct{})
	application, err := New(Options{
		ShutdownTimeout: 20 * time.Millisecond,
		Components: []Component{{
			Name: "stuck", Critical: true,
			Run: func(context.Context) error {
				<-block
				return nil
			},
			Shutdown: func(context.Context) error {
				<-block
				return nil
			},
		}},
		Closers: []io.Closer{closerFunc(func() error { <-block; return nil })},
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	started := time.Now()
	err = application.Run(ctx)
	close(block)
	if !errors.Is(err, ErrShutdownTimeout) || time.Since(started) > 200*time.Millisecond {
		t.Fatalf("Run() error=%v duration=%s", err, time.Since(started))
	}
}

func TestComponentPanicDoesNotLeakPanicValue(t *testing.T) {
	application, err := New(Options{
		ShutdownTimeout: time.Second,
		Components: []Component{{
			Name: "napcat", Critical: true,
			Run: func(context.Context) error { panic("secret upstream response") },
		}},
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	err = application.Run(t.Context())
	if err == nil || !strings.Contains(err.Error(), "component panic") {
		t.Fatalf("Run() error = %v", err)
	}
	if strings.Contains(err.Error(), "secret upstream response") {
		t.Fatalf("Run() leaked panic value: %v", err)
	}
}

func TestDegradableExitDoesNotStopCriticalComponent(t *testing.T) {
	criticalStopped := make(chan struct{})
	application, err := New(Options{
		ShutdownTimeout: time.Second,
		Components: []Component{
			{Name: "telemetry", Run: func(context.Context) error { return errors.New("store unavailable") }},
			{Name: "napcat", Critical: true, Run: func(ctx context.Context) error {
				<-ctx.Done()
				close(criticalStopped)
				return nil
			}},
		},
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- application.Run(ctx) }()
	select {
	case <-criticalStopped:
		t.Fatal("degradable component stopped the critical component")
	case <-time.After(20 * time.Millisecond):
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Run() error = %v", err)
	}
}

func TestRunOnlyOnceAndRejectsInvalidOptions(t *testing.T) {
	if _, err := New(Options{}); err == nil {
		t.Fatal("New() error = nil for zero timeout")
	}
	application, err := New(Options{ShutdownTimeout: time.Second})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := application.Run(ctx); err != nil {
		t.Fatalf("first Run() error = %v", err)
	}
	if err := application.Run(ctx); !errors.Is(err, ErrAlreadyRun) {
		t.Fatalf("second Run() error = %v, want ErrAlreadyRun", err)
	}
}

type closerFunc func() error

func (fn closerFunc) Close() error { return fn() }
