package grouprequest

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/xuri/excelize/v2"
)

const (
	StatusPending = "pending"
	StatusSeen    = "observed"

	SourceEvent  = "event"
	SourceSystem = "system"

	maxRequestKeyRunes = 191
)

type Record struct {
	ID          uint64
	RequestKey  string
	Flag        string
	GroupID     int64
	UserID      int64
	StudentID   string
	StudentName string
	SubType     string
	Comment     string
	Status      string
	Source      string
	RawJSON     string
	RequestedAt time.Time
	FirstSeenAt time.Time
	LastSeenAt  time.Time
}

type Store interface {
	UpsertGroupJoinRequest(ctx context.Context, record Record) error
	ListGroupJoinRequests(ctx context.Context, groupID int64, limit int) ([]Record, error)
}

type Options struct {
	ExportDir string
	Now       func() time.Time
}

type Service struct {
	store     Store
	exportDir string
	now       func() time.Time
}

type ExportResult struct {
	Path  string
	Count int
}

func NewService(store Store, opts Options) *Service {
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	exportDir := strings.TrimSpace(opts.ExportDir)
	if exportDir == "" {
		exportDir = filepath.Join("data", "exports", "group_requests")
	}
	return &Service{store: store, exportDir: exportDir, now: now}
}

func (s *Service) Record(ctx context.Context, record Record) error {
	if s == nil || s.store == nil {
		return nil
	}
	record = normalizeRecord(record, s.now())
	return s.store.UpsertGroupJoinRequest(ctx, record)
}

func (s *Service) Export(ctx context.Context, groupID int64, limit int) (ExportResult, error) {
	if s == nil || s.store == nil {
		return ExportResult{}, fmt.Errorf("群申请存储未初始化")
	}
	if groupID <= 0 {
		return ExportResult{}, fmt.Errorf("群号无效")
	}
	records, err := s.store.ListGroupJoinRequests(ctx, groupID, limit)
	if err != nil {
		return ExportResult{}, err
	}
	if err := os.MkdirAll(s.exportDir, 0o755); err != nil {
		return ExportResult{}, err
	}
	temp, err := os.CreateTemp(s.exportDir, "group_requests_"+s.now().Format("20060102_150405")+"_*.xlsx")
	if err != nil {
		return ExportResult{}, err
	}
	path := temp.Name()
	if err := temp.Close(); err != nil {
		_ = os.Remove(path)
		return ExportResult{}, err
	}
	if err := writeXLSX(path, records); err != nil {
		_ = os.Remove(path)
		return ExportResult{}, err
	}
	return ExportResult{Path: path, Count: len(records)}, nil
}

// RecordFromEvent parses OneBot group request events that NapCat SDK exposes as UnknownEvent.
func RecordFromEvent(raw []byte) (Record, bool, error) {
	var event map[string]any
	if err := json.Unmarshal(raw, &event); err != nil {
		return Record{}, false, err
	}
	if anyString(event["post_type"]) != "request" || anyString(event["request_type"]) != "group" {
		return Record{}, false, nil
	}
	record := recordFromMap(event, SourceEvent, time.Unix(anyInt64(event["time"]), 0))
	record.RawJSON = string(raw)
	return record, true, nil
}

// RecordsFromSystemMessages normalizes get_group_system_msg join and invite rows.
func RecordsFromSystemMessages(joinRequests, invitedRequests []map[string]any, now time.Time) []Record {
	records := make([]Record, 0, len(joinRequests)+len(invitedRequests))
	for _, raw := range joinRequests {
		record := recordFromMap(raw, SourceSystem, now)
		if record.SubType == "" {
			record.SubType = "add"
		}
		records = append(records, record)
	}
	for _, raw := range invitedRequests {
		record := recordFromMap(raw, SourceSystem, now)
		if record.SubType == "" {
			record.SubType = "invite"
		}
		records = append(records, record)
	}
	return records
}

