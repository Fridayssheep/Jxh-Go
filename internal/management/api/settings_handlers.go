package adminapi

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/zjutjh/jxh-go/internal/management/audit"
	"github.com/zjutjh/jxh-go/internal/management/auth"
	"github.com/zjutjh/jxh-go/internal/management/settings"
)

type SettingsOperations interface {
	GetGlobal(context.Context, auth.Principal) (settings.Global, error)
	UpdateGlobal(context.Context, auth.Principal, uint64, settings.GlobalPatch, auth.MutationContext) (settings.Global, error)
	GetGroup(context.Context, auth.Principal, string) (settings.Group, error)
	UpdateGroup(context.Context, auth.Principal, string, uint64, settings.GroupPatch, auth.MutationContext) (settings.Group, error)
	DeleteGroup(context.Context, auth.Principal, string, uint64, auth.MutationContext) error
}

type SettingsHandlers struct {
	service SettingsOperations
}

func NewSettingsHandlers(service SettingsOperations) (*SettingsHandlers, error) {
	if service == nil {
		return nil, fmt.Errorf("settings service is required")
	}
	return &SettingsHandlers{service: service}, nil
}

func (h *SettingsHandlers) Register(router *Router) error {
	if router == nil {
		return fmt.Errorf("admin router is required")
	}
	routes := []struct {
		method  string
		pattern string
		options RouteOptions
		handler http.HandlerFunc
	}{
		{http.MethodGet, "/api/admin/v1/settings", RouteOptions{Permission: auth.PermissionSettingsRead}, h.getGlobal},
		{http.MethodPatch, "/api/admin/v1/settings", mutationRoute(auth.PermissionSettingsWrite), h.updateGlobal},
		{http.MethodGet, "/api/admin/v1/groups/{group_id}/settings", RouteOptions{Permission: auth.PermissionSettingsRead}, h.getGroup},
		{http.MethodPatch, "/api/admin/v1/groups/{group_id}/settings", mutationRoute(auth.PermissionSettingsWrite), h.updateGroup},
		{http.MethodDelete, "/api/admin/v1/groups/{group_id}/settings", mutationRoute(auth.PermissionSettingsWrite), h.deleteGroup},
	}
	for _, route := range routes {
		if err := router.HandleFunc(route.method, route.pattern, route.options, route.handler); err != nil {
			return err
		}
	}
	return nil
}

func (h *SettingsHandlers) getGlobal(w http.ResponseWriter, r *http.Request) {
	identity, _ := AuthFromContext(r.Context())
	value, err := h.service.GetGlobal(r.Context(), principalFromAuth(identity))
	if err != nil {
		h.writeServiceError(w, r, err)
		return
	}
	setRevisionETag(w, value.Version)
	writeJSON(w, http.StatusOK, mapGlobalSettings(value))
}

func (h *SettingsHandlers) updateGlobal(w http.ResponseWriter, r *http.Request) {
	revision, ok := requiredRevision(w, r)
	if !ok {
		return
	}
	patch, ok := decodeGlobalSettingsPatch(w, r)
	if !ok {
		return
	}
	identity, _ := AuthFromContext(r.Context())
	value, err := h.service.UpdateGlobal(r.Context(), principalFromAuth(identity), revision, patch, mutationContextFromRequest(r))
	if err != nil {
		h.writeServiceError(w, r, err)
		return
	}
	setRevisionETag(w, value.Version)
	writeJSON(w, http.StatusOK, mapGlobalSettings(value))
}

func (h *SettingsHandlers) getGroup(w http.ResponseWriter, r *http.Request) {
	groupID, ok := pathIdentifier(w, r, "group_id")
	if !ok {
		return
	}
	identity, _ := AuthFromContext(r.Context())
	value, err := h.service.GetGroup(r.Context(), principalFromAuth(identity), groupID)
	if err != nil {
		h.writeServiceError(w, r, err)
		return
	}
	setRevisionETag(w, value.Version)
	writeJSON(w, http.StatusOK, mapGroupSettings(value))
}

