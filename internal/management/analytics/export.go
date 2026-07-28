package analytics

import (
	"context"
	"encoding/csv"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/xuri/excelize/v2"
)

const (
	csvContentType  = "text/csv; charset=utf-8"
	xlsxContentType = "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"
)

type exportRowSource interface {
	Headers() []string
	RowCount() int
	Next(context.Context) ([]string, bool, error)
	Close() error
}

type sliceExportSource struct {
	headers []string
	rows    [][]string
	index   int
}

func (s *sliceExportSource) Headers() []string { return append([]string(nil), s.headers...) }
func (s *sliceExportSource) RowCount() int     { return len(s.rows) }
func (s *sliceExportSource) Close() error      { return nil }

func (s *sliceExportSource) Next(ctx context.Context) ([]string, bool, error) {
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	if s.index >= len(s.rows) {
		return nil, false, nil
	}
	row := append([]string(nil), s.rows[s.index]...)
	s.index++
	return row, true, nil
}

type joinRequestExportSource struct {
	rows     JoinRequestExportRows
	location *time.Location
}

func (s *joinRequestExportSource) Headers() []string {
	return []string{
		"request_id", "group_id", "sub_type", "source", "observed_status", "decision_status",
		"decision_source", "requested_at", "decided_at",
	}
}

func (s *joinRequestExportSource) RowCount() int { return s.rows.RowCount() }
func (s *joinRequestExportSource) Close() error  { return s.rows.Close() }

func (s *joinRequestExportSource) Next(ctx context.Context) ([]string, bool, error) {
	value, ok, err := s.rows.Next(ctx)
	if err != nil || !ok {
		return nil, ok, err
	}
	if !validJoinRequestExportRow(value) {
		return nil, false, ErrInvalidData
	}
	return []string{
		value.RequestID, value.GroupID, value.SubType, value.Source, value.ObservedStatus, value.DecisionStatus,
		stringValue(value.DecisionSource), formatExportTime(value.RequestedAt, s.location), formatOptionalExportTime(value.DecidedAt, s.location),
	}, true, nil
}

type scheduledJobRunExportSource struct {
	rows     ScheduledJobRunExportRows
	location *time.Location
}

func (s *scheduledJobRunExportSource) Headers() []string {
	return []string{
		"run_id", "job_id", "group_id", "kind", "result", "scheduled_for", "started_at", "completed_at", "duration_ms", "error_code",
	}
}

func (s *scheduledJobRunExportSource) RowCount() int { return s.rows.RowCount() }
func (s *scheduledJobRunExportSource) Close() error  { return s.rows.Close() }

func (s *scheduledJobRunExportSource) Next(ctx context.Context) ([]string, bool, error) {
	value, ok, err := s.rows.Next(ctx)
	if err != nil || !ok {
		return nil, ok, err
	}
	if !validScheduledJobRunExportRow(value) {
		return nil, false, ErrInvalidData
	}
	return []string{
		value.RunID, value.JobID, value.GroupID, value.Kind, string(value.Result), formatOptionalExportTime(value.ScheduledFor, s.location),
		formatExportTime(value.StartedAt, s.location), formatOptionalExportTime(value.CompletedAt, s.location),
		strconv.FormatInt(value.DurationMS, 10), stringValue(value.ErrorCode),
	}, true, nil
}

func prepareExport(source exportRowSource, dataset Dataset, format ExportFormat, generatedAt time.Time, rowCount int) *PreparedExport {
	contentType := csvContentType
	if format == ExportXLSX {
		contentType = xlsxContentType
	}
	metadata := ExportMetadata{
		Filename:    fmt.Sprintf("analytics_%s_%s.%s", dataset, generatedAt.Format("20060102_150405"), format),
		ContentType: contentType, RowCount: rowCount,
	}
	return &PreparedExport{
		metadata: metadata,
		write: func(ctx context.Context, writer io.Writer) error {
			if format == ExportCSV {
				return writeCSVExport(ctx, writer, source, rowCount)
			}
			return writeXLSXExport(ctx, writer, source, rowCount)
		},
		close: source.Close,
	}
}

