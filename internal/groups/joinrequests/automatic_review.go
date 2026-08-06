package joinrequests

import (
	"context"
	"fmt"
	"strings"
	"time"
	"unicode"
)

const (
	EnrollmentYearOffset    = 2
	EnrollmentYearLength    = 4
	MajorCodeOffset         = 6
	MajorCodeLength         = 3
	MinimumEvidenceSamples  = 3
	AutoApprovalRuleVersion = uint64(2)
)

type ReviewOutcome string
type RosterLookupStatus string
type MajorCodeDecision string
type JudgementConfidence string
type RuleStateStatus string

const (
	ReviewPassed            ReviewOutcome = "passed"
	ReviewRejected          ReviewOutcome = "rejected"
	ReviewDependencyPending ReviewOutcome = "dependency_pending"

	RosterNotConfigured RosterLookupStatus = "not_configured"
	RosterMatched       RosterLookupStatus = "matched"
	RosterNotFound      RosterLookupStatus = "not_found"
	RosterMajorMismatch RosterLookupStatus = "major_mismatch"
	RosterUnavailable   RosterLookupStatus = "unavailable"

	MajorCodeMatch     MajorCodeDecision = "match"
	MajorCodeMismatch  MajorCodeDecision = "mismatch"
	MajorCodeUncertain MajorCodeDecision = "uncertain"

	ConfidenceHigh   JudgementConfidence = "high"
	ConfidenceMedium JudgementConfidence = "medium"
	ConfidenceLow    JudgementConfidence = "low"

	RuleStateBuilding RuleStateStatus = "building"
	RuleStateReady    RuleStateStatus = "ready"
	RuleStateFailed   RuleStateStatus = "failed"
)

type StudentIDCheck struct {
	StudentID      string `json:"-"`
	ExpectedLength int    `json:"expected_length"`
	ActualLength   int    `json:"actual_length"`
	LengthValid    bool   `json:"length_valid"`
	Numeric        bool   `json:"numeric"`
	EnrollmentYear string `json:"enrollment_year,omitempty"`
	ExpectedYear   string `json:"expected_year"`
	YearValid      bool   `json:"year_valid"`
	MajorCode      string `json:"major_code,omitempty"`
}

type MajorCount struct {
	Major string `json:"major"`
	Count uint64 `json:"count"`
}

type MajorEvidence struct {
	EnrollmentYear string       `json:"enrollment_year"`
	MajorCode      string       `json:"major_code"`
	TotalSamples   uint64       `json:"total_samples"`
	MajorCounts    []MajorCount `json:"major_counts"`
	Version        uint64       `json:"version"`
}

type MajorCodeJudgement struct {
	Decision   MajorCodeDecision   `json:"decision"`
	Confidence JudgementConfidence `json:"confidence"`
	Reason     string              `json:"reason"`
}

type RosterAssessment struct {
	Status         RosterLookupStatus `json:"status"`
	DatasetVersion *string            `json:"dataset_version,omitempty"`
}

type AutomaticReview struct {
	RuleVersion uint64              `json:"rule_version"`
	Outcome     ReviewOutcome       `json:"outcome"`
	StudentID   StudentIDCheck      `json:"student_id"`
	Roster      RosterAssessment    `json:"roster"`
	Evidence    *MajorEvidence      `json:"evidence,omitempty"`
	Judgement   *MajorCodeJudgement `json:"judgement,omitempty"`
	ReasonCode  string              `json:"reason_code"`
	Reason      string              `json:"reason"`
	ReviewedAt  time.Time           `json:"reviewed_at"`
}

type MajorCodeJudgeInput struct {
	EnrollmentYear  string
	MajorCode       string
	ApplicantMajor  string
	TotalSamples    uint64
	MajorCounts     []MajorCount
	EvidenceVersion uint64
}

type MajorCodeJudge interface {
	Judge(context.Context, MajorCodeJudgeInput) (MajorCodeJudgement, error)
}

