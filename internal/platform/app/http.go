package app

import (
	"context"
	"errors"
	"fmt"
	"net/http"
)

type HTTPServer interface {
	ListenAndServe() error
	Shutdown(context.Context) error
}

func HTTPComponent(name string, server HTTPServer, critical bool) (Component, error) {
	if name == "" || server == nil {
		return Component{}, fmt.Errorf("invalid HTTP component")
	}
	return Component{
		Name: name, Critical: critical,
		Run: func(context.Context) error {
			err := server.ListenAndServe()
			if errors.Is(err, http.ErrServerClosed) {
				return nil
			}
			return err
		},
		Shutdown: server.Shutdown,
	}, nil
}
