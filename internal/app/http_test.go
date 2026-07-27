package app

import (
	"context"
	"errors"
	"net/http"
	"testing"
)

func TestHTTPComponentNormalizesServerClosed(t *testing.T) {
	server := &httpServerFake{serveErr: http.ErrServerClosed}
	component, err := HTTPComponent("admin-http", server, true)
	if err != nil {
		t.Fatalf("HTTPComponent() error = %v", err)
	}
	if err := component.Run(t.Context()); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if err := component.Shutdown(t.Context()); err != nil {
		t.Fatalf("Shutdown() error = %v", err)
	}
	if !server.shutdown {
		t.Fatal("server was not shut down")
	}
}

func TestHTTPComponentPreservesServeFailure(t *testing.T) {
	want := errors.New("listen failed")
	component, err := HTTPComponent("admin-http", &httpServerFake{serveErr: want}, true)
	if err != nil {
		t.Fatalf("HTTPComponent() error = %v", err)
	}
	if err := component.Run(t.Context()); !errors.Is(err, want) {
		t.Fatalf("Run() error = %v, want %v", err, want)
	}
}

type httpServerFake struct {
	serveErr error
	shutdown bool
}

func (s *httpServerFake) ListenAndServe() error { return s.serveErr }

func (s *httpServerFake) Shutdown(context.Context) error {
	s.shutdown = true
	return nil
}
