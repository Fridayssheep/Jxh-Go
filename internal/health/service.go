package health

import (
	"sync/atomic"
	"time"
)

type ComponentStatus struct {
	Available     bool
	Code          string
	Summary       string
	CheckedAt     time.Time
	LastSuccessAt time.Time
	LastErrorAt   time.Time
	Latency       time.Duration
}

type Snapshot struct {
	Live      bool
	Ready     bool
	Database  ComponentStatus
	NapCat    ComponentStatus
	WPS       ComponentStatus
	AI        ComponentStatus
	Quote     ComponentStatus
	Scheduler ComponentStatus
	Workers   ComponentStatus
}

type Service struct {
	state atomic.Pointer[Snapshot]
}

func NewService() *Service {
	service := &Service{}
	service.state.Store(&Snapshot{Live: true})
	return service
}

func (s *Service) Snapshot() Snapshot {
	return *s.load()
}

func (s *Service) SetLive(live bool) {
	s.update(func(snapshot *Snapshot) {
		snapshot.Live = live
	})
}

func (s *Service) SetDatabase(status ComponentStatus) {
	s.update(func(snapshot *Snapshot) {
		snapshot.Database = status
	})
}

func (s *Service) SetNapCat(status ComponentStatus) {
	s.update(func(snapshot *Snapshot) {
		snapshot.NapCat = status
	})
}

func (s *Service) SetWPS(status ComponentStatus) {
	s.update(func(snapshot *Snapshot) {
		snapshot.WPS = status
	})
}

func (s *Service) SetAI(status ComponentStatus) {
	s.update(func(snapshot *Snapshot) {
		snapshot.AI = status
	})
}

func (s *Service) SetQuote(status ComponentStatus) {
	s.update(func(snapshot *Snapshot) {
		snapshot.Quote = status
	})
}

func (s *Service) SetScheduler(status ComponentStatus) {
	s.update(func(snapshot *Snapshot) {
		snapshot.Scheduler = status
	})
}

func (s *Service) SetWorkers(status ComponentStatus) {
	s.update(func(snapshot *Snapshot) {
		snapshot.Workers = status
	})
}

func (s *Service) update(change func(*Snapshot)) {
	for {
		current := s.load()
		next := *current
		change(&next)
		next.Ready = next.Live && next.Database.Available
		if s.state.CompareAndSwap(current, &next) {
			return
		}
	}
}

func (s *Service) load() *Snapshot {
	if state := s.state.Load(); state != nil {
		return state
	}
	initial := &Snapshot{Live: true}
	if s.state.CompareAndSwap(nil, initial) {
		return initial
	}
	return s.state.Load()
}
