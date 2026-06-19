package knowledge

import (
	"context"
	"fmt"
	"time"
)

type SyncStore interface {
	UpsertKnowledge(ctx context.Context, entries []Entry, runID uint64) error
}

type ParseWorkbookFunc func(data []byte, sheet string) (SyncResult, error)

type SyncerOptions struct {
	Source   RowSource
	Sheet    string
	Store    SyncStore
	Parse    ParseWorkbookFunc
	NewRunID func() uint64
	OnSynced func([]Entry)
}

type Syncer struct {
	source   RowSource
	sheet    string
	store    SyncStore
	parse    ParseWorkbookFunc
	newRunID func() uint64
	onSynced func([]Entry)
}

func NewSyncer(opts SyncerOptions) *Syncer {
	parse := opts.Parse
	if parse == nil {
		parse = ParseWorkbook
	}
	newRunID := opts.NewRunID
	if newRunID == nil {
		newRunID = func() uint64 { return uint64(time.Now().UnixNano()) }
	}
	return &Syncer{
		source:   opts.Source,
		sheet:    opts.Sheet,
		store:    opts.Store,
		parse:    parse,
		newRunID: newRunID,
		onSynced: opts.OnSynced,
	}
}

func (s *Syncer) Reload(ctx context.Context) error {
	_, err := s.Sync(ctx)
	return err
}

func (s *Syncer) Sync(ctx context.Context) (SyncResult, error) {
	if s == nil || s.source == nil {
		return SyncResult{}, fmt.Errorf("knowledge sync source is nil")
	}
	if s.store == nil {
		return SyncResult{}, fmt.Errorf("knowledge sync store is nil")
	}
	data, err := s.source.Download(ctx)
	if err != nil {
		return SyncResult{}, err
	}
	result, err := s.parse(data, s.sheet)
	if err != nil {
		return SyncResult{}, err
	}
	if err := s.store.UpsertKnowledge(ctx, result.Entries, s.newRunID()); err != nil {
		return SyncResult{}, err
	}
	if s.onSynced != nil {
		s.onSynced(result.Entries)
	}
	return result, nil
}
