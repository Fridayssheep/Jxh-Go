package config

import (
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

type Source string

const (
	SourceDefault     Source = "default"
	SourceFile        Source = "file"
	SourceEnvironment Source = "environment"
)

type SecretState struct {
	Configured bool   `json:"configured"`
	Source     Source `json:"source"`
}

type SecretOperation string

const (
	SecretReplace SecretOperation = "replace"
	SecretClear   SecretOperation = "clear"
)

type SecretUpdate struct {
	Operation SecretOperation `json:"operation"`
	Value     string          `json:"value,omitempty"`
}

type WPSSettings struct {
	ShareURL   SecretState `json:"share_url"`
	SID        SecretState `json:"sid"`
	Sheet      string      `json:"sheet"`
	TimeoutSec int         `json:"timeout_sec"`
}

type AISettings struct {
	Provider         string      `json:"provider"`
	BaseURL          string      `json:"base_url"`
	APIKey           SecretState `json:"api_key"`
	Model            string      `json:"model"`
	TimeoutSec       int         `json:"timeout_sec"`
	MaxQuestionChars int         `json:"max_question_chars"`
}

type QuoteSettings struct {
	BaseURL    string `json:"base_url"`
	TimeoutSec int    `json:"timeout_sec"`
}

type TimeSettings struct {
	AppTimezone       string `json:"app_timezone"`
	SchedulerTimezone string `json:"scheduler_timezone"`
}

type RetentionSettings struct {
	TriggerLogRetentionDays int `json:"trigger_log_retention_days"`
}

type Settings struct {
	WPS                  WPSSettings       `json:"wps"`
	AI                   AISettings        `json:"ai"`
	Quote                QuoteSettings     `json:"quote"`
	Time                 TimeSettings      `json:"time"`
	Retention            RetentionSettings `json:"retention"`
	EnvironmentOverrides []string          `json:"environment_overrides"`
	Version              uint64            `json:"version"`
}

type WPSSettingsPatch struct {
	ShareURL   *SecretUpdate `json:"share_url,omitempty"`
	SID        *SecretUpdate `json:"sid,omitempty"`
	Sheet      *string       `json:"sheet,omitempty"`
	TimeoutSec *int          `json:"timeout_sec,omitempty"`
}

type AISettingsPatch struct {
	Provider         *string       `json:"provider,omitempty"`
	BaseURL          *string       `json:"base_url,omitempty"`
	APIKey           *SecretUpdate `json:"api_key,omitempty"`
	Model            *string       `json:"model,omitempty"`
	TimeoutSec       *int          `json:"timeout_sec,omitempty"`
	MaxQuestionChars *int          `json:"max_question_chars,omitempty"`
}

type QuoteSettingsPatch struct {
	BaseURL    *string `json:"base_url,omitempty"`
	TimeoutSec *int    `json:"timeout_sec,omitempty"`
}

type TimeSettingsPatch struct {
	AppTimezone       *string `json:"app_timezone,omitempty"`
	SchedulerTimezone *string `json:"scheduler_timezone,omitempty"`
}

type RetentionSettingsPatch struct {
	TriggerLogRetentionDays *int `json:"trigger_log_retention_days,omitempty"`
}

type SettingsPatch struct {
	WPS       *WPSSettingsPatch       `json:"wps,omitempty"`
	AI        *AISettingsPatch        `json:"ai,omitempty"`
	Quote     *QuoteSettingsPatch     `json:"quote,omitempty"`
	Time      *TimeSettingsPatch      `json:"time,omitempty"`
	Retention *RetentionSettingsPatch `json:"retention,omitempty"`
}

var (
	ErrEmptyPatch             = errors.New("configuration patch is empty")
	ErrFieldManagedExternally = errors.New("configuration field is managed externally")
)

type FieldError struct {
	Path string `json:"path"`
	Code string `json:"code"`
}

type ValidationError struct {
	Fields []FieldError `json:"fields"`
}

func (e *ValidationError) Error() string {
	if e == nil || len(e.Fields) == 0 {
		return "invalid configuration"
	}
	return fmt.Sprintf("invalid configuration field %s", e.Fields[0].Path)
}

func (e *ValidationError) HasPath(path string) bool {
	if e == nil {
		return false
	}
	for _, field := range e.Fields {
		if field.Path == path {
			return true
		}
	}
	return false
}

type ManagedFieldsError struct {
	Fields []string `json:"fields"`
}

func (e *ManagedFieldsError) Error() string { return ErrFieldManagedExternally.Error() }
func (e *ManagedFieldsError) Unwrap() error { return ErrFieldManagedExternally }

func (patch SettingsPatch) Paths() []string {
	paths := make([]string, 0, 15)
	if value := patch.WPS; value != nil {
		if value.ShareURL != nil {
			paths = append(paths, "wps.share_url")
		}
		if value.SID != nil {
			paths = append(paths, "wps.sid")
		}
		if value.Sheet != nil {
			paths = append(paths, "wps.sheet")
		}
		if value.TimeoutSec != nil {
			paths = append(paths, "wps.timeout_sec")
		}
	}
	if value := patch.AI; value != nil {
		if value.Provider != nil {
			paths = append(paths, "ai.provider")
		}
		if value.BaseURL != nil {
			paths = append(paths, "ai.base_url")
		}
		if value.APIKey != nil {
			paths = append(paths, "ai.api_key")
		}
		if value.Model != nil {
			paths = append(paths, "ai.model")
		}
		if value.TimeoutSec != nil {
			paths = append(paths, "ai.timeout_sec")
		}
		if value.MaxQuestionChars != nil {
			paths = append(paths, "ai.max_question_chars")
		}
	}
	if value := patch.Quote; value != nil {
		if value.BaseURL != nil {
			paths = append(paths, "quote.base_url")
		}
		if value.TimeoutSec != nil {
			paths = append(paths, "quote.timeout_sec")
		}
	}
	if value := patch.Time; value != nil {
		if value.AppTimezone != nil {
			paths = append(paths, "app.timezone")
		}
		if value.SchedulerTimezone != nil {
			paths = append(paths, "scheduler.timezone")
		}
	}
	if value := patch.Retention; value != nil && value.TriggerLogRetentionDays != nil {
		paths = append(paths, "database.trigger_log_retention_days")
	}
	sort.Strings(paths)
	return paths
}

func validatePatch(patch SettingsPatch) error {
	if len(patch.Paths()) == 0 {
		return ErrEmptyPatch
	}
	fields := make([]FieldError, 0)
	add := func(path, code string) { fields = append(fields, FieldError{Path: path, Code: code}) }
	if value := patch.WPS; value != nil {
		validateSecretUpdate(&fields, "wps.share_url", value.ShareURL, 2048, true)
		validateSecretUpdate(&fields, "wps.sid", value.SID, 4096, false)
		if value.Sheet != nil {
			trimmed := strings.TrimSpace(*value.Sheet)
			if trimmed == "" || utf8.RuneCountInString(trimmed) > 128 {
				add("wps.sheet", "invalid_length")
			}
		}
		validateRange(&fields, "wps.timeout_sec", value.TimeoutSec, 1, 600)
	}
	if value := patch.AI; value != nil {
		if value.Provider != nil && *value.Provider != "openai" && *value.Provider != "ark" {
			add("ai.provider", "invalid_enum")
		}
		validateOptionalHTTPURL(&fields, "ai.base_url", value.BaseURL, false)
		validateSecretUpdate(&fields, "ai.api_key", value.APIKey, 8192, false)
		if value.Model != nil && utf8.RuneCountInString(*value.Model) > 255 {
			add("ai.model", "invalid_length")
		}
		validateRange(&fields, "ai.timeout_sec", value.TimeoutSec, 1, 600)
		validateRange(&fields, "ai.max_question_chars", value.MaxQuestionChars, 1, 10000)
	}
	if value := patch.Quote; value != nil {
		validateOptionalHTTPURL(&fields, "quote.base_url", value.BaseURL, false)
		validateRange(&fields, "quote.timeout_sec", value.TimeoutSec, 1, 120)
	}
	if value := patch.Time; value != nil {
		validateTimezone(&fields, "app.timezone", value.AppTimezone)
		validateTimezone(&fields, "scheduler.timezone", value.SchedulerTimezone)
	}
	if value := patch.Retention; value != nil {
		validateRange(&fields, "database.trigger_log_retention_days", value.TriggerLogRetentionDays, 0, 3650)
	}
	if len(fields) == 0 {
		return nil
	}
	sort.Slice(fields, func(i, j int) bool { return fields[i].Path < fields[j].Path })
	return &ValidationError{Fields: fields}
}

func validateSecretUpdate(fields *[]FieldError, path string, update *SecretUpdate, maximum int, validateURL bool) {
	if update == nil {
		return
	}
	switch update.Operation {
	case SecretClear:
		if update.Value != "" {
			*fields = append(*fields, FieldError{Path: path, Code: "value_not_allowed"})
		}
	case SecretReplace:
		if update.Value == "" || utf8.RuneCountInString(update.Value) > maximum {
			*fields = append(*fields, FieldError{Path: path, Code: "invalid_length"})
			return
		}
		if validateURL && !validHTTPURL(update.Value, true) {
			*fields = append(*fields, FieldError{Path: path, Code: "invalid_url"})
		}
	default:
		*fields = append(*fields, FieldError{Path: path, Code: "invalid_operation"})
	}
}

func validateOptionalHTTPURL(fields *[]FieldError, path string, value *string, allowUserinfo bool) {
	if value == nil {
		return
	}
	if utf8.RuneCountInString(*value) > 2048 || (*value != "" && !validHTTPURL(*value, allowUserinfo)) {
		*fields = append(*fields, FieldError{Path: path, Code: "invalid_url"})
	}
}

func validHTTPURL(value string, allowUserinfo bool) bool {
	parsed, err := url.Parse(value)
	if err != nil || !parsed.IsAbs() || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return false
	}
	return allowUserinfo || parsed.User == nil
}

func validateTimezone(fields *[]FieldError, path string, value *string) {
	if value == nil {
		return
	}
	if *value == "" || utf8.RuneCountInString(*value) > 64 {
		*fields = append(*fields, FieldError{Path: path, Code: "invalid_timezone"})
		return
	}
	if _, err := time.LoadLocation(*value); err != nil {
		*fields = append(*fields, FieldError{Path: path, Code: "invalid_timezone"})
	}
}

func validateRange(fields *[]FieldError, path string, value *int, minimum, maximum int) {
	if value != nil && (*value < minimum || *value > maximum) {
		*fields = append(*fields, FieldError{Path: path, Code: "out_of_range"})
	}
}
