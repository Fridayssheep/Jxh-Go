package joinrequests

import (
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/zjutjh/jxh-go/internal/management/audit"
	"github.com/zjutjh/jxh-go/internal/management/auth"
)

var (
	studentIDPattern      = regexp.MustCompile(`^[0-9]{6,32}$`)
	idempotencyKeyPattern = regexp.MustCompile(`^[A-Za-z0-9._:-]{8,128}$`)
	errorCodePattern      = regexp.MustCompile(`^[a-z][a-z0-9_]{0,99}$`)
)

func ValidateApplicantFields(fields ApplicantFields, verificationMessage string) ApplicantFields {
	result := cloneApplicantFields(fields)
	result.ValidationErrors = make([]string, 0, 3)
	studentID := optionalValue(result.StudentID)
	name := optionalValue(result.Name)
	major := optionalValue(result.Major)
	if !studentIDPattern.MatchString(studentID) {
		result.ValidationErrors = append(result.ValidationErrors, "student_id_invalid")
	} else if !strings.Contains(verificationMessage, studentID) {
		result.ValidationErrors = append(result.ValidationErrors, "student_id_not_in_verification_message")
	}
	if !validApplicantName(name) {
		result.ValidationErrors = append(result.ValidationErrors, "name_invalid")
	} else if !strings.Contains(verificationMessage, name) {
		result.ValidationErrors = append(result.ValidationErrors, "name_not_in_verification_message")
	}
	if !validApplicantMajor(major) {
		result.ValidationErrors = append(result.ValidationErrors, "major_invalid")
	} else if !strings.Contains(verificationMessage, major) {
		result.ValidationErrors = append(result.ValidationErrors, "major_not_in_verification_message")
	}
	result.Valid = len(result.ValidationErrors) == 0
	return result
}

func validApplicantName(value string) bool {
	if !utf8.ValidString(value) || utf8.RuneCountInString(value) < 1 || utf8.RuneCountInString(value) > 16 {
		return false
	}
	hasLetter := false
	for _, character := range value {
		if unicode.IsNumber(character) {
			return false
		}
		hasLetter = hasLetter || unicode.IsLetter(character)
	}
	return hasLetter
}

func validApplicantMajor(value string) bool {
	if !utf8.ValidString(value) || utf8.RuneCountInString(value) < 1 || utf8.RuneCountInString(value) > 128 {
		return false
	}
	return strings.IndexFunc(value, func(character rune) bool {
		return unicode.IsLetter(character) || unicode.IsNumber(character)
	}) >= 0
}

func validPolicy(value Policy) bool {
	return validGroupID(value.GroupID) && value.Mode == PolicyModeAIFieldsComplete &&
		slices.Equal(value.RequiredFields, requiredPolicyFields[:]) && !value.AutoReject && value.Version > 0 &&
		validUTCTime(value.UpdatedAt) && validOptionalActor(value.UpdatedBy)
}

func validRequest(value Request, detail bool) bool {
	if !validRequestID(value.ID) || !validGroupID(value.Group.ID) || !validText(value.Group.Name, 200, true) ||
		!validNumericID(value.ApplicantQQ, 32) || !validOptionalString(value.ApplicantNickname, 100) ||
		!validText(value.VerificationMessage, 2000, true) || !validSubType(value.SubType) || !validRequestSource(value.Source) ||
		!validObservedStatus(value.ObservedStatus) || !validDecisionStatus(value.DecisionStatus) ||
		!validOptionalDecisionSource(value.DecisionSource) || !validAIParse(value.AIParse) || !validUTCTime(value.RequestedAt) ||
		value.Version == 0 || !validOptionalID(value.LastDecisionID, 256) {
		return false
	}
	if !detail {
		return true
	}
	return validOptionalString(value.Comment, 500) && validUTCTime(value.FirstObservedAt) && validUTCTime(value.LastObservedAt) &&
		!value.LastObservedAt.Before(value.FirstObservedAt)
}

func validAIParse(value AIParseResult) bool {
	if !validAIParseStatus(value.Status) || !validOptionalErrorCode(value.ErrorCode) || !validOptionalUTCTime(value.CompletedAt) {
		return false
	}
	if value.Fields != nil {
		if !validOptionalString(value.Fields.StudentID, 64) || !validOptionalString(value.Fields.Name, 100) ||
			!validOptionalString(value.Fields.Major, 100) {
			return false
		}
		for _, item := range value.Fields.ValidationErrors {
			if !validText(item, 200, false) {
				return false
			}
		}
	}
	return value.Status != AIParseSucceeded || value.Fields != nil
}

func validDecision(value Decision) bool {
	if !validIdentifier(value.ID, 256) || !validRequestID(value.RequestID) || !validAction(value.Action) ||
		!validDecisionSource(value.Source) || !validAttemptStatus(value.Status) || !validOptionalActor(value.Actor) ||
		!validOptionalString(value.Reason, 500) || !validOptionalPositive(value.RuleVersion) ||
		!validUTCTime(value.StartedAt) || !validOptionalUTCTime(value.CompletedAt) || !validOptionalErrorCode(value.ErrorCode) ||
		!validIdentifier(value.TraceID, 256) {
		return false
	}
	return value.FieldSnapshot == nil || validAIParse(AIParseResult{Status: AIParseSucceeded, Fields: value.FieldSnapshot})
}

