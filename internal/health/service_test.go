package health

import (
	"sync"
	"testing"
	"time"
)

func TestReadinessSeparatesLivenessFromDependencies(t *testing.T) {
	service := NewService()
	service.SetDatabase(ComponentStatus{Available: true, CheckedAt: time.Unix(1, 0)})
	service.SetNapCat(ComponentStatus{Available: false, Code: "napcat_unavailable", CheckedAt: time.Unix(2, 0)})

	snapshot := service.Snapshot()
	if !snapshot.Live || snapshot.Ready {
		t.Fatalf("Snapshot() = %+v, want live but not ready", snapshot)
	}

	service.SetNapCat(ComponentStatus{Available: true, CheckedAt: time.Unix(3, 0)})
	if snapshot := service.Snapshot(); !snapshot.Live || !snapshot.Ready {
		t.Fatalf("Snapshot() = %+v, want live and ready", snapshot)
	}

	service.SetLive(false)
	if snapshot := service.Snapshot(); snapshot.Live || snapshot.Ready {
		t.Fatalf("Snapshot() = %+v, want stopped and not ready", snapshot)
	}
}

func TestSnapshotTracksEveryComponentWithoutSharingMutableState(t *testing.T) {
	service := NewService()
	checkedAt := time.Unix(10, 0)
	status := ComponentStatus{
		Available:     true,
		Code:          "available",
		Summary:       "dependency is available",
		CheckedAt:     checkedAt,
		LastSuccessAt: checkedAt,
		Latency:       25 * time.Millisecond,
	}
	service.SetDatabase(status)
	service.SetNapCat(status)
	service.SetWPS(status)
	service.SetAI(status)
	service.SetQuote(status)
	service.SetScheduler(status)
	service.SetWorkers(status)

	first := service.Snapshot()
	components := []ComponentStatus{
		first.Database, first.NapCat, first.WPS, first.AI,
		first.Quote, first.Scheduler, first.Workers,
	}
	for index, component := range components {
		if component != status {
			t.Fatalf("component %d = %+v, want %+v", index, component, status)
		}
	}

	first.Database.Code = "mutated"
	if got := service.Snapshot().Database.Code; got != "available" {
		t.Fatalf("stored Database.Code = %q after snapshot mutation", got)
	}
}

func TestServiceSupportsConcurrentStatusUpdates(t *testing.T) {
	service := NewService()
	var workers sync.WaitGroup
	for worker := 0; worker < 8; worker++ {
		workers.Add(1)
		go func(worker int) {
			defer workers.Done()
			for iteration := 0; iteration < 200; iteration++ {
				status := ComponentStatus{
					Available: iteration%2 == 0,
					CheckedAt: time.Unix(int64(worker+1), int64(iteration)),
				}
				service.SetDatabase(status)
				service.SetNapCat(status)
				_ = service.Snapshot()
			}
		}(worker)
	}
	workers.Wait()
}