func writeCSVExport(ctx context.Context, output io.Writer, source exportRowSource, rowCount int) error {
	writer := csv.NewWriter(output)
	if err := writer.Write(sanitizeExportRow(source.Headers())); err != nil {
		return err
	}
	if err := writeExportRows(ctx, source, rowCount, func(rowIndex int, row []string) error {
		return writer.Write(sanitizeExportRow(row))
	}); err != nil {
		return err
	}
	writer.Flush()
	return writer.Error()
}

func writeXLSXExport(ctx context.Context, output io.Writer, source exportRowSource, rowCount int) error {
	file := excelize.NewFile()
	defer file.Close()
	const sheet = "Analytics"
	defaultSheet := file.GetSheetName(0)
	if err := file.SetSheetName(defaultSheet, sheet); err != nil {
		return err
	}
	stream, err := file.NewStreamWriter(sheet)
	if err != nil {
		return err
	}
	if err := stream.SetRow("A1", stringInterfaces(sanitizeExportRow(source.Headers()))); err != nil {
		return err
	}
	if err := writeExportRows(ctx, source, rowCount, func(rowIndex int, row []string) error {
		cell, coordinateErr := excelize.CoordinatesToCellName(1, rowIndex+2)
		if coordinateErr != nil {
			return coordinateErr
		}
		return stream.SetRow(cell, stringInterfaces(sanitizeExportRow(row)))
	}); err != nil {
		return err
	}
	if err := stream.Flush(); err != nil {
		return err
	}
	if err := file.SetPanes(sheet, &excelize.Panes{Freeze: true, YSplit: 1, TopLeftCell: "A2", ActivePane: "bottomLeft"}); err != nil {
		return err
	}
	return file.Write(output)
}

func writeExportRows(ctx context.Context, source exportRowSource, expected int, write func(int, []string) error) error {
	for index := 0; index < expected; index++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		row, ok, err := source.Next(ctx)
		if err != nil {
			return err
		}
		if !ok || len(row) != len(source.Headers()) {
			return ErrInvalidData
		}
		if err := write(index, row); err != nil {
			return err
		}
	}
	_, more, err := source.Next(ctx)
	if err != nil {
		return err
	}
	if more {
		return ErrInvalidData
	}
	return nil
}

func summaryExportSource(value Summary) exportRowSource {
	location, _ := time.LoadLocation(value.Window.Timezone)
	rows := make([][]string, len(value.Metrics))
	for index, metric := range value.Metrics {
		rows[index] = []string{
			string(metric.Key), metric.Label, string(metric.Unit), strconv.FormatBool(metric.Available), formatFloat(metric.Value),
			formatFloat(metric.PreviousValue), formatFloat(metric.ChangePercent), formatExportTime(value.Window.From, location),
			formatExportTime(value.Window.To, location), value.Window.Timezone, formatExportTime(value.DataFreshAt, location),
		}
	}
	return &sliceExportSource{
		headers: []string{
			"metric", "label", "unit", "available", "value", "previous_value", "change_percent", "from", "to", "timezone", "data_fresh_at",
		},
		rows: rows,
	}
}

func timeseriesExportSource(value Timeseries) exportRowSource {
	location, _ := time.LoadLocation(value.Window.Timezone)
	rows := make([][]string, 0)
	for _, series := range value.Series {
		for _, point := range series.Points {
			rows = append(rows, []string{
				string(series.Metric), series.Label, string(series.Unit), formatExportTime(point.BucketStart, location), formatFloat(point.Value),
				formatExportTime(value.Window.From, location), formatExportTime(value.Window.To, location), value.Window.Timezone,
				formatExportTime(value.DataFreshAt, location),
			})
		}
	}
	return &sliceExportSource{
		headers: []string{"metric", "label", "unit", "bucket_start", "value", "from", "to", "timezone", "data_fresh_at"},
		rows:    rows,
	}
}

