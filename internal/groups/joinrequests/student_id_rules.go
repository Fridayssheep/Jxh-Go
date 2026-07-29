package joinrequests

import (
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/zjutjh/jxh-go/internal/management/audit"
	"github.com/zjutjh/jxh-go/internal/management/auth"
)

const (
	StudentIDLength          = 12
	maxStudentMajorMappings  = 1000
	maxStudentMajorAliases   = 20
	maxStudentMajorCodeRunes = 12
)

type StudentIDAssessmentStatus string
type StudentIDWarning string

const (
	StudentIDAssessmentUnconfigured StudentIDAssessmentStatus = "unconfigured"
	StudentIDAssessmentMatched      StudentIDAssessmentStatus = "matched"
	StudentIDAssessmentWarning      StudentIDAssessmentStatus = "warning"

	StudentIDMissing                StudentIDWarning = "student_id_missing"
	StudentIDNotNumeric             StudentIDWarning = "student_id_not_numeric"
	StudentIDLengthMismatch         StudentIDWarning = "student_id_length_mismatch"
	StudentIDEnrollmentYearUnmapped StudentIDWarning = "enrollment_year_unmapped"
	StudentIDMajorCodeUnmapped      StudentIDWarning = "major_code_unmapped"
	StudentIDMajorMissing           StudentIDWarning = "major_missing"
	StudentIDMajorMismatch          StudentIDWarning = "major_mismatch"
)

type StudentIDSegment struct {
	Offset int
	Length int
}

type StudentMajorMapping struct {
	EnrollmentYear int
	MajorCode      string
	MajorName      string
	Aliases        []string
}

type StudentIDRule struct {
	Enabled               bool
	EnrollmentYearSegment *StudentIDSegment
	MajorCodeSegment      *StudentIDSegment
	Mappings              []StudentMajorMapping
	Version               uint64
	UpdatedAt             time.Time
	UpdatedBy             *audit.Actor
}

type StudentIDRulePatch struct {
	Enabled               auth.Field[bool]
	EnrollmentYearSegment auth.Field[*StudentIDSegment]
	MajorCodeSegment      auth.Field[*StudentIDSegment]
	Mappings              auth.Field[[]StudentMajorMapping]
}

type StudentIDRuleMutation struct {
	Context          MutationContext
	ExpectedRevision uint64
	Rule             StudentIDRule
}

type StudentIDAssessment struct {
	Status         StudentIDAssessmentStatus
	RuleVersion    uint64
	EnrollmentYear *int
	MajorCode      *string
	ExpectedMajor  *string
	MajorMatches   *bool
	Warnings       []StudentIDWarning
}

func AssessStudentID(rule StudentIDRule, fields *ApplicantFields) StudentIDAssessment {
	result := StudentIDAssessment{
		Status: StudentIDAssessmentUnconfigured, RuleVersion: rule.Version, Warnings: []StudentIDWarning{},
	}
	if !studentIDRuleConfigured(rule) {
		return result
	}
	result.Status = StudentIDAssessmentWarning
	studentID := ""
	major := ""
	if fields != nil {
		studentID = optionalValue(fields.StudentID)
		major = optionalValue(fields.Major)
	}
	if strings.TrimSpace(studentID) == "" {
		result.Warnings = append(result.Warnings, StudentIDMissing)
		return result
	}
	if utf8.RuneCountInString(studentID) != StudentIDLength {
		result.Warnings = append(result.Warnings, StudentIDLengthMismatch)
		return result
	}
	if !asciiDigits(studentID) {
		result.Warnings = append(result.Warnings, StudentIDNotNumeric)
		return result
	}

	yearSegment := rule.EnrollmentYearSegment
	majorSegment := rule.MajorCodeSegment
	year, _ := strconv.Atoi(studentID[yearSegment.Offset : yearSegment.Offset+yearSegment.Length])
	code := studentID[majorSegment.Offset : majorSegment.Offset+majorSegment.Length]
	result.EnrollmentYear = &year
	result.MajorCode = &code

	var mapping *StudentMajorMapping
	yearConfigured := false
	for index := range rule.Mappings {
		candidate := &rule.Mappings[index]
		if candidate.EnrollmentYear != year {
			continue
		}
		yearConfigured = true
		if candidate.MajorCode == code {
			mapping = candidate
			break
		}
	}
	if !yearConfigured {
		result.Warnings = append(result.Warnings, StudentIDEnrollmentYearUnmapped)
		return result
	}
	if mapping == nil {
		result.Warnings = append(result.Warnings, StudentIDMajorCodeUnmapped)
		return result
	}

	expected := mapping.MajorName
	result.ExpectedMajor = &expected
	major = strings.TrimSpace(major)
	if major == "" {
		result.Warnings = append(result.Warnings, StudentIDMajorMissing)
		return result
	}
	matches := strings.EqualFold(major, strings.TrimSpace(mapping.MajorName))
	if !matches {
		for _, alias := range mapping.Aliases {
			if strings.EqualFold(major, strings.TrimSpace(alias)) {
				matches = true
				break
			}
		}
	}
	result.MajorMatches = &matches
	if !matches {
		result.Warnings = append(result.Warnings, StudentIDMajorMismatch)
		return result
	}
	result.Status = StudentIDAssessmentMatched
	return result
}

