package joinrequests

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/csv"
	"encoding/hex"
	"fmt"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/xuri/excelize/v2"
	"github.com/zjutjh/jxh-go/internal/management/auth"
)

const maxAdmissionRosterRows = 50_000

func (s *Service) GetAutomaticRuleConfiguration(principal auth.Principal) (AutomaticRuleConfiguration, error) {
	if !principal.Has(auth.PermissionJoinRequestsRead) {
		return AutomaticRuleConfiguration{}, ErrForbidden
	}
	return AutomaticRuleConfiguration{
		StudentIDLength: StudentIDLength, EnrollmentYearOffset: EnrollmentYearOffset,
		EnrollmentYearLength: EnrollmentYearLength, MajorCodeOffset: MajorCodeOffset,
		MajorCodeLength: MajorCodeLength, CurrentYear: s.now().In(s.location).Format("2006"),
		MinimumSamples: MinimumEvidenceSamples,
	}, nil
}

func (s *Service) ListMajorEvidence(ctx context.Context, principal auth.Principal) ([]EvidenceSummary, RuleState, error) {
	if !principal.Has(auth.PermissionJoinRequestsRead) {
		return nil, RuleState{}, ErrForbidden
	}
	items, state, err := s.store.ListMajorEvidence(ctx)
	if err != nil {
		return nil, RuleState{}, fmt.Errorf("list major code evidence: %w", err)
	}
	return items, state, nil
}

func (s *Service) ListMajorEvidenceSamples(ctx context.Context, principal auth.Principal, query EvidenceListQuery) (Page[EvidenceSample], error) {
	if !principal.Has(auth.PermissionJoinRequestsRead) {
		return Page[EvidenceSample]{}, ErrForbidden
	}
	if query.Page == 0 {
		query.Page = 1
	}
	if query.Limit == 0 {
		query.Limit = 50
	}
	if query.Page < 1 || query.Page > 100_000 || query.Limit < 1 || query.Limit > 100 ||
		query.EnrollmentYear != "" && !fourDigits(query.EnrollmentYear) ||
		query.MajorCode != "" && !threeDigits(query.MajorCode) {
		return Page[EvidenceSample]{}, ErrInvalidInput
	}
	page, err := s.store.ListMajorEvidenceSamples(ctx, query)
	if err != nil {
		return Page[EvidenceSample]{}, fmt.Errorf("list major code evidence samples: %w", err)
	}
	return page, nil
}

func (s *Service) UpdateMajorEvidenceSample(ctx context.Context, principal auth.Principal, sampleID, revision uint64, patch EvidenceSamplePatch, request auth.MutationContext) (EvidenceSample, error) {
	if !principal.Has(auth.PermissionJoinPoliciesWrite) {
		return EvidenceSample{}, ErrForbidden
	}
	if sampleID == 0 || revision == 0 || patch.Major == nil && patch.Active == nil || !validMutationRequest(request) {
		return EvidenceSample{}, ErrInvalidInput
	}
	if patch.Major != nil {
		value := strings.TrimSpace(*patch.Major)
		if !validApplicantMajor(value) || strings.ContainsAny(value, "\r\n\t") {
			return EvidenceSample{}, ErrInvalidInput
		}
		patch.Major = &value
	}
	result, err := s.store.UpdateMajorEvidenceSample(ctx, EvidenceSampleMutation{
		Context: mutationContext(principal, request, s.now()), SampleID: sampleID, ExpectedRevision: revision, Patch: patch,
	})
	if err != nil {
		return EvidenceSample{}, fmt.Errorf("update major code evidence sample: %w", err)
	}
	return result, nil
}

func (s *Service) RebuildMajorEvidence(ctx context.Context, principal auth.Principal, idempotencyKey string, request auth.MutationContext) (EvidenceRebuildResult, error) {
	if !principal.Has(auth.PermissionJoinPoliciesWrite) {
		return EvidenceRebuildResult{}, ErrForbidden
	}
	if !idempotencyKeyPattern.MatchString(idempotencyKey) || !validMutationRequest(request) {
		return EvidenceRebuildResult{}, ErrInvalidInput
	}
	result, err := s.store.RebuildMajorEvidence(ctx, EvidenceRebuildMutation{
		Context: mutationContext(principal, request, s.now()), IdempotencyKey: idempotencyKey,
	})
	if err != nil {
		return EvidenceRebuildResult{}, fmt.Errorf("rebuild major code evidence: %w", err)
	}
	return result, nil
}

func (s *Service) GetAdmissionRosterStatus(ctx context.Context, principal auth.Principal) (AdmissionRosterStatus, error) {
	if !principal.Has(auth.PermissionJoinRequestsRead) {
		return AdmissionRosterStatus{}, ErrForbidden
	}
	result, err := s.store.GetAdmissionRosterStatus(ctx)
	if err != nil {
		return AdmissionRosterStatus{}, fmt.Errorf("get admission roster status: %w", err)
	}
	return result, nil
}