func (h *SettingsHandlers) updateGroup(w http.ResponseWriter, r *http.Request) {
	groupID, ok := pathIdentifier(w, r, "group_id")
	if !ok {
		return
	}
	revision, ok := requiredSettingsRevision(w, r)
	if !ok {
		return
	}
	patch, ok := decodeGroupSettingsPatch(w, r)
	if !ok {
		return
	}
	identity, _ := AuthFromContext(r.Context())
	value, err := h.service.UpdateGroup(r.Context(), principalFromAuth(identity), groupID, revision, patch, mutationContextFromRequest(r))
	if err != nil {
		h.writeServiceError(w, r, err)
		return
	}
	setRevisionETag(w, value.Version)
	writeJSON(w, http.StatusOK, mapGroupSettings(value))
}

func (h *SettingsHandlers) deleteGroup(w http.ResponseWriter, r *http.Request) {
	groupID, ok := pathIdentifier(w, r, "group_id")
	if !ok {
		return
	}
	revision, ok := requiredRevision(w, r)
	if !ok {
		return
	}
	identity, _ := AuthFromContext(r.Context())
	if err := h.service.DeleteGroup(r.Context(), principalFromAuth(identity), groupID, revision, mutationContextFromRequest(r)); err != nil {
		h.writeServiceError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *SettingsHandlers) writeServiceError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, settings.ErrForbidden):
		writeAPIError(w, r, http.StatusForbidden, CodeForbidden, "settings operation is forbidden", nil, false)
	case errors.Is(err, settings.ErrInvalidInput):
		writeAPIError(w, r, http.StatusBadRequest, CodeBadRequest, "settings input is invalid", nil, false)
	case errors.Is(err, settings.ErrNotFound):
		writeAPIError(w, r, http.StatusNotFound, CodeNotFound, "settings group does not exist", nil, false)
	case errors.Is(err, settings.ErrConflict):
		writeAPIError(w, r, http.StatusConflict, "resource_version_conflict", "settings were changed by another operation", nil, false)
	default:
		writeAPIError(w, r, http.StatusInternalServerError, CodeInternal, "internal server error", nil, false)
	}
}

type rawSettingsField struct {
	Set  bool
	Null bool
	data []byte
}

func (f *rawSettingsField) UnmarshalJSON(data []byte) error {
	f.Set = true
	f.Null = bytes.Equal(bytes.TrimSpace(data), []byte("null"))
	f.data = append(f.data[:0], data...)
	return nil
}

type settingsPatchEnvelope struct {
	Features     rawSettingsField `json:"features"`
	JoinRequests rawSettingsField `json:"join_requests"`
}

type featureSettingsPatchFields struct {
	KeywordReply  rawSettingsField `json:"keyword_reply"`
	AIQA          rawSettingsField `json:"ai_qa"`
	Quote         rawSettingsField `json:"quote"`
	LinkCleaner   rawSettingsField `json:"link_cleaner"`
	Welcome       rawSettingsField `json:"welcome"`
	CustomCommand rawSettingsField `json:"custom_commands"`
}

type basicSettingsPatchFields struct {
	Enabled rawSettingsField `json:"enabled"`
}

type welcomeSettingsPatchFields struct {
	Enabled         rawSettingsField `json:"enabled"`
	MessageTemplate rawSettingsField `json:"message_template"`
}

type joinRequestSettingsPatchFields struct {
	AutoRejectReason rawSettingsField `json:"auto_reject_reason"`
}