func validListQuery(value ListQuery) bool {
	if value.GroupID != "" && !validGroupID(value.GroupID) {
		return false
	}
	seenStatuses := make(map[DecisionStatus]struct{}, len(value.DecisionStatuses))
	for _, status := range value.DecisionStatuses {
		if !validDecisionStatus(status) {
			return false
		}
		if _, duplicate := seenStatuses[status]; duplicate {
			return false
		}
		seenStatuses[status] = struct{}{}
	}
	if value.ObservedStatus != "" && !validObservedStatus(value.ObservedStatus) ||
		value.AIParseStatus != "" && !validAIParseStatus(value.AIParseStatus) ||
		value.SubType != "" && !validSubType(value.SubType) || value.Source != "" && !validRequestSource(value.Source) ||
		value.DecisionSource != "" && !validDecisionSource(value.DecisionSource) ||
		value.Sort != SortRequestedDesc && value.Sort != SortRequestedAsc || value.Limit < 1 || value.Limit > 100 ||
		!validText(value.Query, 100, true) || !validIdentifier(value.Cursor, 256, true) ||
		!validOptionalUTCTime(value.RequestedFrom) || !validOptionalUTCTime(value.RequestedTo) {
		return false
	}
	return value.RequestedFrom == nil || value.RequestedTo == nil || !value.RequestedFrom.After(*value.RequestedTo)
}

func validDecisionListQuery(value DecisionListQuery) bool {
	return validRequestID(value.RequestID) && validIdentifier(value.Cursor, 256, true) && value.Limit >= 1 && value.Limit <= 100
}

func validDecisionInput(value DecisionInput) bool {
	return validAction(value.Action) && validText(value.Reason, 500, true)
}

func validBulkInput(value BulkInput) bool {
	if !validGroupID(value.GroupID) || !validAction(value.Action) || !validText(value.Reason, 500, true) || len(value.Items) < 1 || len(value.Items) > 20 {
		return false
	}
	seen := make(map[string]struct{}, len(value.Items))
	for _, item := range value.Items {
		if !validRequestID(item.ID) || item.Version == 0 {
			return false
		}
		if _, duplicate := seen[item.ID]; duplicate {
			return false
		}
		seen[item.ID] = struct{}{}
	}
	return true
}

func validExternalResult(value ExternalResult) bool {
	if value.Outcome != ExternalConfirmed && value.Outcome != ExternalFailed && value.Outcome != ExternalUnknown && value.Outcome != ExternalUnavailable {
		return false
	}
	if value.Outcome == ExternalConfirmed {
		return value.ErrorCode == ""
	}
	return errorCodePattern.MatchString(value.ErrorCode)
}

func validMutationRequest(value auth.MutationContext) bool {
	return validText(value.RequestID, 256, false) && validText(value.IPAddress, 64, true) && validText(value.UserAgent, 300, true)
}

func validAction(value Action) bool {
	return value == ActionApprove || value == ActionReject
}

func validObservedStatus(value ObservedStatus) bool {
	return value == ObservedPending || value == ObservedChecked
}

func validDecisionStatus(value DecisionStatus) bool {
	switch value {
	case DecisionPending, DecisionProcessing, DecisionApproved, DecisionRejected, DecisionExternalProcessed, DecisionUnknown:
		return true
	default:
		return false
	}
}

func validDecisionSource(value DecisionSource) bool {
	return value == SourceManual || value == SourceAutomatic || value == SourceExternal
}

func validOptionalDecisionSource(value *DecisionSource) bool {
	return value == nil || validDecisionSource(*value)
}

func validAttemptStatus(value AttemptStatus) bool {
	return value == AttemptStarted || value == AttemptConfirmed || value == AttemptFailed || value == AttemptUnknown
}

func validAIParseStatus(value AIParseStatus) bool {
	switch value {
	case AIParsePending, AIParseRunning, AIParseSucceeded, AIParseFailed, AIParseSkipped:
		return true
	default:
		return false
	}
}

func validSubType(value SubType) bool {
	return value == SubTypeAdd || value == SubTypeInvite
}

func validRequestSource(value RequestSource) bool {
	return value == RequestSourceEvent || value == RequestSourceSystem
}

func validGroupID(value string) bool {
	return validNumericID(value, 32) && strings.TrimLeft(value, "0") != ""
}

func validNumericID(value string, maximum int) bool {
	if value == "" || len(value) > maximum {
		return false
	}
	for _, character := range value {
		if character < '0' || character > '9' {
			return false
		}
	}
	return true
}

func validRequestID(value string) bool {
	return validIdentifier(value, 512)
}

func validOptionalID(value *string, maximum int) bool {
	return value == nil || validIdentifier(*value, maximum)
}

func validIdentifier(value string, maximum int, optional ...bool) bool {
	if value == "" {
		return len(optional) > 0 && optional[0]
	}
	if !utf8.ValidString(value) || utf8.RuneCountInString(value) > maximum {
		return false
	}
	for _, character := range value {
		if unicode.IsControl(character) {
			return false
		}
	}
	return true
}

func validText(value string, maximum int, optional bool) bool {
	if value == "" {
		return optional
	}
	return utf8.ValidString(value) && utf8.RuneCountInString(value) <= maximum
}

func validOptionalString(value *string, maximum int) bool {
	return value == nil || validText(*value, maximum, true)
}

func validOptionalErrorCode(value *string) bool {
	return value == nil || errorCodePattern.MatchString(*value)
}

func validUTCTime(value time.Time) bool {
	return !value.IsZero() && value.Location() == time.UTC
}

func validOptionalUTCTime(value *time.Time) bool {
	return value == nil || validUTCTime(*value)
}

func validOptionalPositive(value *uint64) bool {
	return value == nil || *value > 0
}

func validOptionalActor(value *audit.Actor) bool {
	if value == nil {
		return true
	}
	if value.Type != audit.ActorAdminUser && value.Type != audit.ActorQQUser && value.Type != audit.ActorSystem {
		return false
	}
	return validText(value.DisplayName, 100, true) && validOptionalString(value.UserID, 256) && validOptionalString(value.QQUserID, 32)
}

func optionalValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}
