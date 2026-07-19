package ai

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/zjutjh/jxh-go/internal/knowledge"
)

func TestKnowledgeRetrieverRemovesImageCQFromAnswerMetadata(t *testing.T) {
	answer := "See the map: [CQ:image,file=cache.image,url=https://cdn.example.com/map.png] Gate 1."
	retriever := NewKnowledgeRetriever([]knowledge.Entry{{
		SourceKey: "map",
		Keyword:   "campus map",
		Path:      "Campus [CQ:image,url=https://cdn.example.com/path.png]",
		Aliases:   []string{"map [CQ:image,url=https://cdn.example.com/alias.png]"},
		Answer:    answer,
		Content:   "Knowledge: " + answer,
		Enabled:   true,
		AIEnabled: true,
	}})

	docs, err := retriever.Retrieve(context.Background(), "campus map", 1)
	if err != nil {
		t.Fatalf("Retrieve returned error: %v", err)
	}
	if len(docs) != 1 {
		t.Fatalf("documents = %d, want 1", len(docs))
	}
	if strings.Contains(docs[0].Metadata["answer"], "[CQ:image") || strings.Contains(docs[0].Metadata["answer"], "cdn.example.com") {
		t.Fatalf("answer metadata still contains image CQ data: %q", docs[0].Metadata["answer"])
	}
	if docs[0].Metadata["answer"] != "See the map:  Gate 1." {
		t.Fatalf("answer metadata = %q", docs[0].Metadata["answer"])
	}
	if strings.Contains(docs[0].Content, "[CQ:image") || strings.Contains(docs[0].Content, "cdn.example.com") {
		t.Fatalf("document content still contains persisted image CQ data: %q", docs[0].Content)
	}
	if strings.Contains(docs[0].Metadata["path"], "[CQ:image") || strings.Contains(docs[0].Metadata["path"], "cdn.example.com") {
		t.Fatalf("path metadata still contains persisted image CQ data: %q", docs[0].Metadata["path"])
	}
	prompt := BuildPrompt("where is the map?", docs)
	if strings.Contains(prompt, "cdn.example.com") || strings.Contains(prompt, "[CQ:image") {
		t.Fatalf("AI prompt still contains image CQ data: %q", prompt)
	}
}

func TestKnowledgeRetrieverUsesScoreThreshold(t *testing.T) {
	retriever := NewKnowledgeRetriever([]knowledge.Entry{
		{
			SourceKey: "weak",
			Keyword:   "报到",
			Answer:    "报到时可以查看交通指南",
			Content:   "知识正文：报到时可以查看交通指南",
			Enabled:   true,
			AIEnabled: true,
		},
	}, KnowledgeRetrieverOptions{ScoreThreshold: 0.5})

	docs, err := retriever.Retrieve(context.Background(), "交通", 10)
	if err != nil {
		t.Fatalf("Retrieve returned error: %v", err)
	}
	if len(docs) != 0 {
		t.Fatalf("docs length = %d, want 0", len(docs))
	}
}

func TestKnowledgeRetrieverPassesCacheTTL(t *testing.T) {
	retriever := NewKnowledgeRetriever([]knowledge.Entry{{
		SourceKey: "traffic",
		Keyword:   "交通",
		Answer:    "交通说明",
		Content:   "知识正文：交通说明",
		Enabled:   true,
		AIEnabled: true,
	}}, KnowledgeRetrieverOptions{CacheTTL: time.Minute})

	if retriever.Retriever == nil {
		t.Fatal("retriever engine is nil")
	}
	if retriever.Retriever.CacheTTL() != time.Minute {
		t.Fatalf("cache TTL = %s, want %s", retriever.Retriever.CacheTTL(), time.Minute)
	}
}
