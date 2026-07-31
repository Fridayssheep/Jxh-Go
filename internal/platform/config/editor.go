package config

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/gofrs/flock"
	"gopkg.in/yaml.v3"
)

const (
	maxEditableDocumentBytes = 1 << 20
	maxJavaScriptSafeInteger = uint64(1<<53 - 1)
)

var (
	ErrInvalidEditor   = errors.New("invalid config file editor")
	ErrInvalidDocument = errors.New("invalid config document")
	ErrVersionConflict = errors.New("config version conflict")
)

type FileEditor struct {
	path string
	mu   sync.Mutex
}

var editableEnvironmentFields = map[string]string{
	"JXH_AI_API_KEY":      "ai.api_key",
	"JXH_AI_BASE_URL":     "ai.base_url",
	"JXH_AI_MODEL":        "ai.model",
	"JXH_AI_PROVIDER":     "ai.provider",
	"JXH_QUOTE_BASE_URL":  "quote.base_url",
	"JXH_WPS_SHARE_URL":   "wps.share_url",
	"JXH_WPS_SID":         "wps.sid",
	"JXH_WPS_TIMEOUT_SEC": "wps.timeout_sec",
}

func NewFileEditor(path string) (*FileEditor, error) {
	if strings.TrimSpace(path) == "" {
		return nil, ErrInvalidEditor
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("resolve config path: %w", err)
	}
	return &FileEditor{path: absolute}, nil
}

func (e *FileEditor) Read(ctx context.Context) (Settings, error) {
	if e == nil {
		return Settings{}, ErrInvalidEditor
	}
	if err := ctx.Err(); err != nil {
		return Settings{}, err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.readLocked()
}

func (e *FileEditor) Update(ctx context.Context, expectedVersion uint64, patch SettingsPatch) (Settings, []string, error) {
	if e == nil {
		return Settings{}, nil, ErrInvalidEditor
	}
	if expectedVersion == 0 {
		return Settings{}, nil, ErrInvalidDocument
	}
	if err := validatePatch(patch); err != nil {
		return Settings{}, nil, err
	}
	if managed := managedPatchFields(patch); len(managed) != 0 {
		return Settings{}, nil, &ManagedFieldsError{Fields: managed}
	}
	if err := ctx.Err(); err != nil {
		return Settings{}, nil, err
	}

	e.mu.Lock()
	defer e.mu.Unlock()
	fileLock := flock.New(e.path + ".lock")
	lockContext, cancelLock := context.WithTimeout(ctx, 5*time.Second)
	defer cancelLock()
	locked, err := fileLock.TryLockContext(lockContext, 100*time.Millisecond)
	if err != nil {
		return Settings{}, nil, fmt.Errorf("lock config file: %w", err)
	}
	if !locked {
		return Settings{}, nil, fmt.Errorf("lock config file: %w", context.DeadlineExceeded)
	}
	defer func() { _ = fileLock.Unlock() }()

	currentBytes, err := readConfigDocument(e.path)
	if err != nil {
		return Settings{}, nil, err
	}
	if versionFor(currentBytes) != expectedVersion {
		return Settings{}, nil, ErrVersionConflict
	}
	document, err := decodeYAMLDocument(currentBytes)
	if err != nil {
		return Settings{}, nil, ErrInvalidDocument
	}
	changed, err := applySettingsPatch(document, patch)
	if err != nil {
		return Settings{}, nil, err
	}
	if len(changed) == 0 {
		settings, readErr := settingsFromDocument(e.path, currentBytes)
		return settings, nil, readErr
	}

	candidate, err := yaml.Marshal(document)
	if err != nil || len(candidate) == 0 || len(candidate) > maxEditableDocumentBytes {
		return Settings{}, nil, ErrInvalidDocument
	}
	if err := validateStrictDocument(candidate); err != nil {
		return Settings{}, nil, ErrInvalidDocument
	}
	if _, err := settingsFromDocument(e.path, candidate); err != nil {
		return Settings{}, nil, err
	}
	latestBytes, err := readConfigDocument(e.path)
	if err != nil {
		return Settings{}, nil, err
	}
	if versionFor(latestBytes) != expectedVersion {
		return Settings{}, nil, ErrVersionConflict
	}
	if err := replaceFileAtomic(e.path, candidate); err != nil {
		return Settings{}, nil, fmt.Errorf("replace config file: %w", err)
	}
	settings, err := e.readLocked()
	if err != nil {
		return Settings{}, nil, err
	}
	return settings, changed, nil
}

func (e *FileEditor) readLocked() (Settings, error) {
	raw, err := readConfigDocument(e.path)
	if err != nil {
		return Settings{}, err
	}
	return settingsFromDocument(e.path, raw)
}

func readConfigDocument(path string) ([]byte, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config file: %w", err)
	}
	if len(raw) == 0 || len(raw) > maxEditableDocumentBytes {
		return nil, ErrInvalidDocument
	}
	return raw, nil
}

