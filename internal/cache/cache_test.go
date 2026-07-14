package cache

import (
	"testing"
	"time"
)

func TestEventDedupeSeenMarkForget(t *testing.T) {
	dedupe := NewEventDedupe(time.Hour)
	if dedupe.Seen("event") {
		t.Fatal("new event was already seen")
	}
	dedupe.Mark("event")
	if !dedupe.Seen("event") {
		t.Fatal("marked event was not seen")
	}
	dedupe.Forget("event")
	if dedupe.Seen("event") {
		t.Fatal("forgotten event was still seen")
	}
}
