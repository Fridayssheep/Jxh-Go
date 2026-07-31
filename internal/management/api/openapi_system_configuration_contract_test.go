package adminapi

import (
	"os"
	"path/filepath"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestOpenAPISystemConfigurationIsStructured(t *testing.T) {
	document, err := os.ReadFile(filepath.Join("..", "..", "..", "..", "..", "docs", "api", "jxh-manager-openapi.yaml"))
	if errorsIsNotExist(err) {
		t.Skip("workspace OpenAPI document is not present in a standalone bot checkout")
	}
	if err != nil {
		t.Fatal(err)
	}
	var spec struct {
		Paths      map[string]map[string]any `yaml:"paths"`
		Components struct {
			Schemas map[string]map[string]any `yaml:"schemas"`
		} `yaml:"components"`
	}
	if err := yaml.Unmarshal(document, &spec); err != nil {
		t.Fatal(err)
	}

	configuration := requireOpenAPISchema(t, spec.Components.Schemas, "SystemConfiguration")
	for _, field := range []string{
		"wps",
		"ai",
		"quote",
		"time",
		"retention",
		"environment_overrides",
		"version",
		"applied_version",
		"restart_required",
		"restart_supported",
	} {
		requireOpenAPIRequired(t, configuration, field)
	}
	if _, exists := requireSystemConfigurationProperties(t, configuration)["yaml"]; exists {
		t.Fatal("SystemConfiguration must not expose raw yaml")
	}

	patch := requireOpenAPISchema(t, spec.Components.Schemas, "SystemConfigurationPatch")
	patchProperties := requireSystemConfigurationProperties(t, patch)
	requireExactOpenAPIProperties(t, patchProperties, "wps", "ai", "quote", "time", "retention")
	for _, schemaName := range []string{
		"ConfiguredSecret",
		"SecretUpdate",
		"WPSSettings",
		"WPSSettingsPatch",
		"AISettings",
		"AISettingsPatch",
		"QuoteSettings",
		"QuoteSettingsPatch",
		"TimeSettings",
		"TimeSettingsPatch",
		"RetentionSettings",
		"RetentionSettingsPatch",
		"SystemConfiguration",
		"SystemConfigurationPatch",
		"BotRestartRequest",
	} {
		schema := requireOpenAPISchema(t, spec.Components.Schemas, schemaName)
		if schema["additionalProperties"] != false {
			t.Fatalf("%s additionalProperties = %v, want false", schemaName, schema["additionalProperties"])
		}
	}

	secretUpdate := requireOpenAPISchema(t, spec.Components.Schemas, "SecretUpdate")
	operation := requireOpenAPIProperty(t, secretUpdate, "operation")
	requireExactOpenAPIStringEnum(t, operation, "replace", "clear")

	permission := requireOpenAPISchema(t, spec.Components.Schemas, "Permission")
	requireOpenAPIStringEnumValue(t, permission, "bot:restart")

	restartPath, exists := spec.Paths["/system/bot/restart"]
	if !exists {
		t.Fatal("POST /system/bot/restart is missing")
	}
	restartOperation, ok := restartPath["post"].(map[string]any)
	if !ok {
		t.Fatal("POST /system/bot/restart is missing")
	}
	if restartOperation["x-status"] != "implemented" {
		t.Fatalf("POST /system/bot/restart x-status = %v, want implemented", restartOperation["x-status"])
	}
	restartRequest := requireOpenAPIRequestSchema(t, spec.Components.Schemas, restartOperation)
	requireOpenAPIRequired(t, restartRequest, "confirmation")
	requireOpenAPIRequired(t, restartRequest, "configuration_version")
	confirmation := requireOpenAPIProperty(t, restartRequest, "confirmation")
	if confirmation["const"] != "restart" {
		t.Fatalf("Bot restart confirmation const = %v, want restart", confirmation["const"])
	}
	configurationVersion := requireOpenAPIProperty(t, restartRequest, "configuration_version")
	if configurationVersion["type"] != "integer" {
		t.Fatalf("Bot restart configuration_version type = %v, want integer", configurationVersion["type"])
	}
	requireOpenAPIResponseSchemaRef(t, restartOperation, "202", "#/components/schemas/SystemOperation")
}

func requireSystemConfigurationProperties(t *testing.T, schema map[string]any) map[string]any {
	t.Helper()
	properties, ok := schema["properties"].(map[string]any)
	if !ok {
		t.Fatalf("schema has no properties: %v", schema)
	}
	return properties
}

func requireExactOpenAPIProperties(t *testing.T, properties map[string]any, names ...string) {
	t.Helper()
	if len(properties) != len(names) {
		t.Fatalf("schema properties = %v, want exactly %v", properties, names)
	}
	for _, name := range names {
		if _, exists := properties[name]; !exists {
			t.Fatalf("schema properties = %v, missing %s", properties, name)
		}
	}
}

func requireExactOpenAPIStringEnum(t *testing.T, schema map[string]any, values ...string) {
	t.Helper()
	if raw, ok := schema["enum"].([]any); !ok || len(raw) != len(values) {
		t.Fatalf("schema enum = %v, want exactly %v", schema["enum"], values)
	}
	for _, value := range values {
		requireOpenAPIStringEnumValue(t, schema, value)
	}
}

func requireOpenAPIStringEnumValue(t *testing.T, schema map[string]any, value string) {
	t.Helper()
	raw, ok := schema["enum"].([]any)
	if !ok {
		t.Fatalf("schema has no enum: %v", schema)
	}
	for _, candidate := range raw {
		if candidate == value {
			return
		}
	}
	t.Fatalf("schema enum %v does not contain %q", raw, value)
}

func requireOpenAPIRequestSchema(t *testing.T, schemas map[string]map[string]any, operation map[string]any) map[string]any {
	t.Helper()
	requestBody, ok := operation["requestBody"].(map[string]any)
	if !ok {
		t.Fatalf("operation has no request body: %v", operation)
	}
	content, ok := requestBody["content"].(map[string]any)
	if !ok {
		t.Fatalf("request body has no content: %v", requestBody)
	}
	mediaType, ok := content["application/json"].(map[string]any)
	if !ok {
		t.Fatalf("request body has no application/json content: %v", content)
	}
	schema, ok := mediaType["schema"].(map[string]any)
	if !ok {
		t.Fatalf("request body has no schema: %v", mediaType)
	}
	ref, ok := schema["$ref"].(string)
	if !ok {
		return schema
	}
	const prefix = "#/components/schemas/"
	if len(ref) <= len(prefix) || ref[:len(prefix)] != prefix {
		t.Fatalf("unsupported request schema ref %q", ref)
	}
	return requireOpenAPISchema(t, schemas, ref[len(prefix):])
}

func requireOpenAPIResponseSchemaRef(t *testing.T, operation map[string]any, status, want string) {
	t.Helper()
	responses, ok := operation["responses"].(map[string]any)
	if !ok {
		t.Fatalf("operation has no responses: %v", operation)
	}
	response, ok := responses[status].(map[string]any)
	if !ok {
		t.Fatalf("operation has no %s response: %v", status, responses)
	}
	content, ok := response["content"].(map[string]any)
	if !ok {
		t.Fatalf("response has no content: %v", response)
	}
	mediaType, ok := content["application/json"].(map[string]any)
	if !ok {
		t.Fatalf("response has no application/json content: %v", content)
	}
	schema, ok := mediaType["schema"].(map[string]any)
	if !ok || schema["$ref"] != want {
		t.Fatalf("response schema = %v, want ref %s", mediaType["schema"], want)
	}
}
