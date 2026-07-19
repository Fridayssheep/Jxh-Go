package ai

import (
	"context"
	"time"

	"github.com/zjutjh/jxh-go/internal/cqreply"
	"github.com/zjutjh/jxh-go/internal/knowledge"
)

type KnowledgeRetriever struct {
	Retriever *knowledge.RetrievalEngine
}

type KnowledgeRetrieverOptions struct {
	ScoreThreshold float64
	CacheTTL       time.Duration
}

func NewKnowledgeRetriever(entries []knowledge.Entry, options ...KnowledgeRetrieverOptions) KnowledgeRetriever {
	var opts KnowledgeRetrieverOptions
	if len(options) > 0 {
		opts = options[0]
	}
	return KnowledgeRetriever{Retriever: knowledge.NewRetrievalEngine(knowledge.RetrievalOptions{
		Entries:        entries,
		ScoreThreshold: opts.ScoreThreshold,
		CacheTTL:       opts.CacheTTL,
	})}
}

func (r KnowledgeRetriever) Retrieve(ctx context.Context, query string, topK int) ([]Document, error) {
	if r.Retriever == nil {
		return nil, nil
	}
	docs, err := r.Retriever.Retrieve(ctx, query, topK)
	if err != nil {
		return nil, err
	}
	out := make([]Document, 0, len(docs))
	for _, doc := range docs {
		metadata := map[string]string{
			"keyword": cqreply.Parse(doc.Entry.Keyword).PlainText,
			"answer":  cqreply.Parse(doc.Entry.Answer).PlainText,
		}
		if doc.Entry.Category != "" {
			metadata["category"] = cqreply.Parse(doc.Entry.Category).PlainText
		}
		if doc.Entry.Path != "" {
			metadata["path"] = cqreply.Parse(doc.Entry.Path).PlainText
		}
		out = append(out, Document{
			ID:       cqreply.Parse(doc.Entry.SourceKey).PlainText,
			Content:  cqreply.Parse(doc.Entry.Content).PlainText,
			Metadata: metadata,
			Score:    doc.Score,
		})
	}
	return out, nil
}
