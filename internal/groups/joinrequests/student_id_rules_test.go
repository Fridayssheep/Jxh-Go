package joinrequests

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/zjutjh/jxh-go/internal/management/auth"
	"github.com/zjutjh/jxh-go/internal/management/events"
)

func TestServiceReloadsRuleAndAssessesListAndDetail(t *testing.T) {
	rule := studentIDRuleFixture()
	request := joinRequestFixture("flag_1", DecisionPending, 1)
	studentID, major := "302025315326", "自动化"
	request.VerificationMessage = "学号302025315326 姓名张三 专业自动化"
	request.AIParse.Fields.StudentID = &studentID
	request.AIParse.Fields.Major = &major
	store := &joinStoreFake{
		studentIDRule: rule, studentIDRuleFound: true,
		requestPage: Page[Request]{Items: []Request{request}, TotalCount: 1}, request: request, requestFound: true,
	}
	service := newJoinService(t, store, &joinApproverFake{available: true})
	if err := service.ReloadStudentIDRule(t.Context()); err != nil {
		t.Fatal(err)
	}

	page, err := service.List(t.Context(), joinMaintainer(), ListQuery{})
	if err != nil || len(page.Items) != 1 || page.Items[0].StudentIDAssessment.Status != StudentIDAssessmentWarning ||
		!equalStudentIDWarnings(page.Items[0].StudentIDAssessment.Warnings, []StudentIDWarning{StudentIDMajorMismatch}) {
		t.Fatalf("page=%+v error=%v", page, err)
	}
	detail, err := service.Get(t.Context(), joinMaintainer(), request.ID)
	if err != nil || detail.StudentIDAssessment.Status != StudentIDAssessmentWarning {
		t.Fatalf("detail=%+v error=%v", detail, err)
	}
}

func TestServiceUpdatesStudentIDRuleAfterPersistence(t *testing.T) {
	current := studentIDRuleFixture()
	current.Enabled = false
	store := &joinStoreFake{studentIDRule: current, studentIDRuleFound: true}
	service := newJoinService(t, store, &joinApproverFake{available: true})
	publisher := &studentIDRuleEventPublisherFake{}
	service.events = publisher
	if err := service.ReloadStudentIDRule(t.Context()); err != nil {
		t.Fatal(err)
	}
	enabled := true
	patch := StudentIDRulePatch{Enabled: authField(enabled)}
	updated, err := service.UpdateStudentIDRule(t.Context(), joinSuperAdmin(), current.Version, patch, joinMutationRequest())
	if err != nil || !updated.Enabled || updated.Version != current.Version+1 ||
		store.studentIDRuleMutation.ExpectedRevision != current.Version {
		t.Fatalf("updated=%+v mutation=%+v error=%v", updated, store.studentIDRuleMutation, err)
	}
	loaded, err := service.GetStudentIDRule(t.Context(), joinMaintainer())
	if err != nil || !loaded.Enabled || loaded.Version != updated.Version {
		t.Fatalf("loaded=%+v error=%v", loaded, err)
	}
	if len(publisher.drafts) != 1 || publisher.drafts[0].Type != events.EventSettingsUpdated ||
		publisher.drafts[0].Resource == nil || publisher.drafts[0].Resource.ID != "student_id_rule" ||
		publisher.drafts[0].Resource.Version != updated.Version {
		t.Fatalf("events=%+v", publisher.drafts)
	}
}

func TestServiceRejectsInvalidOrStaleStudentIDRulePatchBeforePersistence(t *testing.T) {
	current := studentIDRuleFixture()
	store := &joinStoreFake{studentIDRule: current, studentIDRuleFound: true}
	service := newJoinService(t, store, &joinApproverFake{available: true})
	if err := service.ReloadStudentIDRule(t.Context()); err != nil {
		t.Fatal(err)
	}
	overlap := &StudentIDSegment{Offset: 5, Length: 3}
	_, err := service.UpdateStudentIDRule(t.Context(), joinSuperAdmin(), current.Version,
		StudentIDRulePatch{MajorCodeSegment: auth.Field[*StudentIDSegment]{Set: true, Value: overlap}}, joinMutationRequest())
	if !errors.Is(err, ErrInvalidInput) || store.studentIDRuleMutation.ExpectedRevision != 0 {
		t.Fatalf("invalid patch error=%v mutation=%+v", err, store.studentIDRuleMutation)
	}
	_, err = service.UpdateStudentIDRule(t.Context(), joinSuperAdmin(), current.Version-1,
		StudentIDRulePatch{Enabled: authField(false)}, joinMutationRequest())
	if !errors.Is(err, ErrConflict) || store.studentIDRuleMutation.ExpectedRevision != 0 {
		t.Fatalf("stale patch error=%v mutation=%+v", err, store.studentIDRuleMutation)
	}
	_, err = service.UpdateStudentIDRule(t.Context(), auth.Principal{Role: auth.RoleObserver}, current.Version,
		StudentIDRulePatch{Enabled: authField(false)}, joinMutationRequest())
	if !errors.Is(err, ErrForbidden) {
		t.Fatalf("observer update error=%v", err)
	}
}