func settingsFromDocument(path string, raw []byte) (Settings, error) {
	document, err := decodeYAMLDocument(raw)
	if err != nil || validateStrictDocument(raw) != nil {
		return Settings{}, ErrInvalidDocument
	}
	cfg, err := loadConfigBytes(path, raw)
	if err != nil {
		return Settings{}, ErrInvalidDocument
	}
	overrides := activeEditableEnvironmentOverrides()
	settings := Settings{
		WPS: WPSSettings{
			ShareURL:   secretState(cfg.WPS.ShareURL, document, []string{"wps", "share_url"}, "JXH_WPS_SHARE_URL"),
			SID:        secretState(cfg.WPS.SID, document, []string{"wps", "sid"}, "JXH_WPS_SID"),
			Sheet:      cfg.WPS.Sheet,
			TimeoutSec: cfg.WPS.TimeoutSec,
		},
		AI: AISettings{
			Provider:         cfg.AI.Provider,
			BaseURL:          cfg.AI.BaseURL,
			APIKey:           secretState(cfg.AI.APIKey, document, []string{"ai", "api_key"}, "JXH_AI_API_KEY"),
			Model:            cfg.AI.Model,
			TimeoutSec:       cfg.AI.TimeoutSec,
			MaxQuestionChars: cfg.AI.MaxQuestionChars,
		},
		Quote:                QuoteSettings{BaseURL: cfg.Quote.BaseURL, TimeoutSec: cfg.Quote.TimeoutSec},
		Time:                 TimeSettings{AppTimezone: cfg.App.Timezone, SchedulerTimezone: cfg.Scheduler.Timezone},
		Retention:            RetentionSettings{TriggerLogRetentionDays: cfg.Database.TriggerLogRetentionDays},
		EnvironmentOverrides: overrides,
		Version:              versionFor(raw),
	}
	if err := validateEffectiveSettings(settings); err != nil {
		return Settings{}, err
	}
	if err := validateEffectiveSecrets(cfg); err != nil {
		return Settings{}, err
	}
	return settings, nil
}

func secretState(effective string, document *yaml.Node, path []string, environmentKey string) SecretState {
	if value, exists := os.LookupEnv(environmentKey); exists && value != "" {
		return SecretState{Configured: effective != "", Source: SourceEnvironment}
	}
	if valueAtPath(document, path) != nil {
		return SecretState{Configured: effective != "", Source: SourceFile}
	}
	return SecretState{Configured: effective != "", Source: SourceDefault}
}

func validateEffectiveSettings(settings Settings) error {
	patch := SettingsPatch{
		WPS: &WPSSettingsPatch{Sheet: &settings.WPS.Sheet, TimeoutSec: &settings.WPS.TimeoutSec},
		AI: &AISettingsPatch{
			Provider: &settings.AI.Provider, BaseURL: &settings.AI.BaseURL, Model: &settings.AI.Model,
			TimeoutSec: &settings.AI.TimeoutSec, MaxQuestionChars: &settings.AI.MaxQuestionChars,
		},
		Quote:     &QuoteSettingsPatch{BaseURL: &settings.Quote.BaseURL, TimeoutSec: &settings.Quote.TimeoutSec},
		Time:      &TimeSettingsPatch{AppTimezone: &settings.Time.AppTimezone, SchedulerTimezone: &settings.Time.SchedulerTimezone},
		Retention: &RetentionSettingsPatch{TriggerLogRetentionDays: &settings.Retention.TriggerLogRetentionDays},
	}
	return validatePatch(patch)
}

