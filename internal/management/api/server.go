package adminapi

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/zjutjh/jxh-go/internal/platform/config"
)

type Server struct {
	httpServer *http.Server
}

func NewServer(configuration config.AdminConfig, handler http.Handler) (*Server, error) {
	if handler == nil || configuration.Addr == "" {
		return nil, fmt.Errorf("invalid admin server configuration")
	}
	durations := []int{
		configuration.ReadHeaderTimeoutSeconds, configuration.ReadTimeoutSeconds,
		configuration.WriteTimeoutSeconds, configuration.IdleTimeoutSeconds,
	}
	for _, seconds := range durations {
		if seconds <= 0 {
			return nil, fmt.Errorf("invalid admin server timeout")
		}
	}
	return &Server{httpServer: &http.Server{
		Addr: configuration.Addr, Handler: handler,
		ReadHeaderTimeout: time.Duration(configuration.ReadHeaderTimeoutSeconds) * time.Second,
		ReadTimeout:       time.Duration(configuration.ReadTimeoutSeconds) * time.Second,
		WriteTimeout:      time.Duration(configuration.WriteTimeoutSeconds) * time.Second,
		IdleTimeout:       time.Duration(configuration.IdleTimeoutSeconds) * time.Second,
	}}, nil
}

func (s *Server) ListenAndServe() error {
	return s.httpServer.ListenAndServe()
}

func (s *Server) Shutdown(ctx context.Context) error {
	return s.httpServer.Shutdown(ctx)
}

func (s *Server) HTTPServer() *http.Server {
	return s.httpServer
}
