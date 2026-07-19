package knowledge

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
)

type ParseWorkbookFunc func(data []byte, sheet string) (SyncResult, error)

type SyncerOptions struct {
	Source    RowSource
	Sheet     string
	CacheFile string
	Index     *IndexRef
	Parse     ParseWorkbookFunc
}

type Syncer struct {
	source    RowSource
	sheet     string
	cacheFile string
	index     *IndexRef
	parse     ParseWorkbookFunc
}

func NewSyncer(opts SyncerOptions) *Syncer {
	parse := opts.Parse
	if parse == nil {
		parse = ParseWorkbook
	}
	return &Syncer{
		source:    opts.Source,
		sheet:     opts.Sheet,
		cacheFile: opts.CacheFile,
		index:     opts.Index,
		parse:     parse,
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
	if s.index == nil {
		return SyncResult{}, fmt.Errorf("knowledge index is nil")
	}
	data, err := s.source.Download(ctx)
	if err != nil {
		return SyncResult{}, err
	}
	result, index, err := s.parseAndBuild(data)
	if err != nil {
		return SyncResult{}, err
	}
	if err := saveAtomic(s.cacheFile, data); err != nil {
		return SyncResult{}, err
	}
	s.index.Store(index)
	return result, nil
}

func (s *Syncer) LoadCache() (SyncResult, error) {
	if s == nil || s.index == nil {
		return SyncResult{}, fmt.Errorf("knowledge index is nil")
	}
	if s.cacheFile == "" {
		return SyncResult{}, fmt.Errorf("knowledge cache file is empty")
	}
	data, err := os.ReadFile(s.cacheFile)
	if err != nil {
		return SyncResult{}, err
	}
	result, index, err := s.parseAndBuild(data)
	if err != nil {
		return SyncResult{}, err
	}
	s.index.Store(index)
	return result, nil
}

func (s *Syncer) parseAndBuild(data []byte) (SyncResult, *Index, error) {
	result, err := s.parse(data, s.sheet)
	if err != nil {
		return SyncResult{}, nil, err
	}
	if len(result.Entries) == 0 {
		return SyncResult{}, nil, fmt.Errorf("knowledge workbook contains no valid entries")
	}
	return result, NewIndex(result.Entries), nil
}

func saveAtomic(path string, data []byte) error {
	if path == "" {
		return nil
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	temp, err := os.CreateTemp(dir, ".knowledge-*.xlsx")
	if err != nil {
		return err
	}
	tempName := temp.Name()
	defer os.Remove(tempName)
	if _, err := temp.Write(data); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	return os.Rename(tempName, path)
}