func rankingsExportSource(value Rankings) exportRowSource {
	location, _ := time.LoadLocation(value.Window.Timezone)
	rows := make([][]string, len(value.Items))
	for index, item := range value.Items {
		rows[index] = []string{
			string(value.Dimension), string(value.Metric), string(value.Unit), strconv.Itoa(item.Rank), item.Key, item.DisplayName,
			strconv.FormatFloat(item.Value, 'g', -1, 64), formatExportTime(value.Window.From, location),
			formatExportTime(value.Window.To, location), value.Window.Timezone, formatExportTime(value.DataFreshAt, location),
		}
	}
	return &sliceExportSource{
		headers: []string{"dimension", "metric", "unit", "rank", "key", "display_name", "value", "from", "to", "timezone", "data_fresh_at"},
		rows:    rows,
	}
}

func validJoinRequestExportRow(value JoinRequestExportRow) bool {
	if !validText(value.RequestID, 256, false) || !validText(value.GroupID, 256, false) ||
		(value.SubType != "add" && value.SubType != "invite") || (value.Source != "event" && value.Source != "system") ||
		(value.ObservedStatus != "pending" && value.ObservedStatus != "checked") || !validJoinDecisionStatus(value.DecisionStatus) ||
		!validJoinDecisionSource(value.DecisionSource) || !validDataTime(value.RequestedAt) || !validOptionalUTCTime(value.DecidedAt) {
		return false
	}
	return value.DecidedAt == nil || !value.DecidedAt.Before(value.RequestedAt)
}

func validJoinDecisionStatus(value string) bool {
	switch value {
	case "pending", "processing", "approved", "rejected", "external_processed", "unknown":
		return true
	default:
		return false
	}
}

func validJoinDecisionSource(value *string) bool {
	return value == nil || *value == "manual" || *value == "automatic" || *value == "external"
}

func validScheduledJobRunExportRow(value ScheduledJobRunExportRow) bool {
	if !validText(value.RunID, 256, false) || !validText(value.JobID, 256, false) || !validText(value.GroupID, 256, false) ||
		(value.Kind != "scheduled" && value.Kind != "test") || !validScheduledJobResult(value.Result) || !validOptionalUTCTime(value.ScheduledFor) ||
		!validDataTime(value.StartedAt) || !validOptionalUTCTime(value.CompletedAt) || value.DurationMS < 0 ||
		!validSafeErrorCode(value.ErrorCode) {
		return false
	}
	return value.CompletedAt == nil || !value.CompletedAt.Before(value.StartedAt)
}

func validScheduledJobResult(value Result) bool {
	return value == ResultSuccess || value == ResultFailed || value == ResultUnknown || value == ResultSkipped
}

func validSafeErrorCode(value *string) bool {
	if value == nil {
		return true
	}
	if len(*value) < 1 || len(*value) > 100 || (*value)[0] < 'a' || (*value)[0] > 'z' {
		return false
	}
	for _, character := range (*value)[1:] {
		if (character < 'a' || character > 'z') && (character < '0' || character > '9') && character != '_' {
			return false
		}
	}
	return true
}

func sanitizeExportRow(row []string) []string {
	result := make([]string, len(row))
	for index, value := range row {
		result[index] = sanitizeSpreadsheetCell(value)
	}
	return result
}

func sanitizeSpreadsheetCell(value string) string {
	trimmed := strings.TrimLeft(value, " \t\r\n")
	first, _ := utf8.DecodeRuneInString(trimmed)
	if first == '=' || first == '+' || first == '-' || first == '@' {
		return "'" + value
	}
	return value
}

func stringInterfaces(values []string) []interface{} {
	result := make([]interface{}, len(values))
	for index := range values {
		result[index] = values[index]
	}
	return result
}

func formatFloat(value *float64) string {
	if value == nil {
		return ""
	}
	return strconv.FormatFloat(*value, 'g', -1, 64)
}

func formatExportTime(value time.Time, location *time.Location) string {
	return value.In(location).Format(time.RFC3339Nano)
}

func formatOptionalExportTime(value *time.Time, location *time.Location) string {
	if value == nil {
		return ""
	}
	return formatExportTime(*value, location)
}

func stringValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}