func validateEffectiveSecrets(cfg Config) error {
	fields := make([]FieldError, 0, 3)
	if cfg.WPS.ShareURL != "" && (len([]rune(cfg.WPS.ShareURL)) > 2048 || !validHTTPURL(cfg.WPS.ShareURL, true)) {
		fields = append(fields, FieldError{Path: "wps.share_url", Code: "invalid_url"})
	}
	if len([]rune(cfg.WPS.SID)) > 4096 {
		fields = append(fields, FieldError{Path: "wps.sid", Code: "invalid_length"})
	}
	if len([]rune(cfg.AI.APIKey)) > 8192 {
		fields = append(fields, FieldError{Path: "ai.api_key", Code: "invalid_length"})
	}
	if len(fields) == 0 {
		return nil
	}
	return &ValidationError{Fields: fields}
}

func managedPatchFields(patch SettingsPatch) []string {
	active := activeEditableEnvironmentOverrides()
	if len(active) == 0 {
		return nil
	}
	managed := make(map[string]struct{}, len(active))
	for _, path := range active {
		managed[path] = struct{}{}
	}
	result := make([]string, 0)
	for _, path := range patch.Paths() {
		if _, exists := managed[path]; exists {
			result = append(result, path)
		}
	}
	return result
}

func activeEditableEnvironmentOverrides() []string {
	result := make([]string, 0, len(editableEnvironmentFields))
	for key, field := range editableEnvironmentFields {
		if value, exists := os.LookupEnv(key); exists && value != "" {
			result = append(result, field)
		}
	}
	sort.Strings(result)
	return result
}

func applySettingsPatch(document *yaml.Node, patch SettingsPatch) ([]string, error) {
	changed := make([]string, 0, len(patch.Paths()))
	set := func(path string, value any) error {
		wasChanged, err := setYAMLValue(document, strings.Split(path, "."), value)
		if err == nil && wasChanged {
			changed = append(changed, path)
		}
		return err
	}
	setSecret := func(path string, update *SecretUpdate) error {
		if update == nil {
			return nil
		}
		if update.Operation == SecretClear {
			return set(path, "")
		}
		return set(path, update.Value)
	}
	if value := patch.WPS; value != nil {
		if err := setSecret("wps.share_url", value.ShareURL); err != nil {
			return nil, err
		}
		if err := setSecret("wps.sid", value.SID); err != nil {
			return nil, err
		}
		if value.Sheet != nil {
			if err := set("wps.sheet", strings.TrimSpace(*value.Sheet)); err != nil {
				return nil, err
			}
		}
		if value.TimeoutSec != nil {
			if err := set("wps.timeout_sec", *value.TimeoutSec); err != nil {
				return nil, err
			}
		}
	}
	if value := patch.AI; value != nil {
		for _, item := range []struct {
			path  string
			value any
			set   bool
		}{
			{"ai.provider", dereference(value.Provider), value.Provider != nil},
			{"ai.base_url", dereference(value.BaseURL), value.BaseURL != nil},
			{"ai.model", dereference(value.Model), value.Model != nil},
			{"ai.timeout_sec", dereference(value.TimeoutSec), value.TimeoutSec != nil},
			{"ai.max_question_chars", dereference(value.MaxQuestionChars), value.MaxQuestionChars != nil},
		} {
			if item.set {
				if err := set(item.path, item.value); err != nil {
					return nil, err
				}
			}
		}
		if err := setSecret("ai.api_key", value.APIKey); err != nil {
			return nil, err
		}
	}
	if value := patch.Quote; value != nil {
		if value.BaseURL != nil {
			if err := set("quote.base_url", *value.BaseURL); err != nil {
				return nil, err
			}
		}
		if value.TimeoutSec != nil {
			if err := set("quote.timeout_sec", *value.TimeoutSec); err != nil {
				return nil, err
			}
		}
	}
	if value := patch.Time; value != nil {
		if value.AppTimezone != nil {
			if err := set("app.timezone", *value.AppTimezone); err != nil {
				return nil, err
			}
		}
		if value.SchedulerTimezone != nil {
			if err := set("scheduler.timezone", *value.SchedulerTimezone); err != nil {
				return nil, err
			}
		}
	}
	if value := patch.Retention; value != nil && value.TriggerLogRetentionDays != nil {
		if err := set("database.trigger_log_retention_days", *value.TriggerLogRetentionDays); err != nil {
			return nil, err
		}
	}
	sort.Strings(changed)
	return changed, nil
}

