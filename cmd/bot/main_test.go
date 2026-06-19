package main

import (
	"context"
	"testing"

	"github.com/zjutjh/jxh-go/internal/ai"
	"github.com/zjutjh/jxh-go/internal/config"
)

func TestNewAIServiceReturnsNilWhenDisabled(t *testing.T) {
	cfg := config.Default()
	cfg.AI.Enabled = false

	service, err := newAIService(context.Background(), cfg, ai.StaticRetriever{})
	if err != nil {
		t.Fatalf("newAIService returned error: %v", err)
	}
	if service != nil {
		t.Fatal("newAIService returned service, want nil")
	}
}
