package adminapi

import (
	"os"
	"path/filepath"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestOpenAPIJoinRejectionContracts(t *testing.T) {
	document, err := os.ReadFile(filepath.Join("..", "..", "..", "..", "..", "docs", "api", "jxh-manager-openapi.yaml"))
	if errorsIsNotExist(err) {
		t.Skip("workspace OpenAPI document is not present in a standalone bot checkout")
	}
	if err != nil {
		t.Fatal(err)
	}
	var spec struct {
		Components struct {
			Schemas map[string]map[string]any `yaml:"schemas"`
		} `yaml:"components"`
	}
	if err := yaml.Unmarshal(document, &spec); err != nil {
		t.Fatal(err)
	}

	joinSettings := requireOpenAPISchema(t, spec.Components.Schemas, "JoinRequestGlobalSettings")
	requireOpenAPIRequired(t, joinSettings, "auto_reject_reason")
	reason := requireOpenAPIProperty(t, joinSettings, "auto_reject_reason")
	if reason["minLength"] != 1 || reason["maxLength"] != 500 {
		t.Fatalf("auto_reject_reason bounds = %v..%v, want 1..500", reason["minLength"], reason["maxLength"])
	}

	global := requireOpenAPISchema(t, spec.Components.Schemas, "GlobalSettings")
	requireOpenAPIRequired(t, global, "join_requests")
	requireOpenAPIProperty(t, global, "join_requests")
	globalPatch := requireOpenAPISchema(t, spec.Components.Schemas, "GlobalSettingsPatch")
	if globalPatch["minProperties"] != 1 {
		t.Fatalf("GlobalSettingsPatch minProperties = %v, want 1", globalPatch["minProperties"])
	}
	requireOpenAPIProperty(t, globalPatch, "features")
	requireOpenAPIProperty(t, globalPatch, "join_requests")

	autoReject := requireOpenAPIProperty(t, requireOpenAPISchema(t, spec.Components.Schemas, "JoinRequestPolicy"), "auto_reject")
	if _, fixed := autoReject["const"]; fixed {
		t.Fatalf("JoinRequestPolicy.auto_reject remains fixed: %v", autoReject)
	}
	policyPatch := requireOpenAPISchema(t, spec.Components.Schemas, "JoinRequestPolicyPatch")
	if policyPatch["minProperties"] != 1 {
		t.Fatalf("JoinRequestPolicyPatch minProperties = %v, want 1", policyPatch["minProperties"])
	}
	requireOpenAPIProperty(t, policyPatch, "enabled")
	requireOpenAPIProperty(t, policyPatch, "auto_reject")

	requireOpenAPIRejectReasonUnion(t, spec.Components.Schemas, "JoinDecisionRequest")
	requireOpenAPIRejectReasonUnion(t, spec.Components.Schemas, "BulkJoinDecisionRequest")
}

func requireOpenAPISchema(t *testing.T, schemas map[string]map[string]any, name string) map[string]any {
	t.Helper()
	schema, exists := schemas[name]
	if !exists {
		t.Fatalf("OpenAPI schema %s is missing", name)
	}
	return schema
}

func requireOpenAPIProperty(t *testing.T, schema map[string]any, name string) map[string]any {
	t.Helper()
	properties, ok := schema["properties"].(map[string]any)
	if !ok {
		t.Fatalf("schema has no properties: %v", schema)
	}
	property, ok := properties[name].(map[string]any)
	if !ok {
		t.Fatalf("schema property %s is missing", name)
	}
	return property
}

func requireOpenAPIRequired(t *testing.T, schema map[string]any, name string) {
	t.Helper()
	required, ok := schema["required"].([]any)
	if !ok {
		t.Fatalf("schema has no required fields: %v", schema)
	}
	for _, field := range required {
		if field == name {
			return
		}
	}
	t.Fatalf("schema required fields %v do not contain %s", required, name)
}

func requireOpenAPIRejectReasonUnion(t *testing.T, schemas map[string]map[string]any, name string) {
	t.Helper()
	schema := requireOpenAPISchema(t, schemas, name)
	branches, ok := schema["oneOf"].([]any)
	if !ok || len(branches) != 2 {
		t.Fatalf("%s oneOf = %v, want approve and reject branches", name, schema["oneOf"])
	}
	seen := map[string]bool{}
	for _, rawBranch := range branches {
		branch, ok := rawBranch.(map[string]any)
		if !ok {
			t.Fatalf("%s branch is invalid: %T", name, rawBranch)
		}
		action := requireOpenAPIProperty(t, branch, "action")
		value, ok := action["const"].(string)
		if !ok {
			t.Fatalf("%s branch action has no string const: %v", name, action)
		}
		requireOpenAPIProperty(t, branch, "reason")
		if value == "reject" {
			requireOpenAPIRequired(t, branch, "reason")
		}
		seen[value] = true
	}
	if !seen["approve"] || !seen["reject"] {
		t.Fatalf("%s action branches = %v", name, seen)
	}
}