func validStudentIDRule(rule StudentIDRule) bool {
	if rule.Version == 0 || !validUTCTime(rule.UpdatedAt) || !validOptionalActor(rule.UpdatedBy) ||
		len(rule.Mappings) > maxStudentMajorMappings {
		return false
	}
	if !validStudentIDSegment(rule.EnrollmentYearSegment) || !validStudentIDSegment(rule.MajorCodeSegment) {
		return false
	}
	if rule.EnrollmentYearSegment != nil && rule.EnrollmentYearSegment.Length != 4 {
		return false
	}
	if segmentsOverlap(rule.EnrollmentYearSegment, rule.MajorCodeSegment) {
		return false
	}
	codeLength := 0
	if rule.MajorCodeSegment != nil {
		codeLength = rule.MajorCodeSegment.Length
	}
	seenMappings := make(map[string]struct{}, len(rule.Mappings))
	for _, mapping := range rule.Mappings {
		if mapping.EnrollmentYear < 1000 || mapping.EnrollmentYear > 9999 ||
			!validStudentMajorCode(mapping.MajorCode, codeLength) ||
			!validTrimmedText(mapping.MajorName, 128) || len(mapping.Aliases) > maxStudentMajorAliases {
			return false
		}
		key := strconv.Itoa(mapping.EnrollmentYear) + "\x00" + mapping.MajorCode
		if _, exists := seenMappings[key]; exists {
			return false
		}
		seenMappings[key] = struct{}{}
		seenNames := map[string]struct{}{strings.ToLower(mapping.MajorName): {}}
		for _, alias := range mapping.Aliases {
			if !validTrimmedText(alias, 128) {
				return false
			}
			normalized := strings.ToLower(alias)
			if _, exists := seenNames[normalized]; exists {
				return false
			}
			seenNames[normalized] = struct{}{}
		}
	}
	if !rule.Enabled {
		return true
	}
	return studentIDRuleConfigured(rule)
}

func studentIDRulePatchSet(patch StudentIDRulePatch) bool {
	return patch.Enabled.Set || patch.EnrollmentYearSegment.Set || patch.MajorCodeSegment.Set || patch.Mappings.Set
}

func applyStudentIDRulePatch(current StudentIDRule, patch StudentIDRulePatch) StudentIDRule {
	result := cloneStudentIDRule(current)
	if patch.Enabled.Set {
		result.Enabled = patch.Enabled.Value
	}
	if patch.EnrollmentYearSegment.Set {
		result.EnrollmentYearSegment = cloneStudentIDSegment(patch.EnrollmentYearSegment.Value)
	}
	if patch.MajorCodeSegment.Set {
		result.MajorCodeSegment = cloneStudentIDSegment(patch.MajorCodeSegment.Value)
	}
	if patch.Mappings.Set {
		result.Mappings = cloneStudentMajorMappings(patch.Mappings.Value)
	}
	return result
}

