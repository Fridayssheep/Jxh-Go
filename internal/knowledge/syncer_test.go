package knowledge

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

func TestSyncerSerializesConcurrentReloads(t *testing.T) {
	source := &blockingDownloader{release: make(chan struct{}), entered: make(chan struct{}, 2)}
	syncer := NewSyncer(SyncerOptions{Source: source, Index: NewIndexRef(nil)})
	errorsChannel := make(chan error, 2)
	for range 2 {
		go func() { errorsChannel <- syncer.Sync(t.Context()) }()
	}
	<-source.entered
	select {
	case <-source.entered:
		t.Fatal("concurrent download entered before first reload completed")
	case <-time.After(20 * time.Millisecond):
	}
	close(source.release)
	for range 2 {
		if err := <-errorsChannel; err == nil {
			t.Fatal("Sync() error = nil for fake download")
		}
	}
	if source.maximum != 1 {
		t.Fatalf("maximum concurrent downloads = %d", source.maximum)
	}
}

type blockingDownloader struct {
	mu      sync.Mutex
	active  int
	maximum int
	release chan struct{}
	entered chan struct{}
}

func (s *blockingDownloader) Download(context.Context) ([]byte, error) {
	s.mu.Lock()
	s.active++
	if s.active > s.maximum {
		s.maximum = s.active
	}
	s.mu.Unlock()
	s.entered <- struct{}{}
	<-s.release
	s.mu.Lock()
	s.active--
	s.mu.Unlock()
	return nil, errors.New("download failed")
}
