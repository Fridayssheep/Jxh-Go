package adminapi

import (
	"errors"
	"net/http"
	"time"

	"github.com/zjutjh/jxh-go/internal/groups/joinrequests"
	"github.com/zjutjh/jxh-go/internal/management/auth"
)

type studentIDSegmentDTO struct {
	Offset int `json:"offset"`
	Length int `json:"length"`
}

type studentIDSegmentInput struct {
	Offset *int `json:"offset"`
	Length *int `json:"length"`
}

type studentMajorMappingDTO struct {
	EnrollmentYear int      `json:"enrollment_year"`
	MajorCode      string   `json:"major_code"`
	MajorName      string   `json:"major_name"`
	Aliases        []string `json:"aliases"`
}

type studentMajorMappingInput struct {
	EnrollmentYear int       `json:"enrollment_year"`
	MajorCode      string    `json:"major_code"`
	MajorName      string    `json:"major_name"`
	Aliases        *[]string `json:"aliases"`
}

type studentIDRuleDTO struct {
	Enabled               bool                     `json:"enabled"`
	StudentIDLength       int                      `json:"student_id_length"`
	EnrollmentYearSegment *studentIDSegmentDTO     `json:"enrollment_year_segment"`
	MajorCodeSegment      *studentIDSegmentDTO     `json:"major_code_segment"`
	Mappings              []studentMajorMappingDTO `json:"mappings"`
	Version               uint64                   `json:"version"`
	UpdatedAt             time.Time                `json:"updated_at"`
	UpdatedBy             *auditActorDTO           `json:"updated_by"`
}

type studentIDAssessmentDTO struct {
	Status         joinrequests.StudentIDAssessmentStatus `json:"status"`
	RuleVersion    uint64                                 `json:"rule_version"`
	EnrollmentYear *int                                   `json:"enrollment_year"`
	MajorCode      *string                                `json:"major_code"`
	ExpectedMajor  *string                                `json:"expected_major"`
	MajorMatches   *bool                                  `json:"major_matches"`
	Warnings       []joinrequests.StudentIDWarning        `json:"warnings"`
}

type studentIDRulePatchFields struct {
	Enabled               rawSettingsField `json:"enabled"`
	EnrollmentYearSegment rawSettingsField `json:"enrollment_year_segment"`
	MajorCodeSegment      rawSettingsField `json:"major_code_segment"`
	Mappings              rawSettingsField `json:"mappings"`
}

func (h *JoinRequestHandlers) getStudentIDRule(w http.ResponseWriter, r *http.Request) {
	identity, _ := AuthFromContext(r.Context())
	value, err := h.service.GetStudentIDRule(r.Context(), principalFromAuth(identity))
	if err != nil {
		h.writeServiceError(w, r, err)
		return
	}
	setRevisionETag(w, value.Version)
	writeJSON(w, http.StatusOK, mapStudentIDRule(value))
}

func (h *JoinRequestHandlers) updateStudentIDRule(w http.ResponseWriter, r *http.Request) {
	revision, ok := requiredRevision(w, r)
	if !ok {
		return
	}
	patch, ok := decodeStudentIDRulePatch(w, r)
	if !ok {
		return
	}
	identity, _ := AuthFromContext(r.Context())
	value, err := h.service.UpdateStudentIDRule(
		r.Context(), principalFromAuth(identity), revision, patch, mutationContextFromRequest(r),
	)
	if err != nil {
		if errors.Is(err, joinrequests.ErrConflict) {
			writeAPIError(w, r, http.StatusConflict, "resource_version_conflict", "student ID rule was changed by another operation", nil, false)
			return
		}
		h.writeServiceError(w, r, err)
		return
	}
	setRevisionETag(w, value.Version)
	writeJSON(w, http.StatusOK, mapStudentIDRule(value))
}