type RuleState struct {
	RuleVersion     uint64
	Status          RuleStateStatus
	EvidenceVersion uint64
	ActivatedAt     *time.Time
	RebuiltAt       *time.Time
	LastErrorCode   *string
	Version         uint64
}

type AutomaticRuleConfiguration struct {
	StudentIDLength      int
	EnrollmentYearOffset int
	EnrollmentYearLength int
	MajorCodeOffset      int
	MajorCodeLength      int
	CurrentYear          string
	MinimumSamples       int
}

type EvidenceSummary struct {
	EnrollmentYear string
	MajorCode      string
	TotalSamples   uint64
	MajorCounts    []MajorCount
}

type EvidenceSample struct {
	ID             uint64
	EnrollmentYear string
	MajorCode      string
	Major          string
	ApprovalSource DecisionSource
	SourceGroupID  string
	Active         bool
	Version        uint64
	UpdatedAt      time.Time
}

type EvidenceListQuery struct {
	EnrollmentYear string
	MajorCode      string
	Active         *bool
	Page           int
	Limit          int
}

type EvidenceSamplePatch struct {
	Major  *string
	Active *bool
}

type EvidenceSampleMutation struct {
	Context          MutationContext
	SampleID         uint64
	ExpectedRevision uint64
	Patch            EvidenceSamplePatch
}

type EvidenceRebuildResult struct {
	RuleState   RuleState
	SampleCount uint64
}

type EvidenceRebuildMutation struct {
	Context        MutationContext
	IdempotencyKey string
}

type AdmissionRosterEntry struct {
	StudentID string
	Name      string
	Major     string
}

type AdmissionRosterRecord struct {
	Configured     bool
	Found          bool
	DatasetVersion string
	Major          string
}

type AdmissionRosterStatus struct {
	Configured     bool
	DatasetVersion *string
	FileName       *string
	RowCount       uint64
	ActivatedAt    *time.Time
}

type AdmissionRosterImport struct {
	Context        MutationContext
	IdempotencyKey string
	FileName       string
	ContentHash    string
	Entries        []AdmissionRosterEntry
}

type AdmissionRosterValidationError struct {
	Row     int
	Field   string
	Message string
}

func (e *AdmissionRosterValidationError) Error() string {
	if e == nil {
		return "admission roster is invalid"
	}
	if e.Row > 0 {
		return fmt.Sprintf("第 %d 行：%s", e.Row, e.Message)
	}
	return e.Message
}

func (e *AdmissionRosterValidationError) Unwrap() error { return ErrInvalidInput }

type AdmissionRosterReader interface {
	Lookup(context.Context, string) (AdmissionRosterRecord, error)
}

type AdmissionRosterImporter interface {
	Import(context.Context, AdmissionRosterImport) (AdmissionRosterStatus, error)
}

func CheckStudentID(studentID string, now time.Time) StudentIDCheck {
	result := StudentIDCheck{
		StudentID: studentID, ExpectedLength: StudentIDLength, ActualLength: len(studentID),
		ExpectedYear: now.Format("2006"), LengthValid: len(studentID) == StudentIDLength,
	}
	result.Numeric = result.LengthValid
	for _, character := range studentID {
		if character < '0' || character > '9' {
			result.Numeric = false
			break
		}
	}
	if result.LengthValid && result.Numeric {
		result.EnrollmentYear = studentID[EnrollmentYearOffset : EnrollmentYearOffset+EnrollmentYearLength]
		result.MajorCode = studentID[MajorCodeOffset : MajorCodeOffset+MajorCodeLength]
		result.YearValid = result.EnrollmentYear == result.ExpectedYear
	}
	return result
}

func normalizeMajor(value string) string {
	value = strings.TrimSpace(value)
	return strings.Map(func(character rune) rune {
		if unicode.IsSpace(character) {
			return -1
		}
		return character
	}, value)
}