func (s *Service) ImportAdmissionRoster(ctx context.Context, principal auth.Principal, fileName string, data []byte, idempotencyKey string, request auth.MutationContext) (AdmissionRosterStatus, error) {
	if !principal.Has(auth.PermissionJoinPoliciesWrite) {
		return AdmissionRosterStatus{}, ErrForbidden
	}
	fileName = filepath.Base(strings.TrimSpace(fileName))
	if fileName == "." || fileName == "" || !utf8.ValidString(fileName) || utf8.RuneCountInString(fileName) > 255 ||
		len(data) == 0 || !idempotencyKeyPattern.MatchString(idempotencyKey) || !validMutationRequest(request) {
		return AdmissionRosterStatus{}, ErrInvalidInput
	}
	entries, err := parseAdmissionRoster(fileName, data)
	if err != nil {
		return AdmissionRosterStatus{}, err
	}
	digest := sha256.Sum256(data)
	result, err := s.rosterImporter.Import(ctx, AdmissionRosterImport{
		Context: mutationContext(principal, request, s.now()), IdempotencyKey: idempotencyKey,
		FileName: fileName, ContentHash: hex.EncodeToString(digest[:]), Entries: entries,
	})
	if err != nil {
		return AdmissionRosterStatus{}, fmt.Errorf("import admission roster: %w", err)
	}
	return result, nil
}

func parseAdmissionRoster(fileName string, data []byte) ([]AdmissionRosterEntry, error) {
	extension := strings.ToLower(filepath.Ext(fileName))
	var rows [][]string
	var err error
	switch extension {
	case ".csv":
		reader := csv.NewReader(bytes.NewReader(data))
		reader.FieldsPerRecord = -1
		reader.TrimLeadingSpace = true
		rows, err = reader.ReadAll()
	case ".xlsx":
		var workbook *excelize.File
		workbook, err = excelize.OpenReader(bytes.NewReader(data))
		if err == nil {
			defer workbook.Close()
			sheets := workbook.GetSheetList()
			if len(sheets) == 0 {
				return nil, rosterValidationError(0, "file", "工作簿中没有可读取的工作表")
			}
			rows, err = workbook.GetRows(sheets[0])
		}
	default:
		return nil, rosterValidationError(0, "file", "仅支持 CSV 或 XLSX 文件")
	}
	if err != nil {
		return nil, rosterValidationError(0, "file", "文件内容无法解析")
	}
	if len(rows) < 2 {
		return nil, rosterValidationError(0, "file", "文件必须包含表头和至少一条名单记录")
	}
	if len(rows) > maxAdmissionRosterRows+1 {
		return nil, rosterValidationError(0, "file", fmt.Sprintf("名单不能超过 %d 条记录", maxAdmissionRosterRows))
	}
	return admissionEntriesFromRows(rows)
}

func admissionEntriesFromRows(rows [][]string) ([]AdmissionRosterEntry, error) {
	headers := make(map[string]int)
	for index, header := range rows[0] {
		header = strings.TrimSpace(strings.TrimPrefix(header, "\ufeff"))
		switch strings.ToLower(header) {
		case "学号", "student_id", "student id":
			headers["student_id"] = index
		case "姓名", "name", "student_name":
			headers["name"] = index
		case "专业", "major":
			headers["major"] = index
		}
	}
	studentIDColumn, ok := headers["student_id"]
	if !ok {
		return nil, rosterValidationError(1, "student_id", "缺少“学号”或 student_id 表头")
	}
	seen := make(map[string]struct{}, len(rows)-1)
	entries := make([]AdmissionRosterEntry, 0, len(rows)-1)
	for index, row := range rows[1:] {
		rowNumber := index + 2
		studentID := rowValue(row, studentIDColumn)
		if studentID == "" && rowEmpty(row) {
			continue
		}
		check := CheckStudentID(studentID, time.Now())
		if !check.LengthValid || !check.Numeric {
			return nil, rosterValidationError(rowNumber, "student_id", "学号必须为 12 位纯数字")
		}
		if _, duplicate := seen[studentID]; duplicate {
			return nil, rosterValidationError(rowNumber, "student_id", "学号在文件中重复")
		}
		seen[studentID] = struct{}{}
		entry := AdmissionRosterEntry{StudentID: studentID}
		if index, exists := headers["name"]; exists {
			entry.Name = rowValue(row, index)
			if entry.Name != "" && utf8.RuneCountInString(entry.Name) > 64 {
				return nil, rosterValidationError(rowNumber, "name", "姓名不能超过 64 个字符")
			}
		}
		if index, exists := headers["major"]; exists {
			entry.Major = rowValue(row, index)
			if entry.Major != "" && !validApplicantMajor(entry.Major) {
				return nil, rosterValidationError(rowNumber, "major", "专业名称格式无效")
			}
		}
		entries = append(entries, entry)
	}
	if len(entries) == 0 || len(entries) > maxAdmissionRosterRows {
		return nil, rosterValidationError(0, "file", "文件中没有有效名单记录")
	}
	return entries, nil
}

func rosterValidationError(row int, field, message string) error {
	return &AdmissionRosterValidationError{Row: row, Field: field, Message: message}
}

func rowValue(row []string, index int) string {
	if index < 0 || index >= len(row) {
		return ""
	}
	return strings.TrimSpace(row[index])
}

func rowEmpty(row []string) bool {
	for _, value := range row {
		if strings.TrimSpace(value) != "" {
			return false
		}
	}
	return true
}

func fourDigits(value string) bool {
	return len(value) == 4 && allDigits(value)
}

func threeDigits(value string) bool {
	return len(value) == 3 && allDigits(value)
}

func allDigits(value string) bool {
	for _, character := range value {
		if character < '0' || character > '9' {
			return false
		}
	}
	return true
}
