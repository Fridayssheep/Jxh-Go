package ai

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/flow/agent"
	"github.com/cloudwego/eino/schema"
	"github.com/zjutjh/jxh-go/internal/knowledge"
)

type fakeAgent struct {
	generate func(context.Context, []*schema.Message) (*schema.Message, error)
}

type scriptedModelState struct {
	mu        sync.Mutex
	calls     int
	toolNames []string
}

type scriptedToolModel struct {
	state *scriptedModelState
}

func (m *scriptedToolModel) WithTools(tools []*schema.ToolInfo) (model.ToolCallingChatModel, error) {
	m.state.mu.Lock()
	defer m.state.mu.Unlock()
	for _, info := range tools {
		m.state.toolNames = append(m.state.toolNames, info.Name)
	}
	return &scriptedToolModel{state: m.state}, nil
}

func (m *scriptedToolModel) Generate(context.Context, []*schema.Message, ...model.Option) (*schema.Message, error) {
	m.state.mu.Lock()
	defer m.state.mu.Unlock()
	m.state.calls++
	switch m.state.calls {
	case 1:
		return toolCallMessage("call-1", `{"query":"[","mode":"regex","limit":5}`), nil
	case 2:
		return toolCallMessage("call-2", `{"query":"宿舍 空调","mode":"and","limit":5}`), nil
	default:
		return schema.AssistantMessage("空调说明", nil), nil
	}
}

func (m *scriptedToolModel) Stream(context.Context, []*schema.Message, ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	return nil, errors.New("stream is not implemented")
}

func toolCallMessage(id, arguments string) *schema.Message {
	return schema.AssistantMessage("", []schema.ToolCall{{
		ID: id,
		Function: schema.FunctionCall{
			Name:      "search_knowledge",
			Arguments: arguments,
		},
	}})
}

func (f fakeAgent) Generate(ctx context.Context, input []*schema.Message, _ ...agent.AgentOption) (*schema.Message, error) {
	return f.generate(ctx, input)
}

func TestServiceReturnsNoResultAnswerWithoutSources(t *testing.T) {
	service := newService(fakeAgent{generate: func(_ context.Context, messages []*schema.Message) (*schema.Message, error) {
		if messages[0].Role != schema.System ||
			!strings.Contains(messages[0].Content, "首次搜索优先使用 and 模式") ||
			!strings.Contains(messages[0].Content, "不得使用模型自身知识补全") {
			t.Fatalf("system prompt = %+v", messages[0])
		}
		return schema.AssistantMessage("知识库暂时没有足够信息", nil), nil
	}}, time.Second, 100)
	answer, sources, err := service.AnswerWithSources(context.Background(), "问题")
	if err != nil {
		t.Fatalf("AnswerWithSources returned error: %v", err)
	}
	if answer != "知识库暂时没有足够信息" || len(sources) != 0 {
		t.Fatalf("answer/sources = %q/%v", answer, sources)
	}
}

func TestServiceReturnsDeduplicatedSearchSources(t *testing.T) {
	service := newService(fakeAgent{generate: func(ctx context.Context, _ []*schema.Message) (*schema.Message, error) {
		collector := sourceCollectorFromContext(ctx)
		collector.add("source-a")
		collector.add("source-a")
		collector.add("source-b")
		return schema.AssistantMessage("答案", nil), nil
	}}, time.Second, 100)
	answer, sources, err := service.AnswerWithSources(context.Background(), "问题")
	if err != nil {
		t.Fatalf("AnswerWithSources returned error: %v", err)
	}
	if answer != "答案" || !reflect.DeepEqual(sources, []string{"source-a", "source-b"}) {
		t.Fatalf("answer/sources = %q/%v", answer, sources)
	}
}

func TestServiceAppliesQuestionLimitAndTimeout(t *testing.T) {
	service := newService(fakeAgent{generate: func(ctx context.Context, messages []*schema.Message) (*schema.Message, error) {
		if messages[1].Content != "问题" {
			t.Fatalf("question = %q, want %q", messages[1].Content, "问题")
		}
		<-ctx.Done()
		return nil, ctx.Err()
	}}, 10*time.Millisecond, 2)
	_, _, err := service.AnswerWithSources(context.Background(), "问题很长")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error = %v, want deadline exceeded", err)
	}
}

func TestSearchKnowledgeReturnsStructuredErrorAndCollectsHits(t *testing.T) {
	index := knowledge.NewIndexRef([]knowledge.Entry{{
		SourceKey: "source", Keyword: "宿舍", Content: "宿舍 空调", Answer: "答案", Enabled: true, AIEnabled: true,
	}})
	collector := &sourceCollector{seen: make(map[string]struct{})}
	ctx := context.WithValue(context.Background(), sourceCollectorKey{}, collector)
	invalid, err := searchKnowledge(ctx, index, SearchToolInput{Query: "[", Mode: "regex"})
	if err != nil || invalid.Error == "" {
		t.Fatalf("invalid output/error = %+v/%v", invalid, err)
	}
	valid, err := searchKnowledge(ctx, index, SearchToolInput{Query: "宿舍 空调", Mode: "and"})
	if err != nil || len(valid.Results) != 1 {
		t.Fatalf("valid output/error = %+v/%v", valid, err)
	}
	if !reflect.DeepEqual(collector.keys(), []string{"source"}) {
		t.Fatalf("collected sources = %v", collector.keys())
	}
}

func TestReActAgentRetriesSearchAndAnswersFromHit(t *testing.T) {
	state := &scriptedModelState{}
	service, err := NewService(context.Background(), Options{
		Model: &scriptedToolModel{state: state},
		Knowledge: knowledge.NewIndexRef([]knowledge.Entry{{
			SourceKey: "dorm-ac", Keyword: "宿舍空调", Content: "宿舍 空调", Answer: "空调说明", Enabled: true, AIEnabled: true,
		}}),
		Timeout: time.Second,
	})
	if err != nil {
		t.Fatalf("NewService returned error: %v", err)
	}
	answer, sources, err := service.AnswerWithSources(context.Background(), "宿舍空调怎么用")
	if err != nil {
		t.Fatalf("AnswerWithSources returned error: %v", err)
	}
	if answer != "空调说明" || !reflect.DeepEqual(sources, []string{"dorm-ac"}) {
		t.Fatalf("answer/sources = %q/%v", answer, sources)
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.calls != 3 {
		t.Fatalf("model calls = %d, want 3", state.calls)
	}
	if !reflect.DeepEqual(state.toolNames, []string{"search_knowledge"}) {
		t.Fatalf("bound tools = %v", state.toolNames)
	}
}
