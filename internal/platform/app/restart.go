package app

import (
	"context"
	"errors"
	"sync"
)

var ErrRestartRequested = errors.New("bot restart requested")

// RestartCoordinator converts the first accepted restart operation into a
// process cancellation. The supervisor is responsible for starting a new
// process after the current one exits.
type RestartCoordinator struct {
	cancel context.CancelCauseFunc
	once   sync.Once
}

func NewRestartCoordinator(cancel context.CancelCauseFunc) *RestartCoordinator {
	return &RestartCoordinator{cancel: cancel}
}

func (c *RestartCoordinator) Schedule(operationID string) bool {
	if c == nil || c.cancel == nil || operationID == "" {
		return false
	}
	scheduled := false
	c.once.Do(func() {
		scheduled = true
		c.cancel(ErrRestartRequested)
	})
	return scheduled
}