func TestStudentIDWarningDoesNotBlockAutomaticApproval(t *testing.T) {
	rule := studentIDRuleFixture()
	request := joinRequestFixture("flag_warning", DecisionPending, 3)
	studentID, major := "302025315326", "自动化"
	request.VerificationMessage = "学号302025315326 姓名张三 专业自动化"
	request.AIParse.Fields.StudentID = &studentID
	request.AIParse.Fields.Major = &major
	request.AIParse.Fields.Valid = true
	policy := joinPolicyFixture()
	policy.Enabled = true
	store := &joinStoreFake{
		studentIDRule: rule, studentIDRuleFound: true,
		autoCandidates: []AutoCandidate{{Request: request, Policy: policy}},
	}
	store.begin = func(mutation BeginMutation) (Reservation, error) {
		processing := cloneRequest(request)
		processing.DecisionStatus = DecisionProcessing
		processing.DecisionSource = decisionSourcePointer(SourceAutomatic)
		processing.Version++
		decision := decisionFixture(request.ID, "dec_warning", ActionApprove, AttemptStarted)
		decision.Source = SourceAutomatic
		decision.RuleVersion = uint64Pointer(AutoApprovalRuleVersion)
		snapshot := cloneApplicantFields(mutation.FieldSnapshots[request.ID])
		decision.FieldSnapshot = &snapshot
		return Reservation{Items: []ReservedItem{{Request: processing, Decision: decision}}}, nil
	}
	store.complete = func(mutation CompletionMutation) (DecisionResult, error) {
		processing := cloneRequest(request)
		processing.DecisionStatus = DecisionProcessing
		processing.DecisionSource = decisionSourcePointer(SourceAutomatic)
		processing.Version++
		decision := decisionFixture(request.ID, "dec_warning", ActionApprove, AttemptStarted)
		decision.Source = SourceAutomatic
		decision.RuleVersion = uint64Pointer(AutoApprovalRuleVersion)
		decision.FieldSnapshot = cloneApplicantFieldsPointer(request.AIParse.Fields)
		return completedDecisionResult(processing, decision, mutation), nil
	}
	approver := &joinApproverFake{available: true}
	service := newJoinService(t, store, approver)
	if err := service.ReloadStudentIDRule(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := service.ProcessAutoApprovals(t.Context()); err != nil {
		t.Fatal(err)
	}
	if len(approver.flags) != 1 || approver.flags[0] != request.ID {
		t.Fatalf("automatic approvals=%v", approver.flags)
	}
}

func TestAssessStudentIDMatchesConfiguredMajor(t *testing.T) {
	rule := studentIDRuleFixture()
	studentID, major := "302025315326", "计算机"
	fields := ApplicantFields{StudentID: &studentID, Major: &major, Valid: true}

	assessment := AssessStudentID(rule, &fields)

	if assessment.Status != StudentIDAssessmentMatched || assessment.RuleVersion != 3 ||
		assessment.EnrollmentYear == nil || *assessment.EnrollmentYear != 2025 ||
		assessment.MajorCode == nil || *assessment.MajorCode != "315" ||
		assessment.ExpectedMajor == nil || *assessment.ExpectedMajor != "计算机类" ||
		assessment.MajorMatches == nil || !*assessment.MajorMatches || len(assessment.Warnings) != 0 {
		t.Fatalf("assessment=%+v", assessment)
	}
	if !fields.Valid || fields.Major == nil || *fields.Major != major {
		t.Fatalf("assessment mutated applicant fields: %+v", fields)
	}
}

func TestAssessStudentIDReturnsDeterministicWarnings(t *testing.T) {
	rule := studentIDRuleFixture()
	tests := []struct {
		name     string
		student  *string
		major    *string
		warnings []StudentIDWarning
	}{
		{name: "missing", warnings: []StudentIDWarning{StudentIDMissing}},
		{name: "non numeric", student: stringPointer("302025A15326"), major: stringPointer("计算机类"), warnings: []StudentIDWarning{StudentIDNotNumeric}},
		{name: "wrong length", student: stringPointer("2025315326"), major: stringPointer("计算机类"), warnings: []StudentIDWarning{StudentIDLengthMismatch}},
		{name: "year unmapped", student: stringPointer("302024315326"), major: stringPointer("计算机类"), warnings: []StudentIDWarning{StudentIDEnrollmentYearUnmapped}},
		{name: "code unmapped", student: stringPointer("302025999326"), major: stringPointer("计算机类"), warnings: []StudentIDWarning{StudentIDMajorCodeUnmapped}},
		{name: "major missing", student: stringPointer("302025315326"), warnings: []StudentIDWarning{StudentIDMajorMissing}},
		{name: "major mismatch", student: stringPointer("302025315326"), major: stringPointer("自动化"), warnings: []StudentIDWarning{StudentIDMajorMismatch}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			assessment := AssessStudentID(rule, &ApplicantFields{StudentID: test.student, Major: test.major})
			if assessment.Status != StudentIDAssessmentWarning || !equalStudentIDWarnings(assessment.Warnings, test.warnings) {
				t.Fatalf("assessment=%+v want warnings=%v", assessment, test.warnings)
			}
		})
	}
}

