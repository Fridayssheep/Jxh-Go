package config

import "testing"

func TestLoadAppliesWPSAndAIEnv(t *testing.T) {
	t.Setenv("JXH_WPS_SHARE_URL", "https://example.com/knowledge.xlsx")
	t.Setenv("JXH_WPS_TIMEOUT_SEC", "45")
	t.Setenv("JXH_AI_MODEL", "tool-model")

	cfg, err := Load("")
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if cfg.WPS.ShareURL != "https://example.com/knowledge.xlsx" || cfg.WPS.TimeoutSec != 45 {
		t.Fatalf("WPS config = %+v", cfg.WPS)
	}
	if cfg.AI.Model != "tool-model" {
		t.Fatalf("AI model = %q", cfg.AI.Model)
	}
}
