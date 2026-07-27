package audit

import (
	"encoding/json"
	"strings"
	"unicode/utf8"

	"github.com/zjutjh/jxh-go/internal/auth"
)

const (
	RedactedValue           = "[redacted]"
	maxAuditValueString     = 2000
	maxAuditObjectBytes     = 64 << 10
	maxAuditDepth           = 32
	maxAuditCollectionItems = 512
)

func RedactForRole(log Log, role auth.Role) Log {
	result := log
	changed := false
	result.ID, changed = boundedString(log.ID, 256, changed)
	result.Actor.DisplayName, changed = boundedString(log.Actor.DisplayName, 100, changed)
	result.Actor.UserID, changed = cloneOptionalBounded(log.Actor.UserID, 256, changed)
	result.Actor.QQUserID, changed = cloneOptionalBounded(log.Actor.QQUserID, 256, changed)
	result.Target.Type, changed = boundedString(log.Target.Type, 64, changed)
	result.Target.ID, changed = boundedString(log.Target.ID, 256, changed)
	result.Target.DisplayName, changed = boundedString(log.Target.DisplayName, 200, changed)
	result.Action, changed = boundedString(log.Action, 100, changed)
	result.ErrorCode, changed = cloneOptionalBounded(log.ErrorCode, 100, changed)
	result.RequestID, changed = boundedString(log.RequestID, 256, changed)
	result.IPAddress, changed = cloneOptionalBounded(log.IPAddress, 64, changed)
	result.UserAgent, changed = cloneOptionalBounded(log.UserAgent, 300, changed)
	result.Redacted = log.Redacted || changed

	force := role == auth.RoleObserver && observerSensitiveTarget(log.Target.Type)
	result.Before, changed = redactObject(log.Before, force)
	result.Redacted = result.Redacted || changed
	result.After, changed = redactObject(log.After, force)
	result.Redacted = result.Redacted || changed
	result.Metadata, changed = redactObject(log.Metadata, force)
	result.Redacted = result.Redacted || changed
	return result
}

func redactObject(input map[string]any, force bool) (map[string]any, bool) {
	return redactObjectAtDepth(input, force, 0)
}

func redactObjectAtDepth(input map[string]any, force bool, depth int) (map[string]any, bool) {
	if input == nil {
		return nil, false
	}
	capacity := len(input)
	if capacity > maxAuditCollectionItems {
		capacity = maxAuditCollectionItems
	}
	result := make(map[string]any, capacity)
	changed := force
	encodedBytes := 2
	processed := 0
	for key, value := range input {
		if processed >= maxAuditCollectionItems {
			changed = true
			break
		}
		processed++
		safeKey := truncateRunes(key, 128)
		if safeKey != key {
			changed = true
		}
		if _, exists := result[safeKey]; exists {
			changed = true
			continue
		}

		var redacted any
		var itemChanged bool
		if sensitiveKey(key) {
			redacted = RedactedValue
			itemChanged = true
		} else {
			redacted, itemChanged = redactValueAtDepth(value, force, depth+1)
		}
		changed = changed || itemChanged
		entryBytes, ok := auditObjectEntryBytes(safeKey, redacted, len(result) > 0)
		if !ok || encodedBytes+entryBytes > maxAuditObjectBytes {
			changed = true
			redacted = RedactedValue
			entryBytes, ok = auditObjectEntryBytes(safeKey, redacted, len(result) > 0)
			if !ok || encodedBytes+entryBytes > maxAuditObjectBytes {
				continue
			}
		}
		result[safeKey] = redacted
		encodedBytes += entryBytes
	}
	return result, changed
}

func redactValue(value any, force bool) (any, bool) {
	return redactValueAtDepth(value, force, 0)
}