func normalizeRecord(record Record, now time.Time) Record {
	if record.Status == "" {
		record.Status = StatusPending
	}
	if record.Source == "" {
		record.Source = SourceEvent
	}
	if record.RequestedAt.IsZero() {
		record.RequestedAt = now
	}
	if record.FirstSeenAt.IsZero() {
		record.FirstSeenAt = now
	}
	if record.LastSeenAt.IsZero() {
		record.LastSeenAt = now
	}
	if record.RequestKey == "" {
		record.RequestKey = stableKey(record)
	} else if utf8.RuneCountInString(record.RequestKey) > maxRequestKeyRunes {
		if record.Flag != "" {
			record.RequestKey = stableKey(record)
		} else {
			record.RequestKey = hashedKey("key", record.RequestKey)
		}
	}
	return record
}

func recordFromMap(raw map[string]any, source string, fallbackTime time.Time) Record {
	requestedAt := timeFromMap(raw, fallbackTime, "time", "request_time", "timestamp", "create_time")
	groupID := firstInt64(raw, "group_id", "groupId")
	userID := firstInt64(raw, "user_id", "requester_uin", "requester_id", "uin", "qq")
	flag := firstString(raw, "flag", "request_id", "seq")
	comment := firstString(raw, "comment", "message", "request_content", "answer", "reason")
	studentID, studentName := extractStudentInfo(comment)
	rawJSON, _ := json.Marshal(raw)
	status := StatusPending
	if firstBool(raw, "checked") {
		status = StatusSeen
	}
	return normalizeRecord(Record{
		RequestKey:  flag,
		Flag:        flag,
		GroupID:     groupID,
		UserID:      userID,
		StudentID:   studentID,
		StudentName: studentName,
		SubType:     firstString(raw, "sub_type", "type"),
		Comment:     comment,
		Status:      status,
		Source:      source,
		RawJSON:     string(rawJSON),
		RequestedAt: requestedAt,
		FirstSeenAt: fallbackTime,
		LastSeenAt:  fallbackTime,
	}, fallbackTime)
}

func stableKey(record Record) string {
	if record.Flag != "" {
		if utf8.RuneCountInString(record.Flag) <= maxRequestKeyRunes {
			return record.Flag
		}
		return hashedKey("flag", record.Flag)
	}
	return hashedKey("derived", fmt.Sprintf("%d\x00%d\x00%s\x00%s", record.GroupID, record.UserID, record.Comment, record.RequestedAt.Format(time.RFC3339Nano)))
}

func hashedKey(prefix string, value string) string {
	sum := sha256.Sum256([]byte(value))
	return prefix + ":" + hex.EncodeToString(sum[:])
}

func extractStudentInfo(comment string) (string, string) {
	return extractStudentID(comment), extractStudentName(comment)
}

func extractStudentID(comment string) string {
	value := extractLabeledValue(comment, []string{"学号", "学生号", "学籍号"})
	var b strings.Builder
	for _, r := range value {
		if (r >= '0' && r <= '9') || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') {
			b.WriteRune(r)
			continue
		}
		if b.Len() > 0 {
			break
		}
	}
	candidate := b.String()
	if utf8.RuneCountInString(candidate) < 6 || utf8.RuneCountInString(candidate) > 32 {
		return ""
	}
	return candidate
}

func extractStudentName(comment string) string {
	candidate := extractLabeledValue(comment, []string{"姓名", "名字"})
	candidate = strings.Trim(candidate, " \t\r\n:：=，,；;。、/|")
	if candidate == "" || utf8.RuneCountInString(candidate) > 16 {
		return ""
	}
	for _, r := range candidate {
		if r >= '0' && r <= '9' {
			return ""
		}
	}
	return candidate
}

func extractLabeledValue(comment string, labels []string) string {
	for _, label := range labels {
		idx := strings.Index(comment, label)
		if idx < 0 {
			continue
		}
		rest := comment[idx+len(label):]
		rest = strings.TrimLeft(rest, " \t\r\n:：=-")
		if rest == "" {
			continue
		}
		if value := trimAtBoundary(rest); value != "" {
			return value
		}
	}
	return ""
}