func decodeGlobalSettingsPatch(w http.ResponseWriter, r *http.Request) (settings.GlobalPatch, bool) {
	var envelope settingsPatchEnvelope
	if !decodeRequestJSON(w, r, &envelope) {
		return settings.GlobalPatch{}, false
	}
	if (!envelope.Features.Set && !envelope.JoinRequests.Set) || envelope.Features.Null || envelope.JoinRequests.Null {
		writeInvalidSettingsPayload(w, r)
		return settings.GlobalPatch{}, false
	}
	patch := settings.GlobalPatch{}
	valid := true
	if envelope.Features.Set {
		var fields featureSettingsPatchFields
		if err := decodeStrictJSONBytes(envelope.Features.data, &fields); err != nil {
			writeInvalidSettingsPayload(w, r)
			return settings.GlobalPatch{}, false
		}
		patch.KeywordReply, valid = decodeGlobalBasicField(fields.KeywordReply)
		if valid {
			patch.AIQA, valid = decodeGlobalBasicField(fields.AIQA)
		}
		if valid {
			patch.Quote, valid = decodeGlobalBasicField(fields.Quote)
		}
		if valid {
			patch.LinkCleaner, valid = decodeGlobalBasicField(fields.LinkCleaner)
		}
		if valid {
			patch.CustomCommand, valid = decodeGlobalBasicField(fields.CustomCommand)
		}
		if valid {
			patch.Welcome, valid = decodeGlobalWelcomeField(fields.Welcome)
		}
	}
	if valid {
		patch.JoinRequests, valid = decodeGlobalJoinRequestsField(envelope.JoinRequests)
	}
	if !valid || !globalPatchSet(patch) {
		writeInvalidSettingsPayload(w, r)
		return settings.GlobalPatch{}, false
	}
	return patch, true
}

func decodeGlobalJoinRequestsField(field rawSettingsField) (auth.Field[settings.JoinRequestSettingsPatch], bool) {
	if !field.Set {
		return auth.Field[settings.JoinRequestSettingsPatch]{}, true
	}
	var body joinRequestSettingsPatchFields
	if err := decodeStrictJSONBytes(field.data, &body); err != nil || !body.AutoRejectReason.Set || body.AutoRejectReason.Null {
		return auth.Field[settings.JoinRequestSettingsPatch]{}, false
	}
	var reason string
	if err := decodeStrictJSONBytes(body.AutoRejectReason.data, &reason); err != nil {
		return auth.Field[settings.JoinRequestSettingsPatch]{}, false
	}
	return auth.Field[settings.JoinRequestSettingsPatch]{
		Set: true,
		Value: settings.JoinRequestSettingsPatch{
			AutoRejectReason: auth.Field[string]{Set: true, Value: reason},
		},
	}, true
}

func decodeGlobalBasicField(field rawSettingsField) (auth.Field[settings.BasicPatch], bool) {
	if !field.Set {
		return auth.Field[settings.BasicPatch]{}, true
	}
	if field.Null {
		return auth.Field[settings.BasicPatch]{}, false
	}
	var body basicSettingsPatchFields
	if err := decodeStrictJSONBytes(field.data, &body); err != nil || !body.Enabled.Set || body.Enabled.Null {
		return auth.Field[settings.BasicPatch]{}, false
	}
	var enabled bool
	if err := decodeStrictJSONBytes(body.Enabled.data, &enabled); err != nil {
		return auth.Field[settings.BasicPatch]{}, false
	}
	return auth.Field[settings.BasicPatch]{Set: true, Value: settings.BasicPatch{Enabled: auth.Field[bool]{Set: true, Value: enabled}}}, true
}

