package grouprequest

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/xuri/excelize/v2"
	"github.com/zjutjh/napcat-sdk/api"
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
	ListGroupJoinRequests(ctx context.Context, limit int) ([]Record, error)
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
	Dir   string
	Count int
	Files []ExportFile
}

type ExportFile struct {
	GroupID int64
	Path    string
	Count   int
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
		return fmt.Errorf("群申请存储未初始化")
	}
	record = normalizeRecord(record, s.now())
	return s.store.UpsertGroupJoinRequest(ctx, record)
}

func (s *Service) Export(ctx context.Context, limit int) (ExportResult, error) {
	if s == nil || s.store == nil {
		return ExportResult{}, fmt.Errorf("群申请存储未初始化")
	}
	records, err := s.store.ListGroupJoinRequests(ctx, limit)
	if err != nil {
		return ExportResult{}, err
	}
	if len(records) == 0 {
		return ExportResult{}, nil
	}
	if err := os.MkdirAll(s.exportDir, 0o755); err != nil {
		return ExportResult{}, err
	}
	runDir, err := os.MkdirTemp(s.exportDir, "group_requests_"+s.now().Format("20060102_150405")+"_")
	if err != nil {
		return ExportResult{}, err
	}
	groups := make(map[int64][]Record)
	for _, record := range records {
		groups[record.GroupID] = append(groups[record.GroupID], record)
	}
	groupIDs := make([]int64, 0, len(groups))
	for groupID := range groups {
		groupIDs = append(groupIDs, groupID)
	}
	slices.Sort(groupIDs)
	result := ExportResult{Dir: runDir, Count: len(records), Files: make([]ExportFile, 0, len(groupIDs))}
	for _, groupID := range groupIDs {
		path := filepath.Join(runDir, fmt.Sprintf("group_%d.xlsx", groupID))
		if err := writeXLSX(path, groups[groupID]); err != nil {
			_ = os.RemoveAll(runDir)
			return ExportResult{}, err
		}
		result.Files = append(result.Files, ExportFile{GroupID: groupID, Path: path, Count: len(groups[groupID])})
	}
	return result, nil
}

// RecordFromEvent parses OneBot group request events that NapCat SDK exposes as UnknownEvent.
func RecordFromEvent(raw []byte) (Record, bool, error) {
	var event struct {
		Time        int64  `json:"time"`
		PostType    string `json:"post_type"`
		RequestType string `json:"request_type"`
		SubType     string `json:"sub_type"`
		GroupID     int64  `json:"group_id"`
		UserID      int64  `json:"user_id"`
		Comment     string `json:"comment"`
		Flag        string `json:"flag"`
	}
	if err := json.Unmarshal(raw, &event); err != nil {
		return Record{}, false, err
	}
	if event.PostType != "request" || event.RequestType != "group" {
		return Record{}, false, nil
	}
	var requestedAt time.Time
	if event.Time > 0 {
		requestedAt = time.Unix(event.Time, 0)
	}
	return Record{
		RequestKey:  event.Flag,
		Flag:        event.Flag,
		GroupID:     event.GroupID,
		UserID:      event.UserID,
		StudentID:   extractStudentID(event.Comment),
		StudentName: extractStudentName(event.Comment),
		SubType:     event.SubType,
		Comment:     event.Comment,
		Status:      StatusPending,
		Source:      SourceEvent,
		RawJSON:     string(raw),
		RequestedAt: requestedAt,
	}, true, nil
}

// RecordsFromSystemMessages normalizes get_group_system_msg join and invite rows.
func RecordsFromSystemMessages(joinRequests, invitedRequests []api.OB11Notify, now time.Time) []Record {
	records := make([]Record, 0, len(joinRequests)+len(invitedRequests))
	for _, raw := range joinRequests {
		records = append(records, recordFromSystemMessage(raw, "add", now))
	}
	for _, raw := range invitedRequests {
		records = append(records, recordFromSystemMessage(raw, "invite", now))
	}
	return records
}

func recordFromSystemMessage(raw api.OB11Notify, subType string, now time.Time) Record {
	flag := ""
	if raw.RequestID > 0 {
		flag = strconv.FormatInt(int64(raw.RequestID), 10)
	}
	status := StatusPending
	if raw.Checked {
		status = StatusSeen
	}
	rawJSON, _ := json.Marshal(raw)
	return Record{
		RequestKey:  flag,
		Flag:        flag,
		GroupID:     int64(raw.GroupID),
		UserID:      int64(raw.InvitorUin),
		StudentID:   extractStudentID(raw.Message),
		StudentName: extractStudentName(raw.Message),
		SubType:     subType,
		Comment:     raw.Message,
		Status:      status,
		Source:      SourceSystem,
		RawJSON:     string(rawJSON),
		RequestedAt: now,
	}
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
	if err := f.SaveAs(path); err != nil {
		return err
	}
	return os.Chmod(path, 0o600)
}

func formatTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.Format("2006-01-02 15:04:05")
}