func trimAtBoundary(value string) string {
	stop := len(value)
	for _, boundary := range []string{"\r\n", "\n", "\r", "\t", " ", "，", ",", "；", ";", "。", "、", "/", "|"} {
		if idx := strings.Index(value, boundary); idx >= 0 && idx < stop {
			stop = idx
		}
	}
	for _, label := range []string{"学号", "学生号", "学籍号", "姓名", "名字", "专业", "班级", "学院", "年级", "QQ", "qq"} {
		if idx := strings.Index(value, label); idx >= 0 && idx < stop {
			stop = idx
		}
	}
	return strings.TrimSpace(value[:stop])
}

func writeXLSX(path string, records []Record) error {
	f := excelize.NewFile()
	defer f.Close()
	const sheet = "群申请"
	defaultSheet := f.GetSheetName(0)
	if err := f.SetSheetName(defaultSheet, sheet); err != nil {
		return err
	}
	headers := []string{"记录ID", "群号", "用户QQ", "学号", "姓名", "申请类型", "验证信息", "状态", "来源", "申请时间", "首次记录时间", "最近出现时间", "flag"}
	for i, header := range headers {
		cell, _ := excelize.CoordinatesToCellName(i+1, 1)
		if err := f.SetCellValue(sheet, cell, header); err != nil {
			return err
		}
	}
	for row, record := range records {
		values := []any{
			record.ID,
			record.GroupID,
			record.UserID,
			record.StudentID,
			record.StudentName,
			record.SubType,
			record.Comment,
			record.Status,
			record.Source,
			formatTime(record.RequestedAt),
			formatTime(record.FirstSeenAt),
			formatTime(record.LastSeenAt),
			record.Flag,
		}
		for col, value := range values {
			cell, _ := excelize.CoordinatesToCellName(col+1, row+2)
			if err := f.SetCellValue(sheet, cell, value); err != nil {
				return err
			}
		}
	}
	return f.SaveAs(path)
}

func formatTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.Format("2006-01-02 15:04:05")
}

func timeFromMap(raw map[string]any, fallback time.Time, keys ...string) time.Time {
	for _, key := range keys {
		if ts := anyInt64(raw[key]); ts > 0 {
			return time.Unix(ts, 0)
		}
	}
	return fallback
}

func firstString(raw map[string]any, keys ...string) string {
	for _, key := range keys {
		if value := anyString(raw[key]); value != "" {
			return value
		}
	}
	return ""
}

func firstInt64(raw map[string]any, keys ...string) int64 {
	for _, key := range keys {
		if value := anyInt64(raw[key]); value != 0 {
			return value
		}
	}
	return 0
}

func firstBool(raw map[string]any, keys ...string) bool {
	for _, key := range keys {
		switch value := raw[key].(type) {
		case bool:
			if value {
				return true
			}
		case float64:
			if value != 0 {
				return true
			}
		case int:
			if value != 0 {
				return true
			}
		case int64:
			if value != 0 {
				return true
			}
		case string:
			parsed, err := strconv.ParseBool(strings.TrimSpace(value))
			if err == nil && parsed {
				return true
			}
		}
	}
	return false
}

func anyString(v any) string {
	switch value := v.(type) {
	case string:
		return strings.TrimSpace(value)
	case float64:
		return strconv.FormatInt(int64(value), 10)
	case int:
		return strconv.Itoa(value)
	case int64:
		return strconv.FormatInt(value, 10)
	case json.Number:
		return value.String()
	default:
		return ""
	}
}

func anyInt64(v any) int64 {
	switch value := v.(type) {
	case int64:
		return value
	case int:
		return int64(value)
	case float64:
		return int64(value)
	case json.Number:
		parsed, _ := value.Int64()
		return parsed
	case string:
		parsed, _ := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
		return parsed
	default:
		return 0
	}
}
