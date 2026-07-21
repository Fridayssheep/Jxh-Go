package triggerstats

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/xuri/excelize/v2"
)

type ExportResult struct {
	Path  string
	Count int
}

func (s *Service) ExportForDays(ctx context.Context, days int) (ExportResult, error) {
	if s == nil || s.store == nil {
		return ExportResult{}, fmt.Errorf("词条统计存储未初始化")
	}
	summaries, err := s.SummariesForDays(ctx, days, 0)
	if err != nil {
		return ExportResult{}, err
	}
	if len(summaries) == 0 {
		return ExportResult{}, nil
	}
	if err := os.MkdirAll(s.exportDir, 0o755); err != nil {
		return ExportResult{}, err
	}
	temp, err := os.CreateTemp(s.exportDir, "trigger_stats_"+rangeFileLabel(days)+"_"+s.now().Format("20060102_150405")+"_*.xlsx")
	if err != nil {
		return ExportResult{}, err
	}
	path := temp.Name()
	if err := temp.Close(); err != nil {
		_ = os.Remove(path)
		return ExportResult{}, err
	}
	if err := writeSummariesXLSX(path, rangeDisplayLabel(days), summaries); err != nil {
		_ = os.Remove(path)
		return ExportResult{}, err
	}
	if err := os.Chmod(path, 0o600); err != nil {
		_ = os.Remove(path)
		return ExportResult{}, err
	}
	return ExportResult{Path: path, Count: len(summaries)}, nil
}

func writeSummariesXLSX(path, rangeLabel string, summaries []Summary) error {
	f := excelize.NewFile()
	defer f.Close()
	const sheet = "词条统计"
	if err := f.SetSheetName(f.GetSheetName(0), sheet); err != nil {
		return err
	}
	headers := []string{"关键词", "词条ID", "关键词回复次数", "AI 检索次数", "总触发次数", "最近触发时间", "统计范围"}
	for i, header := range headers {
		cell, _ := excelize.CoordinatesToCellName(i+1, 1)
		if err := f.SetCellValue(sheet, cell, header); err != nil {
			return err
		}
	}
	for row, summary := range summaries {
		keyword := summary.Keyword
		if keyword == "" {
			keyword = summary.SourceKey
		}
		values := []any{
			keyword,
			summary.SourceKey,
			summary.KeywordReplyCount,
			summary.AIRetrievalCount,
			summary.TotalCount,
			formatTime(summary.LastTriggered),
			rangeLabel,
		}
		for col, value := range values {
			cell, _ := excelize.CoordinatesToCellName(col+1, row+2)
			if err := f.SetCellValue(sheet, cell, value); err != nil {
				return err
			}
		}
	}
	if err := f.SetColWidth(sheet, "A", "B", 24); err != nil {
		return err
	}
	if err := f.SetColWidth(sheet, "C", "E", 16); err != nil {
		return err
	}
	if err := f.SetColWidth(sheet, "F", "G", 22); err != nil {
		return err
	}
	if err := f.SetPanes(sheet, &excelize.Panes{Freeze: true, YSplit: 1, TopLeftCell: "A2", ActivePane: "bottomLeft"}); err != nil {
		return err
	}
	lastRow := len(summaries) + 1
	if err := f.AutoFilter(sheet, fmt.Sprintf("A1:G%d", lastRow), nil); err != nil {
		return err
	}
	return f.SaveAs(filepath.Clean(path))
}

func rangeFileLabel(days int) string {
	if days == 0 {
		return "all"
	}
	return fmt.Sprintf("%dd", days)
}

func rangeDisplayLabel(days int) string {
	if days == 0 {
		return "全部"
	}
	return fmt.Sprintf("最近%d个自然日", days)
}
