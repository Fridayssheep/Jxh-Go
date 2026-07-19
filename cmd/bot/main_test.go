package main

import (
	"context"
	"testing"
	"time"

	"github.com/zjutjh/jxh-go/internal/config"
	"github.com/zjutjh/jxh-go/internal/knowledge"
)

func TestNewAIServiceReturnsNilWhenDisabled(t *testing.T) {
	cfg := config.Default()
	cfg.AI.Enabled = false

	service, err := newAIService(context.Background(), cfg, knowledge.NewIndexRef(nil))
	if err != nil {
		t.Fatalf("newAIService returned error: %v", err)
	}
	if service != nil {
		t.Fatal("newAIService returned service, want nil")
	}
}

func TestNewAIServiceReturnsNilWhenOpenAIBaseURLIsMissing(t *testing.T) {
	cfg := config.Default()
	cfg.AI.Enabled = true
	cfg.AI.Provider = "openai"
	cfg.AI.APIKey = "test-key"
	cfg.AI.Model = "test-model"
	cfg.AI.BaseURL = ""

	service, err := newAIService(context.Background(), cfg, knowledge.NewIndexRef(nil))
	if err != nil {
		t.Fatalf("newAIService returned error: %v", err)
	}
	if service != nil {
		t.Fatal("newAIService returned service, want nil")
	}
}

func TestApplicationLocationUsesConfiguredTimezone(t *testing.T) {
	cfg := config.Default()
	cfg.App.Timezone = "Asia/Shanghai"
	_, offset := time.Now().In(applicationLocation(cfg)).Zone()
	if offset != 8*60*60 {
		t.Fatalf("timezone offset = %d, want %d", offset, 8*60*60)
	}
}