func redactValueAtDepth(value any, force bool, depth int) (any, bool) {
	if depth >= maxAuditDepth {
		return RedactedValue, true
	}
	if typed, ok := value.(map[string]any); ok {
		return redactObjectAtDepth(typed, force, depth)
	}
	if typed, ok := value.([]any); ok {
		return redactSliceAtDepth(typed, force, depth)
	}
	if force {
		return RedactedValue, true
	}
	switch typed := value.(type) {
	case nil, bool, json.Number, float64, float32, int, int8, int16, int32, int64,
		uint, uint8, uint16, uint32, uint64:
		return typed, false
	case string:
		if !utf8.ValidString(typed) || sensitiveValue(typed) {
			return RedactedValue, true
		}
		truncated := truncateRunes(typed, maxAuditValueString)
		return truncated, truncated != typed
	default:
		encoded, err := json.Marshal(typed)
		if err != nil || len(encoded) > maxAuditObjectBytes {
			return RedactedValue, true
		}
		var normalized any
		decoder := json.NewDecoder(strings.NewReader(string(encoded)))
		decoder.UseNumber()
		if err := decoder.Decode(&normalized); err != nil {
			return RedactedValue, true
		}
		redacted, changed := redactValueAtDepth(normalized, false, depth+1)
		return redacted, changed
	}
}

func redactSliceAtDepth(input []any, force bool, depth int) ([]any, bool) {
	capacity := len(input)
	if capacity > maxAuditCollectionItems {
		capacity = maxAuditCollectionItems
	}
	result := make([]any, 0, capacity)
	changed := force
	encodedBytes := 2
	for index, value := range input {
		if index >= maxAuditCollectionItems {
			changed = true
			break
		}
		redacted, itemChanged := redactValueAtDepth(value, force, depth+1)
		changed = changed || itemChanged
		itemBytes, ok := auditEncodedBytes(redacted)
		if !ok || encodedBytes+itemBytes+boolInt(len(result) > 0) > maxAuditObjectBytes {
			changed = true
			redacted = RedactedValue
			itemBytes, ok = auditEncodedBytes(redacted)
			if !ok || encodedBytes+itemBytes+boolInt(len(result) > 0) > maxAuditObjectBytes {
				break
			}
		}
		if len(result) > 0 {
			encodedBytes++
		}
		result = append(result, redacted)
		encodedBytes += itemBytes
	}
	return result, changed
}

func auditObjectEntryBytes(key string, value any, comma bool) (int, bool) {
	encodedKey, err := json.Marshal(key)
	if err != nil {
		return 0, false
	}
	encodedValue, ok := auditEncodedBytes(value)
	if !ok {
		return 0, false
	}
	return len(encodedKey) + 1 + encodedValue + boolInt(comma), true
}

func auditEncodedBytes(value any) (int, bool) {
	encoded, err := json.Marshal(value)
	return len(encoded), err == nil && len(encoded) <= maxAuditObjectBytes
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func sensitiveKey(key string) bool {
	normalized := strings.ToLower(key)
	for _, fragment := range []string{
		"password", "token", "digest", "secret", "key", "authorization", "cookie", "verification",
		"upstream", "raw_response", "private_key", "api_key", "wps_sid", "dsn",
	} {
		if strings.Contains(normalized, fragment) {
			return true
		}
	}
	return false
}

func sensitiveValue(value string) bool {
	normalized := strings.ToLower(strings.TrimSpace(value))
	for _, marker := range []string{"bearer ", "$argon2", "password=", "token=", "secret=", "authorization:", "cookie:"} {
		if strings.Contains(normalized, marker) {
			return true
		}
	}
	return false
}

func observerSensitiveTarget(targetType string) bool {
	switch strings.ToLower(targetType) {
	case "admin_user", "admin_session", "security_policy", "cross_group_action", "system_operation":
		return true
	default:
		return false
	}
}

func truncateRunes(value string, limit int) string {
	if limit <= 0 || !utf8.ValidString(value) {
		return ""
	}
	if utf8.RuneCountInString(value) <= limit {
		return value
	}
	end := 0
	count := 0
	for index := range value {
		if count == limit {
			end = index
			break
		}
		count++
	}
	return strings.Clone(value[:end])
}

func boundedString(value string, limit int, changed bool) (string, bool) {
	bounded := truncateRunes(value, limit)
	return bounded, changed || bounded != value
}

func cloneOptionalBounded(value *string, limit int, changed bool) (*string, bool) {
	if value == nil {
		return nil, changed
	}
	truncated := truncateRunes(*value, limit)
	return &truncated, changed || truncated != *value
}