func TestAssessStudentIDReturnsUnconfiguredForDisabledOrIncompleteRule(t *testing.T) {
	rule := studentIDRuleFixture()
	rule.Enabled = false
	assessment := AssessStudentID(rule, nil)
	if assessment.Status != StudentIDAssessmentUnconfigured || assessment.RuleVersion != rule.Version || len(assessment.Warnings) != 0 {
		t.Fatalf("disabled assessment=%+v", assessment)
	}

	rule.Enabled = true
	rule.MajorCodeSegment = nil
	assessment = AssessStudentID(rule, nil)
	if assessment.Status != StudentIDAssessmentUnconfigured || len(assessment.Warnings) != 0 {
		t.Fatalf("incomplete assessment=%+v", assessment)
	}
}

func TestValidStudentIDRuleRejectsUnsafeConfiguration(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*StudentIDRule)
	}{
		{name: "overlapping segments", mutate: func(rule *StudentIDRule) { rule.MajorCodeSegment.Offset = 5 }},
		{name: "out of bounds", mutate: func(rule *StudentIDRule) { rule.MajorCodeSegment.Offset = 10 }},
		{name: "wrong year length", mutate: func(rule *StudentIDRule) { rule.EnrollmentYearSegment.Length = 3 }},
		{name: "duplicate mapping", mutate: func(rule *StudentIDRule) { rule.Mappings = append(rule.Mappings, rule.Mappings[0]) }},
		{name: "wrong code length", mutate: func(rule *StudentIDRule) { rule.Mappings[0].MajorCode = "31" }},
		{name: "non numeric code", mutate: func(rule *StudentIDRule) { rule.Mappings[0].MajorCode = "A15" }},
		{name: "too many aliases", mutate: func(rule *StudentIDRule) { rule.Mappings[0].Aliases = make([]string, 21) }},
		{name: "too many mappings", mutate: func(rule *StudentIDRule) { rule.Mappings = make([]StudentMajorMapping, 1001) }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			rule := studentIDRuleFixture()
			test.mutate(&rule)
			if validStudentIDRule(rule) {
				t.Fatalf("rule unexpectedly valid: %+v", rule)
			}
		})
	}
}

func TestStudentIDRuleAllowsDisabledDraftAndExactEnglishAlias(t *testing.T) {
	draft := StudentIDRule{Version: 1, UpdatedAt: time.Now().UTC()}
	if !validStudentIDRule(draft) {
		t.Fatalf("disabled draft invalid: %+v", draft)
	}

	rule := studentIDRuleFixture()
	rule.Mappings[0].Aliases = append(rule.Mappings[0].Aliases, "Computer Science")
	studentID, major := "302025315326", "computer science"
	assessment := AssessStudentID(rule, &ApplicantFields{StudentID: &studentID, Major: &major})
	if assessment.MajorMatches == nil || !*assessment.MajorMatches || assessment.Status != StudentIDAssessmentMatched {
		t.Fatalf("assessment=%+v", assessment)
	}
}

func studentIDRuleFixture() StudentIDRule {
	return StudentIDRule{
		Enabled:               true,
		EnrollmentYearSegment: &StudentIDSegment{Offset: 2, Length: 4},
		MajorCodeSegment:      &StudentIDSegment{Offset: 6, Length: 3},
		Mappings: []StudentMajorMapping{{
			EnrollmentYear: 2025, MajorCode: "315", MajorName: "计算机类", Aliases: []string{"计算机", "计科类"},
		}},
		Version: 3, UpdatedAt: time.Date(2026, 7, 29, 1, 0, 0, 0, time.UTC),
	}
}

func equalStudentIDWarnings(left, right []StudentIDWarning) bool {
	return strings.Join(studentIDWarningStrings(left), ",") == strings.Join(studentIDWarningStrings(right), ",")
}

func studentIDWarningStrings(values []StudentIDWarning) []string {
	result := make([]string, len(values))
	for index, value := range values {
		result[index] = string(value)
	}
	return result
}

func authField[T any](value T) auth.Field[T] { return auth.Field[T]{Set: true, Value: value} }

type studentIDRuleEventPublisherFake struct{ drafts []events.Draft }

func (p *studentIDRuleEventPublisherFake) Publish(draft events.Draft) (events.Event, error) {
	p.drafts = append(p.drafts, draft)
	return events.Event{}, nil
}
