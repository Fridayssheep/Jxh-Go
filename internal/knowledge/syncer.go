package knowledge

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
)

type SyncerOptions struct {
	Source    WPSClient
	Sheet     string
	CacheFile string
	Index     *IndexRef
}

type Syncer struct {
	source    WPSClient
	sheet     string
	cacheFile string
	index     *IndexRef
}

func NewSyncer(opts SyncerOptions) *Syncer {
	return &Syncer{
		source:    opts.Source,
		sheet:     opts.Sheet,
		cacheFile: opts.CacheFile,
		index:     opts.Index,
	}
}

func (s *Syncer) Sync(ctx context.Context) error {
	if s == nil {
		return fmt.Errorf("knowledge syncer is nil")
	}
	if s.index == nil {
		return fmt.Errorf("knowledge index is nil")
	}
	data, err := s.source.Download(ctx)
	if err != nil {
		return err
	}
	index, err := s.parseAndBuild(data)
	if err != nil {
		return err
	}
	if err := saveAtomic(s.cacheFile, data); err != nil {
		return err
	}
	s.index.Store(index)
	return nil
}

func (s *Syncer) LoadCache() error {
	if s == nil || s.index == nil {
		return fmt.Errorf("knowledge index is nil")
	}
	if s.cacheFile == "" {
		return fmt.Errorf("knowledge cache file is empty")
	}
	data, err := os.ReadFile(s.cacheFile)
	if err != nil {
		return err
	}
	index, err := s.parseAndBuild(data)
	if err != nil {
		return err
	}
	s.index.Store(index)
	return nil
}

func (s *Syncer) parseAndBuild(data []byte) (*Index, error) {
	rows, err := ReadRowsFromXLSX(bytes.NewReader(data), s.sheet)
	if err != nil {
		return nil, err
	}
	entries := ParseRows(rows)
	if len(entries) == 0 {
		return nil, fmt.Errorf("knowledge workbook contains no valid entries")
	}
	return NewIndex(entries), nil
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
