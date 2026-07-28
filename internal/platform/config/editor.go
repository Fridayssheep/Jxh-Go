package config

import (
	"bytes"
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

	"gopkg.in/yaml.v3"
)

const (
	SecretPlaceholder        = "__JXH_SECRET_UNCHANGED__"
	maxEditableDocumentBytes = 1 << 20
	maxJavaScriptSafeInteger = uint64(1<<53 - 1)
)

var (
	ErrInvalidEditor   = errors.New("invalid config file editor")
	ErrInvalidDocument = errors.New("invalid config document")
	ErrVersionConflict = errors.New("config version conflict")
)

type EditableDocument struct {
	YAML                 string
	Version              uint64
	MaskedFields         []string
	EnvironmentOverrides []string
}

type FileEditor struct {
	path string
	mu   sync.Mutex
}

var sensitiveConfigFields = [][]string{
	{"admin", "session_secret"},
	{"ai", "api_key"},
	{"database", "dsn"},
	{"database", "password"},
	{"onebot", "access_token"},
	{"wps", "share_url"},
	{"wps", "sid"},
}

var environmentConfigFields = map[string]string{
	"JXH_ADMIN_ADDR":                          "admin.addr",
	"JXH_ADMIN_COOKIE_SECURE":                 "admin.cookie_secure",
	"JXH_ADMIN_IDLE_TIMEOUT_SECONDS":          "admin.idle_timeout_seconds",
	"JXH_ADMIN_LOGIN_MAX_ATTEMPTS":            "admin.login_max_attempts",
	"JXH_ADMIN_LOGIN_WINDOW_SECONDS":          "admin.login_window_seconds",
	"JXH_ADMIN_MAX_CONCURRENT_REQUESTS":       "admin.max_concurrent_requests",
	"JXH_ADMIN_MAX_REQUEST_BODY_BYTES":        "admin.max_request_body_bytes",
	"JXH_ADMIN_PUBLIC_ORIGIN":                 "admin.public_origin",
	"JXH_ADMIN_READ_HEADER_TIMEOUT_SECONDS":   "admin.read_header_timeout_seconds",
	"JXH_ADMIN_READ_TIMEOUT_SECONDS":          "admin.read_timeout_seconds",
	"JXH_ADMIN_SESSION_IDLE_TIMEOUT_SECONDS":  "admin.session_idle_timeout_seconds",
	"JXH_ADMIN_SESSION_SECRET":                "admin.session_secret",
	"JXH_ADMIN_SESSION_TTL_SECONDS":           "admin.session_ttl_seconds",
	"JXH_ADMIN_SHUTDOWN_TIMEOUT_SECONDS":      "admin.shutdown_timeout_seconds",
	"JXH_ADMIN_TRUSTED_PROXIES":               "admin.trusted_proxies",
	"JXH_ADMIN_WRITE_TIMEOUT_SECONDS":         "admin.write_timeout_seconds",
	"JXH_AI_API_KEY":                          "ai.api_key",
	"JXH_AI_BASE_URL":                         "ai.base_url",
	"JXH_AI_MODEL":                            "ai.model",
	"JXH_AI_PROVIDER":                         "ai.provider",
	"JXH_DATABASE_CHARSET":                    "database.charset",
	"JXH_DATABASE_CONN_MAX_IDLE_TIME_SECONDS": "database.conn_max_idle_time_seconds",
	"JXH_DATABASE_CONN_MAX_LIFETIME_SECONDS":  "database.conn_max_lifetime_seconds",
	"JXH_DATABASE_HOST":                       "database.host",
	"JXH_DATABASE_LOC":                        "database.loc",
	"JXH_DATABASE_MAX_IDLE_CONNS":             "database.max_idle_conns",
	"JXH_DATABASE_MAX_OPEN_CONNS":             "database.max_open_conns",
	"JXH_DATABASE_NAME":                       "database.name",
	"JXH_DATABASE_PARSE_TIME":                 "database.parse_time",
	"JXH_DATABASE_PING_TIMEOUT_SECONDS":       "database.ping_timeout_seconds",
	"JXH_DATABASE_PORT":                       "database.port",
	"JXH_DATABASE_USER":                       "database.user",
	"JXH_MYSQL_DSN":                           "database.dsn",
	"JXH_MYSQL_PASSWORD":                      "database.password",
	"JXH_ONEBOT_TOKEN":                        "onebot.access_token",
	"JXH_ONEBOT_WS_URL":                       "onebot.ws_url",
	"JXH_QUOTE_BASE_URL":                      "quote.base_url",
	"JXH_WPS_SHARE_URL":                       "wps.share_url",
	"JXH_WPS_SID":                             "wps.sid",
	"JXH_WPS_TIMEOUT_SEC":                     "wps.timeout_sec",
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

func (e *FileEditor) Read() (EditableDocument, error) {
	if e == nil {
		return EditableDocument{}, ErrInvalidEditor
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.readLocked()
}

func (e *FileEditor) Update(expectedVersion uint64, candidate string) (EditableDocument, error) {
	if e == nil || expectedVersion == 0 || len(candidate) == 0 || len(candidate) > maxEditableDocumentBytes {
		return EditableDocument{}, ErrInvalidDocument
	}
	e.mu.Lock()
	defer e.mu.Unlock()

	currentBytes, err := os.ReadFile(e.path)
	if err != nil {
		return EditableDocument{}, fmt.Errorf("read config file: %w", err)
	}
	if versionFor(currentBytes) != expectedVersion {
		return EditableDocument{}, ErrVersionConflict
	}
	currentNode, err := decodeYAMLDocument(currentBytes)
	if err != nil {
		return EditableDocument{}, fmt.Errorf("decode current config: %w", err)
	}
	candidateNode, err := decodeYAMLDocument([]byte(candidate))
	if err != nil {
		return EditableDocument{}, ErrInvalidDocument
	}
	if err := restoreSecrets(candidateNode, currentNode); err != nil {
		return EditableDocument{}, ErrInvalidDocument
	}
	restored, err := yaml.Marshal(candidateNode)
	if err != nil || bytes.Contains(restored, []byte(SecretPlaceholder)) || validateStrictDocument(restored) != nil {
		return EditableDocument{}, ErrInvalidDocument
	}
	if err := replaceFileAtomic(e.path, restored); err != nil {
		return EditableDocument{}, fmt.Errorf("replace config file: %w", err)
	}
	return e.readLocked()
}

func (e *FileEditor) readLocked() (EditableDocument, error) {
	raw, err := os.ReadFile(e.path)
	if err != nil {
		return EditableDocument{}, fmt.Errorf("read config file: %w", err)
	}
	if len(raw) == 0 || len(raw) > maxEditableDocumentBytes {
		return EditableDocument{}, ErrInvalidDocument
	}
	document, err := decodeYAMLDocument(raw)
	if err != nil || validateStrictDocument(raw) != nil {
		return EditableDocument{}, ErrInvalidDocument
	}
	masked := maskSecrets(document)
	encoded, err := yaml.Marshal(document)
	if err != nil {
		return EditableDocument{}, fmt.Errorf("encode editable config: %w", err)
	}
	return EditableDocument{
		YAML: string(encoded), Version: versionFor(raw), MaskedFields: masked,
		EnvironmentOverrides: activeEnvironmentOverrides(),
	}, nil
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

func maskSecrets(document *yaml.Node) []string {
	masked := make([]string, 0, len(sensitiveConfigFields))
	for _, path := range sensitiveConfigFields {
		value := valueAtPath(document, path)
		if value == nil || value.Kind != yaml.ScalarNode || value.Value == "" {
			continue
		}
		value.Tag = "!!str"
		value.Value = SecretPlaceholder
		value.Style = 0
		masked = append(masked, strings.Join(path, "."))
	}
	return masked
}

func restoreSecrets(candidate, current *yaml.Node) error {
	for _, path := range sensitiveConfigFields {
		value := valueAtPath(candidate, path)
		if value == nil || value.Kind != yaml.ScalarNode || value.Value != SecretPlaceholder {
			continue
		}
		prior := valueAtPath(current, path)
		if prior == nil || prior.Kind != yaml.ScalarNode {
			return ErrInvalidDocument
		}
		*value = *prior
	}
	return nil
}

func valueAtPath(document *yaml.Node, path []string) *yaml.Node {
	if document == nil || len(document.Content) != 1 {
		return nil
	}
	current := document.Content[0]
	for _, segment := range path {
		if current.Kind != yaml.MappingNode {
			return nil
		}
		var next *yaml.Node
		for index := 0; index+1 < len(current.Content); index += 2 {
			if current.Content[index].Value == segment {
				next = current.Content[index+1]
				break
			}
		}
		if next == nil {
			return nil
		}
		current = next
	}
	return current
}

func activeEnvironmentOverrides() []string {
	result := make([]string, 0)
	seen := make(map[string]struct{})
	for key, field := range environmentConfigFields {
		if value, exists := os.LookupEnv(key); !exists || value == "" {
			continue
		}
		if _, exists := seen[field]; exists {
			continue
		}
		seen[field] = struct{}{}
		result = append(result, field)
	}
	sort.Strings(result)
	return result
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

func restrictedFileMode(mode os.FileMode) os.FileMode {
	return mode.Perm() & 0o600
}