func decodeGlobalWelcomeField(field rawSettingsField) (auth.Field[settings.WelcomePatch], bool) {
	if !field.Set {
		return auth.Field[settings.WelcomePatch]{}, true
	}
	if field.Null {
		return auth.Field[settings.WelcomePatch]{}, false
	}
	var body welcomeSettingsPatchFields
	if err := decodeStrictJSONBytes(field.data, &body); err != nil || (!body.Enabled.Set && !body.MessageTemplate.Set) {
		return auth.Field[settings.WelcomePatch]{}, false
	}
	patch := settings.WelcomePatch{}
	if body.Enabled.Set {
		if body.Enabled.Null || decodeStrictJSONBytes(body.Enabled.data, &patch.Enabled.Value) != nil {
			return auth.Field[settings.WelcomePatch]{}, false
		}
		patch.Enabled.Set = true
	}
	if body.MessageTemplate.Set {
		if body.MessageTemplate.Null || decodeStrictJSONBytes(body.MessageTemplate.data, &patch.MessageTemplate.Value) != nil {
			return auth.Field[settings.WelcomePatch]{}, false
		}
		patch.MessageTemplate.Set = true
	}
	return auth.Field[settings.WelcomePatch]{Set: true, Value: patch}, true
}

func decodeGroupSettingsPatch(w http.ResponseWriter, r *http.Request) (settings.GroupPatch, bool) {
	var envelope settingsPatchEnvelope
	if !decodeRequestJSON(w, r, &envelope) {
		return settings.GroupPatch{}, false
	}
	if !envelope.Features.Set || envelope.Features.Null {
		writeInvalidSettingsPayload(w, r)
		return settings.GroupPatch{}, false
	}
	var fields featureSettingsPatchFields
	if err := decodeStrictJSONBytes(envelope.Features.data, &fields); err != nil {
		writeInvalidSettingsPayload(w, r)
		return settings.GroupPatch{}, false
	}
	patch := settings.GroupPatch{}
	var valid bool
	patch.KeywordReply, valid = decodeGroupFeatureField(fields.KeywordReply, false)
	if !valid {
		writeInvalidSettingsPayload(w, r)
		return settings.GroupPatch{}, false
	}
	patch.AIQA, valid = decodeGroupFeatureField(fields.AIQA, false)
	if !valid {
		writeInvalidSettingsPayload(w, r)
		return settings.GroupPatch{}, false
	}
	patch.Quote, valid = decodeGroupFeatureField(fields.Quote, false)
	if !valid {
		writeInvalidSettingsPayload(w, r)
		return settings.GroupPatch{}, false
	}
	patch.LinkCleaner, valid = decodeGroupFeatureField(fields.LinkCleaner, false)
	if !valid {
		writeInvalidSettingsPayload(w, r)
		return settings.GroupPatch{}, false
	}
	patch.Welcome, valid = decodeGroupFeatureField(fields.Welcome, true)
	if !valid {
		writeInvalidSettingsPayload(w, r)
		return settings.GroupPatch{}, false
	}
	patch.CustomCommand, valid = decodeGroupFeatureField(fields.CustomCommand, false)
	if !valid || !groupPatchSet(patch) {
		writeInvalidSettingsPayload(w, r)
		return settings.GroupPatch{}, false
	}
	return patch, true
}

func decodeGroupFeatureField(field rawSettingsField, welcome bool) (settings.OverrideFeaturePatch, bool) {
	if !field.Set {
		return settings.OverrideFeaturePatch{}, true
	}
	if field.Null {
		return settings.OverrideFeaturePatch{Set: true, Clear: true}, true
	}
	if !welcome {
		var body basicSettingsPatchFields
		if err := decodeStrictJSONBytes(field.data, &body); err != nil || !body.Enabled.Set {
			return settings.OverrideFeaturePatch{}, false
		}
		value, ok := decodeNullableBool(body.Enabled)
		return settings.OverrideFeaturePatch{Set: true, Enabled: auth.Field[*bool]{Set: true, Value: value}}, ok
	}
	var body welcomeSettingsPatchFields
	if err := decodeStrictJSONBytes(field.data, &body); err != nil || (!body.Enabled.Set && !body.MessageTemplate.Set) {
		return settings.OverrideFeaturePatch{}, false
	}
	patch := settings.OverrideFeaturePatch{Set: true}
	if body.Enabled.Set {
		value, ok := decodeNullableBool(body.Enabled)
		if !ok {
			return settings.OverrideFeaturePatch{}, false
		}
		patch.Enabled = auth.Field[*bool]{Set: true, Value: value}
	}
	if body.MessageTemplate.Set {
		value, ok := decodeNullableString(body.MessageTemplate)
		if !ok {
			return settings.OverrideFeaturePatch{}, false
		}
		patch.MessageTemplate = auth.Field[*string]{Set: true, Value: value}
	}
	return patch, true
}