func decodeStudentIDRulePatch(w http.ResponseWriter, r *http.Request) (joinrequests.StudentIDRulePatch, bool) {
	var body studentIDRulePatchFields
	if !decodeRequestJSON(w, r, &body) {
		return joinrequests.StudentIDRulePatch{}, false
	}
	patch := joinrequests.StudentIDRulePatch{}
	if body.Enabled.Set {
		if body.Enabled.Null || decodeStrictJSONBytes(body.Enabled.data, &patch.Enabled.Value) != nil {
			return invalidStudentIDRulePatch(w, r)
		}
		patch.Enabled.Set = true
	}
	var ok bool
	patch.EnrollmentYearSegment, ok = decodeStudentIDSegmentPatch(body.EnrollmentYearSegment)
	if !ok {
		return invalidStudentIDRulePatch(w, r)
	}
	patch.MajorCodeSegment, ok = decodeStudentIDSegmentPatch(body.MajorCodeSegment)
	if !ok {
		return invalidStudentIDRulePatch(w, r)
	}
	if body.Mappings.Set {
		if body.Mappings.Null {
			return invalidStudentIDRulePatch(w, r)
		}
		var values []studentMajorMappingInput
		if decodeStrictJSONBytes(body.Mappings.data, &values) != nil {
			return invalidStudentIDRulePatch(w, r)
		}
		mappings := make([]joinrequests.StudentMajorMapping, len(values))
		for index, value := range values {
			if value.Aliases == nil {
				return invalidStudentIDRulePatch(w, r)
			}
			mappings[index] = joinrequests.StudentMajorMapping{
				EnrollmentYear: value.EnrollmentYear, MajorCode: value.MajorCode,
				MajorName: value.MajorName, Aliases: append([]string{}, (*value.Aliases)...),
			}
		}
		patch.Mappings = auth.Field[[]joinrequests.StudentMajorMapping]{Set: true, Value: mappings}
	}
	if !patch.Enabled.Set && !patch.EnrollmentYearSegment.Set && !patch.MajorCodeSegment.Set && !patch.Mappings.Set {
		return invalidStudentIDRulePatch(w, r)
	}
	return patch, true
}

func decodeStudentIDSegmentPatch(field rawSettingsField) (auth.Field[*joinrequests.StudentIDSegment], bool) {
	if !field.Set {
		return auth.Field[*joinrequests.StudentIDSegment]{}, true
	}
	if field.Null {
		return auth.Field[*joinrequests.StudentIDSegment]{Set: true}, true
	}
	var value studentIDSegmentInput
	if decodeStrictJSONBytes(field.data, &value) != nil || value.Offset == nil || value.Length == nil {
		return auth.Field[*joinrequests.StudentIDSegment]{}, false
	}
	return auth.Field[*joinrequests.StudentIDSegment]{
		Set: true, Value: &joinrequests.StudentIDSegment{Offset: *value.Offset, Length: *value.Length},
	}, true
}

func invalidStudentIDRulePatch(w http.ResponseWriter, r *http.Request) (joinrequests.StudentIDRulePatch, bool) {
	writeAPIError(w, r, http.StatusBadRequest, CodeBadRequest, "student ID rule patch is invalid", nil, false)
	return joinrequests.StudentIDRulePatch{}, false
}

func mapStudentIDRule(value joinrequests.StudentIDRule) studentIDRuleDTO {
	mappings := make([]studentMajorMappingDTO, len(value.Mappings))
	for index, mapping := range value.Mappings {
		mappings[index] = studentMajorMappingDTO{
			EnrollmentYear: mapping.EnrollmentYear, MajorCode: mapping.MajorCode,
			MajorName: mapping.MajorName, Aliases: append([]string{}, mapping.Aliases...),
		}
	}
	return studentIDRuleDTO{
		Enabled: value.Enabled, StudentIDLength: joinrequests.StudentIDLength,
		EnrollmentYearSegment: mapStudentIDSegment(value.EnrollmentYearSegment),
		MajorCodeSegment:      mapStudentIDSegment(value.MajorCodeSegment), Mappings: mappings,
		Version: value.Version, UpdatedAt: value.UpdatedAt.UTC(), UpdatedBy: mapOptionalAuditActor(value.UpdatedBy),
	}
}

func mapStudentIDSegment(value *joinrequests.StudentIDSegment) *studentIDSegmentDTO {
	if value == nil {
		return nil
	}
	return &studentIDSegmentDTO{Offset: value.Offset, Length: value.Length}
}

func mapStudentIDAssessment(value joinrequests.StudentIDAssessment) studentIDAssessmentDTO {
	return studentIDAssessmentDTO{
		Status: value.Status, RuleVersion: value.RuleVersion, EnrollmentYear: value.EnrollmentYear,
		MajorCode: value.MajorCode, ExpectedMajor: value.ExpectedMajor, MajorMatches: value.MajorMatches,
		Warnings: append([]joinrequests.StudentIDWarning{}, value.Warnings...),
	}
}
