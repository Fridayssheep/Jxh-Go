package grouprequest

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/xuri/excelize/v2"
)

type memoryGroupRequestStore struct {
	records []Record
}

func (s *memoryGroupRequestStore) UpsertGroupJoinRequest(ctx context.Context, record Record) error {
	_ = ctx
	for i := range s.records {
		if s.records[i].RequestKey == record.RequestKey {
			s.records[i] = record
			return nil
		}
	}
	s.records = append(s.records, record)
	return nil
}

func (s *memoryGroupRequestStore) ListGroupJoinRequests(ctx context.Context, limit int) ([]Record, error) {
	_ = ctx
	if limit > 0 && len(s.records) > limit {
		return append([]Record(nil), s.records[:limit]...), nil
	}
	return append([]Record(nil), s.records...), nil
}

func TestRecordFromEventNormalizesGroupRequest(t *testing.T) {
	raw := []byte(`{"time":1780000000,"post_type":"request","request_type":"group","sub_type":"add","self_id":999,"group_id":12345,"user_id":67890,"comment":"我是 24 级新生","flag":"flag-1"}`)

	record, ok, err := RecordFromEvent(raw)

	if err != nil {
		t.Fatalf("RecordFromEvent returned error: %v", err)
	}
	if !ok {
		t.Fatal("RecordFromEvent did not recognize group request")
	}
	if record.RequestKey != "flag-1" {
		t.Fatalf("RequestKey = %q, want flag-1", record.RequestKey)
	}
	if record.GroupID != 12345 || record.UserID != 67890 {
		t.Fatalf("group/user = %d/%d, want 12345/67890", record.GroupID, record.UserID)
	}
	if record.Comment != "我是 24 级新生" {
		t.Fatalf("Comment = %q", record.Comment)
	}
	if record.Status != StatusPending || record.Source != SourceEvent {
		t.Fatalf("status/source = %q/%q", record.Status, record.Source)
	}
}

func TestRecordFromEventBoundsLongFlagRequestKey(t *testing.T) {
	longFlag := strings.Repeat("flag", 80)
	raw := []byte(`{"time":1780000000,"post_type":"request","request_type":"group","sub_type":"add","group_id":12345,"user_id":67890,"flag":"` + longFlag + `"}`)

	record, ok, err := RecordFromEvent(raw)

	if err != nil {
		t.Fatalf("RecordFromEvent returned error: %v", err)
	}
	if !ok {
		t.Fatal("RecordFromEvent did not recognize group request")
	}
	if record.Flag != longFlag {
		t.Fatalf("Flag was not preserved")
	}
	if record.RequestKey == longFlag {
		t.Fatal("RequestKey kept an overlong raw flag")
	}
	if len(record.RequestKey) > 191 {
		t.Fatalf("RequestKey length = %d, want <= 191", len(record.RequestKey))
	}
	if !strings.HasPrefix(record.RequestKey, "flag:") {
		t.Fatalf("RequestKey = %q, want flag hash prefix", record.RequestKey)
	}
}

func TestRecordFromEventExtractsLabeledStudentInfo(t *testing.T) {
	raw := []byte(`{"time":1780000000,"post_type":"request","request_type":"group","sub_type":"add","group_id":12345,"user_id":67890,"comment":"姓名：张三 学号：202612345678 专业：计算机","flag":"flag-1"}`)

	record, ok, err := RecordFromEvent(raw)

	if err != nil {
		t.Fatalf("RecordFromEvent returned error: %v", err)
	}
	if !ok {
		t.Fatal("RecordFromEvent did not recognize group request")
	}
	if record.StudentID != "202612345678" {
		t.Fatalf("StudentID = %q", record.StudentID)
	}
	if record.StudentName != "张三" {
		t.Fatalf("StudentName = %q", record.StudentName)
	}
}

func TestRecordFromEventLeavesUnlabeledStudentInfoEmpty(t *testing.T) {
	raw := []byte(`{"time":1780000000,"post_type":"request","request_type":"group","sub_type":"add","group_id":12345,"user_id":67890,"comment":"我是张三 202612345678 计算机","flag":"flag-1"}`)

	record, ok, err := RecordFromEvent(raw)

	if err != nil {
		t.Fatalf("RecordFromEvent returned error: %v", err)
	}
	if !ok {
		t.Fatal("RecordFromEvent did not recognize group request")
	}
	if record.StudentID != "" || record.StudentName != "" {
		t.Fatalf("student info = %q/%q, want empty", record.StudentID, record.StudentName)
	}
}

func TestServiceExportsRequestsToXLSX(t *testing.T) {
	now := time.Date(2026, 7, 10, 20, 30, 0, 0, time.Local)
	dir := t.TempDir()
	store := &memoryGroupRequestStore{records: []Record{{
		ID:          7,
		RequestKey:  "flag-1",
		Flag:        "flag-1",
		GroupID:     12345,
		UserID:      67890,
		StudentID:   "202612345678",
		StudentName: "张三",
		SubType:     "add",
		Comment:     "我是 24 级新生",
		Status:      StatusPending,
		Source:      SourceEvent,
		RequestedAt: now,
		FirstSeenAt: now,
		LastSeenAt:  now,
	}}}
	service := NewService(store, Options{
		ExportDir: dir,
		Now:       func() time.Time { return now },
	})

	result, err := service.Export(context.Background(), 0)

	if err != nil {
		t.Fatalf("Export returned error: %v", err)
	}
	if result.Count != 1 {
		t.Fatalf("exported count = %d, want 1", result.Count)
	}
	if _, err := os.Stat(result.Path); err != nil {
		t.Fatalf("export file does not exist: %v", err)
	}
	if filepath.Dir(result.Path) != dir {
		t.Fatalf("export dir = %q, want %q", filepath.Dir(result.Path), dir)
	}
	f, err := excelize.OpenFile(result.Path)
	if err != nil {
		t.Fatalf("open exported xlsx: %v", err)
	}
	defer f.Close()
	studentID, err := f.GetCellValue("群申请", "D2")
	if err != nil {
		t.Fatalf("read exported xlsx: %v", err)
	}
	if studentID != "202612345678" {
		t.Fatalf("D2 = %q, want student id", studentID)
	}
	studentName, err := f.GetCellValue("群申请", "E2")
	if err != nil {
		t.Fatalf("read exported xlsx: %v", err)
	}
	if studentName != "张三" {
		t.Fatalf("E2 = %q, want student name", studentName)
	}
	cell, err := f.GetCellValue("群申请", "G2")
	if err != nil {
		t.Fatalf("read exported xlsx: %v", err)
	}
	if cell != "我是 24 级新生" {
		t.Fatalf("G2 = %q, want comment", cell)
	}
}