func decodeNullableBool(field rawSettingsField) (*bool, bool) {
	if field.Null {
		return nil, true
	}
	var value bool
	if err := decodeStrictJSONBytes(field.data, &value); err != nil {
		return nil, false
	}
	return &value, true
}

func decodeNullableString(field rawSettingsField) (*string, bool) {
	if field.Null {
		return nil, true
	}
	var value string
	if err := decodeStrictJSONBytes(field.data, &value); err != nil {
		return nil, false
	}
	return &value, true
}

func globalPatchSet(value settings.GlobalPatch) bool {
	return value.KeywordReply.Set || value.AIQA.Set || value.Quote.Set || value.LinkCleaner.Set || value.Welcome.Set ||
		value.CustomCommand.Set || value.JoinRequests.Set
}

func groupPatchSet(value settings.GroupPatch) bool {
	return value.KeywordReply.Set || value.AIQA.Set || value.Quote.Set || value.LinkCleaner.Set || value.Welcome.Set || value.CustomCommand.Set
}

func writeInvalidSettingsPayload(w http.ResponseWriter, r *http.Request) {
	writeAPIError(w, r, http.StatusBadRequest, CodeBadRequest, "settings payload is invalid", nil, false)
}

func requiredSettingsRevision(w http.ResponseWriter, r *http.Request) (uint64, bool) {
	value := r.Header.Get("If-Match")
	if value == "" {
		writeAPIError(w, r, http.StatusPreconditionRequired, CodePreconditionRequired, "If-Match 请求头必填", nil, false)
		return 0, false
	}
	revision, err := ParseIfMatchIncludingZero(value)
	if err != nil {
		writeAPIError(w, r, http.StatusBadRequest, CodeBadRequest, "If-Match 版本无效", nil, false)
		return 0, false
	}
	return revision, true
}

type basicFeatureSettingsDTO struct {
	Enabled bool `json:"enabled"`
}

type welcomeFeatureSettingsDTO struct {
	Enabled         bool   `json:"enabled"`
	MessageTemplate string `json:"message_template"`
}

type featureSettingsDTO struct {
	KeywordReply  basicFeatureSettingsDTO   `json:"keyword_reply"`
	AIQA          basicFeatureSettingsDTO   `json:"ai_qa"`
	Quote         basicFeatureSettingsDTO   `json:"quote"`
	LinkCleaner   basicFeatureSettingsDTO   `json:"link_cleaner"`
	Welcome       welcomeFeatureSettingsDTO `json:"welcome"`
	CustomCommand basicFeatureSettingsDTO   `json:"custom_commands"`
}

type joinRequestSettingsDTO struct {
	AutoRejectReason string `json:"auto_reject_reason"`
}

type welcomeFeatureOverrideDTO struct {
	Enabled         *bool   `json:"enabled,omitempty"`
	MessageTemplate *string `json:"message_template,omitempty"`
}

type groupFeatureOverridesDTO struct {
	KeywordReply  *basicFeatureSettingsDTO   `json:"keyword_reply,omitempty"`
	AIQA          *basicFeatureSettingsDTO   `json:"ai_qa,omitempty"`
	Quote         *basicFeatureSettingsDTO   `json:"quote,omitempty"`
	LinkCleaner   *basicFeatureSettingsDTO   `json:"link_cleaner,omitempty"`
	Welcome       *welcomeFeatureOverrideDTO `json:"welcome,omitempty"`
	CustomCommand *basicFeatureSettingsDTO   `json:"custom_commands,omitempty"`
}