func dereference[T any](value *T) any {
	if value == nil {
		return nil
	}
	return *value
}

func setYAMLValue(document *yaml.Node, path []string, value any) (bool, error) {
	if document == nil || len(document.Content) != 1 || document.Content[0].Kind != yaml.MappingNode || len(path) == 0 {
		return false, ErrInvalidDocument
	}
	current := document.Content[0]
	for _, segment := range path[:len(path)-1] {
		next := mappingValue(current, segment)
		if next == nil {
			current.Content = append(current.Content,
				&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: segment},
				&yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"},
			)
			next = current.Content[len(current.Content)-1]
		}
		if next.Kind != yaml.MappingNode {
			return false, ErrInvalidDocument
		}
		current = next
	}
	leaf := path[len(path)-1]
	next := mappingValue(current, leaf)
	if next == nil {
		current.Content = append(current.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: leaf})
		next = &yaml.Node{}
		current.Content = append(current.Content, next)
	}
	var replacement yaml.Node
	if err := replacement.Encode(value); err != nil {
		return false, err
	}
	if next.Kind == replacement.Kind && next.Tag == replacement.Tag && next.Value == replacement.Value {
		return false, nil
	}
	comments := [3]string{next.HeadComment, next.LineComment, next.FootComment}
	*next = replacement
	next.HeadComment, next.LineComment, next.FootComment = comments[0], comments[1], comments[2]
	return true, nil
}

func mappingValue(mapping *yaml.Node, key string) *yaml.Node {
	if mapping == nil || mapping.Kind != yaml.MappingNode {
		return nil
	}
	for index := 0; index+1 < len(mapping.Content); index += 2 {
		if mapping.Content[index].Value == key {
			return mapping.Content[index+1]
		}
	}
	return nil
}

func decodeYAMLDocument(data []byte) (*yaml.Node, error) {
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	var document yaml.Node
	if err := decoder.Decode(&document); err != nil {
		return nil, err
	}
	if len(document.Content) != 1 || document.Content[0].Kind != yaml.MappingNode {
		return nil, errors.New("config must be a mapping")
	}
	var extra yaml.Node
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return nil, errors.New("config must contain exactly one document")
	}
	return &document, nil
}

func validateStrictDocument(data []byte) error {
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	var cfg Config
	if err := decoder.Decode(&cfg); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return errors.New("config must contain exactly one document")
	}
	return nil
}

func valueAtPath(document *yaml.Node, path []string) *yaml.Node {
	if document == nil || len(document.Content) != 1 {
		return nil
	}
	current := document.Content[0]
	for _, segment := range path {
		current = mappingValue(current, segment)
		if current == nil {
			return nil
		}
	}
	return current
}

func versionFor(data []byte) uint64 {
	digest := sha256.Sum256(data)
	version := binary.BigEndian.Uint64(digest[:8]) & maxJavaScriptSafeInteger
	if version == 0 {
		return 1
	}
	return version
}

func replaceFileAtomic(path string, data []byte) (err error) {
	directory := filepath.Dir(path)
	current, err := os.Stat(path)
	if err != nil {
		return err
	}
	finalMode := restrictedFileMode(current.Mode())
	temporary, err := os.CreateTemp(directory, ".jxh-config-*.tmp")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer func() {
		_ = temporary.Close()
		if err != nil {
			_ = os.Remove(temporaryPath)
		}
	}()
	if err = temporary.Chmod(0o600); err != nil {
		return err
	}
	if _, err = temporary.Write(data); err != nil {
		return err
	}
	if err = temporary.Sync(); err != nil {
		return err
	}
	if err = temporary.Close(); err != nil {
		return err
	}
	if err = os.Chmod(temporaryPath, finalMode); err != nil {
		return err
	}
	if err = os.Rename(temporaryPath, path); err != nil {
		return err
	}
	if err = os.Chmod(path, finalMode); err != nil {
		return err
	}
	if runtime.GOOS != "windows" {
		directoryFile, openErr := os.Open(directory)
		if openErr != nil {
			return openErr
		}
		err = directoryFile.Sync()
		closeErr := directoryFile.Close()
		if err == nil {
			err = closeErr
		}
	}
	return err
}

func restrictedFileMode(mode os.FileMode) os.FileMode { return mode.Perm() & 0o600 }