func sameStudentIDRuleConfiguration(left, right StudentIDRule) bool {
	if left.Enabled != right.Enabled || !sameStudentIDSegment(left.EnrollmentYearSegment, right.EnrollmentYearSegment) ||
		!sameStudentIDSegment(left.MajorCodeSegment, right.MajorCodeSegment) || len(left.Mappings) != len(right.Mappings) {
		return false
	}
	for index := range left.Mappings {
		leftMapping, rightMapping := left.Mappings[index], right.Mappings[index]
		if leftMapping.EnrollmentYear != rightMapping.EnrollmentYear || leftMapping.MajorCode != rightMapping.MajorCode ||
			leftMapping.MajorName != rightMapping.MajorName || len(leftMapping.Aliases) != len(rightMapping.Aliases) {
			return false
		}
		for aliasIndex := range leftMapping.Aliases {
			if leftMapping.Aliases[aliasIndex] != rightMapping.Aliases[aliasIndex] {
				return false
			}
		}
	}
	return true
}

func sameStudentIDSegment(left, right *StudentIDSegment) bool {
	return left == nil && right == nil || left != nil && right != nil && *left == *right
}

func studentIDRuleConfigured(rule StudentIDRule) bool {
	return rule.Enabled && rule.EnrollmentYearSegment != nil && rule.MajorCodeSegment != nil &&
		rule.EnrollmentYearSegment.Length == 4 && validStudentIDSegment(rule.EnrollmentYearSegment) &&
		validStudentIDSegment(rule.MajorCodeSegment) && !segmentsOverlap(rule.EnrollmentYearSegment, rule.MajorCodeSegment) &&
		len(rule.Mappings) > 0 && len(rule.Mappings) <= maxStudentMajorMappings
}

func validStudentIDSegment(segment *StudentIDSegment) bool {
	return segment == nil || segment.Offset >= 0 && segment.Length > 0 &&
		segment.Offset+segment.Length <= StudentIDLength
}

func segmentsOverlap(left, right *StudentIDSegment) bool {
	if left == nil || right == nil {
		return false
	}
	return left.Offset < right.Offset+right.Length && right.Offset < left.Offset+left.Length
}

func validStudentMajorCode(code string, configuredLength int) bool {
	length := utf8.RuneCountInString(code)
	if length < 1 || length > maxStudentMajorCodeRunes || configuredLength > 0 && length != configuredLength {
		return false
	}
	return asciiDigits(code)
}

func validTrimmedText(value string, maximum int) bool {
	return value == strings.TrimSpace(value) && validText(value, maximum, false)
}

func asciiDigits(value string) bool {
	if value == "" {
		return false
	}
	for _, character := range value {
		if character < '0' || character > '9' {
			return false
		}
	}
	return true
}

func cloneStudentIDRule(value StudentIDRule) StudentIDRule {
	value.EnrollmentYearSegment = cloneStudentIDSegment(value.EnrollmentYearSegment)
	value.MajorCodeSegment = cloneStudentIDSegment(value.MajorCodeSegment)
	value.Mappings = cloneStudentMajorMappings(value.Mappings)
	value.UpdatedBy = cloneActor(value.UpdatedBy)
	value.UpdatedAt = value.UpdatedAt.UTC()
	return value
}

func cloneStudentIDSegment(value *StudentIDSegment) *StudentIDSegment {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func cloneStudentMajorMappings(values []StudentMajorMapping) []StudentMajorMapping {
	result := make([]StudentMajorMapping, len(values))
	for index, value := range values {
		result[index] = value
		result[index].Aliases = append([]string{}, value.Aliases...)
	}
	return result
}

func cloneStudentIDAssessment(value StudentIDAssessment) StudentIDAssessment {
	if value.EnrollmentYear != nil {
		copy := *value.EnrollmentYear
		value.EnrollmentYear = &copy
	}
	value.MajorCode = cloneString(value.MajorCode)
	value.ExpectedMajor = cloneString(value.ExpectedMajor)
	if value.MajorMatches != nil {
		copy := *value.MajorMatches
		value.MajorMatches = &copy
	}
	value.Warnings = append([]StudentIDWarning{}, value.Warnings...)
	return value
}