type globalSettingsDTO struct {
	Features     featureSettingsDTO     `json:"features"`
	JoinRequests joinRequestSettingsDTO `json:"join_requests"`
	Version      uint64                 `json:"version"`
	UpdatedAt    time.Time              `json:"updated_at"`
	UpdatedBy    *auditActorDTO         `json:"updated_by"`
}

type groupSettingsDTO struct {
	GroupID       string                   `json:"group_id"`
	Effective     featureSettingsDTO       `json:"effective"`
	Overrides     groupFeatureOverridesDTO `json:"overrides"`
	GlobalVersion uint64                   `json:"global_version"`
	Version       uint64                   `json:"version"`
	UpdatedAt     *time.Time               `json:"updated_at"`
	UpdatedBy     *auditActorDTO           `json:"updated_by"`
}

func mapGlobalSettings(value settings.Global) globalSettingsDTO {
	return globalSettingsDTO{
		Features:     mapFeatureSettings(value.Features),
		JoinRequests: joinRequestSettingsDTO{AutoRejectReason: value.JoinRequests.AutoRejectReason}, Version: value.Version,
		UpdatedAt: value.UpdatedAt.UTC(), UpdatedBy: mapOptionalAuditActor(value.UpdatedBy),
	}
}

func mapGroupSettings(value settings.Group) groupSettingsDTO {
	return groupSettingsDTO{
		GroupID: value.GroupID, Effective: mapFeatureSettings(value.Effective), Overrides: mapFeatureOverrides(value.Overrides),
		GlobalVersion: value.GlobalVersion, Version: value.Version, UpdatedAt: utcTimePointer(value.UpdatedAt),
		UpdatedBy: mapOptionalAuditActor(value.UpdatedBy),
	}
}

func mapFeatureSettings(value settings.Features) featureSettingsDTO {
	return featureSettingsDTO{
		KeywordReply: basicFeatureSettingsDTO{Enabled: value.KeywordReply.Enabled},
		AIQA:         basicFeatureSettingsDTO{Enabled: value.AIQA.Enabled}, Quote: basicFeatureSettingsDTO{Enabled: value.Quote.Enabled},
		LinkCleaner:   basicFeatureSettingsDTO{Enabled: value.LinkCleaner.Enabled},
		Welcome:       welcomeFeatureSettingsDTO{Enabled: value.Welcome.Enabled, MessageTemplate: value.Welcome.MessageTemplate},
		CustomCommand: basicFeatureSettingsDTO{Enabled: value.CustomCommand.Enabled},
	}
}

func mapFeatureOverrides(value settings.Overrides) groupFeatureOverridesDTO {
	result := groupFeatureOverridesDTO{}
	if value.KeywordReply != nil {
		result.KeywordReply = &basicFeatureSettingsDTO{Enabled: value.KeywordReply.Enabled}
	}
	if value.AIQA != nil {
		result.AIQA = &basicFeatureSettingsDTO{Enabled: value.AIQA.Enabled}
	}
	if value.Quote != nil {
		result.Quote = &basicFeatureSettingsDTO{Enabled: value.Quote.Enabled}
	}
	if value.LinkCleaner != nil {
		result.LinkCleaner = &basicFeatureSettingsDTO{Enabled: value.LinkCleaner.Enabled}
	}
	if value.Welcome != nil {
		result.Welcome = &welcomeFeatureOverrideDTO{Enabled: value.Welcome.Enabled, MessageTemplate: value.Welcome.MessageTemplate}
	}
	if value.CustomCommand != nil {
		result.CustomCommand = &basicFeatureSettingsDTO{Enabled: value.CustomCommand.Enabled}
	}
	return result
}

func mapOptionalAuditActor(value *audit.Actor) *auditActorDTO {
	if value == nil {
		return nil
	}
	mapped := mapAuditActor(*value)
	return &mapped
}
