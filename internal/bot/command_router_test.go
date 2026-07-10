package bot

import (
	"context"
	"testing"
	"time"

	"github.com/zjutjh/jxh-go/internal/ai"
	"github.com/zjutjh/jxh-go/internal/cache"
	"github.com/zjutjh/jxh-go/internal/commands"
	"github.com/zjutjh/jxh-go/internal/knowledge"
)

type recordingSender struct {
	groupID int64
	text    string
}

func (s *recordingSender) SendGroupText(ctx context.Context, groupID int64, text string) error {
	_ = ctx
	s.groupID = groupID
	s.text = text
	return nil
}

func (s *recordingSender) SendGroupMessage(ctx context.Context, groupID int64, message any) error {
	_ = ctx
	_ = groupID
	_ = message
	return nil
}

type recordingModerator struct {
	recordingSender
	bannedUserID int64
	bannedGroup  int64
	banDuration  int64
	restarted    bool
}

func (s *recordingModerator) SetGroupBan(ctx context.Context, groupID, userID int64, duration time.Duration) error {
	_ = ctx
	s.bannedGroup = groupID
	s.bannedUserID = userID
	s.banDuration = int64(duration.Seconds())
	return nil
}

func (s *recordingModerator) SetRestart(ctx context.Context) error {
	_ = ctx
	s.restarted = true
	return nil
}

func TestGroupCommandRouterIgnoresBareSlashCommand(t *testing.T) {
	sender := &recordingSender{}
	router := NewGroupCommandRouter(Options{})

	handled, err := router.Handle(context.Background(), GroupMessage{
		GroupID: 123,
		Text:    "/test",
	}, sender)

	if err != nil {
		t.Fatalf("Handle returned error: %v", err)
	}
	if !handled {
		t.Fatal("Handle did not swallow bare /test without mentioning self")
	}
	if sender.text != "" {
		t.Fatalf("sent text = %q, want no response", sender.text)
	}
}

func TestGroupCommandRouterIgnoresCommandWhenMentioningAnotherUser(t *testing.T) {
	sender := &recordingSender{}
	router := NewGroupCommandRouter(Options{})

	handled, err := router.Handle(context.Background(), GroupMessage{
		GroupID: 123,
		SelfID:  999,
		Text:    "/test",
		AtUsers: []int64{
			456,
		},
	}, sender)

	if err != nil {
		t.Fatalf("Handle returned error: %v", err)
	}
	if !handled {
		t.Fatal("Handle did not swallow /test when the message mentions another user")
	}
	if sender.text != "" {
		t.Fatalf("sent text = %q, want no response", sender.text)
	}
}

func TestPipelineSwallowsBareSlashCommandBeforeKnowledgeLookup(t *testing.T) {
	knowledgeCache := cache.NewKnowledge()
	knowledgeCache.Replace(knowledge.NewKeywordIndex([]knowledge.Entry{{
		SourceKey:  "slash-test",
		Keyword:    "/test",
		Answer:     "不应该触发",
		Enabled:    true,
		ExactReply: true,
	}}))
	sender := &recordingSender{}
	pipeline := NewPipeline(Options{
		Knowledge: knowledgeCache,
		Sender:    sender,
	})

	err := pipeline.HandleGroupMessage(context.Background(), GroupMessage{
		GroupID: 123,
		Text:    "/test",
	})

	if err != nil {
		t.Fatalf("HandleGroupMessage returned error: %v", err)
	}
	if sender.text != "" {
		t.Fatalf("sent text = %q, want no response", sender.text)
	}
}

func TestGroupCommandRouterHandlesCommandWhenMentioningSelf(t *testing.T) {
	sender := &recordingSender{}
	router := NewGroupCommandRouter(Options{})

	handled, err := router.Handle(context.Background(), GroupMessage{
		GroupID: 123,
		SelfID:  999,
		Text:    "/test",
		AtUsers: []int64{
			999,
		},
	}, sender)

	if err != nil {
		t.Fatalf("Handle returned error: %v", err)
	}
	if !handled {
		t.Fatal("Handle did not handle /test when the message mentions self")
	}
	if sender.text != "精小弘正常" {
		t.Fatalf("sent text = %q, want %q", sender.text, "精小弘正常")
	}
}

func TestGroupCommandRouterIgnoresBareAICommand(t *testing.T) {
	sender := &recordingSender{}
	router := NewGroupCommandRouter(Options{})

	handled, err := router.Handle(context.Background(), GroupMessage{
		GroupID: 123,
		Text:    "/ai 报到",
	}, sender)

	if err != nil {
		t.Fatalf("Handle returned error: %v", err)
	}
	if !handled {
		t.Fatal("Handle did not swallow bare /ai without mentioning self")
	}
	if sender.text != "" {
		t.Fatalf("sent text = %q, want no response", sender.text)
	}
}

func TestGroupCommandRouterHandlesAICommandWhenMentioningSelf(t *testing.T) {
	sender := &recordingSender{}
	router := NewGroupCommandRouter(Options{})

	handled, err := router.Handle(context.Background(), GroupMessage{
		GroupID: 123,
		SelfID:  999,
		Text:    "/ai 报到",
		AtUsers: []int64{
			999,
		},
	}, sender)

	if err != nil {
		t.Fatalf("Handle returned error: %v", err)
	}
	if !handled {
		t.Fatal("Handle did not handle /ai when the message mentions self")
	}
	if sender.groupID != 123 {
		t.Fatalf("sent group ID = %d, want 123", sender.groupID)
	}
	if sender.text != ai.DisabledAnswer {
		t.Fatalf("sent text = %q, want %q", sender.text, ai.DisabledAnswer)
	}
}

func TestTargetAtUsersExcludesSelf(t *testing.T) {
	msg := GroupMessage{
		SelfID: 999,
		AtUsers: []int64{
			999,
			456,
		},
	}

	targets := targetAtUsers(msg)

	if len(targets) != 1 || targets[0] != 456 {
		t.Fatalf("targetAtUsers = %v, want [456]", targets)
	}
}

func TestGroupCommandRouterRejectsRestartWhenNotAuthorized(t *testing.T) {
	sender := &recordingModerator{}
	router := NewGroupCommandRouter(Options{
		Admin: commands.NewAdminHandler(commands.NewMemoryAdminStore()),
	})

	handled, err := router.Handle(context.Background(), GroupMessage{
		GroupID: 123,
		UserID:  456,
		SelfID:  999,
		Text:    "/admin restart",
		AtUsers: []int64{999},
	}, sender)

	if err != nil {
		t.Fatalf("Handle returned error: %v", err)
	}
	if !handled {
		t.Fatal("Handle did not handle unauthorized /admin restart")
	}
	if sender.restarted {
		t.Fatal("restart was requested for unauthorized user")
	}
	if sender.text != "~你好像没有权限执行该项操作耶~" {
		t.Fatalf("sent text = %q", sender.text)
	}
}

func TestGroupCommandRouterAllowsRestartForOwner(t *testing.T) {
	sender := &recordingModerator{}
	router := NewGroupCommandRouter(Options{
		Admin: commands.NewAdminHandler(commands.NewMemoryAdminStore()),
	})

	handled, err := router.Handle(context.Background(), GroupMessage{
		GroupID: 123,
		UserID:  456,
		SelfID:  999,
		IsOwner: true,
		Text:    "/admin restart",
		AtUsers: []int64{999},
	}, sender)

	if err != nil {
		t.Fatalf("Handle returned error: %v", err)
	}
	if !handled {
		t.Fatal("Handle did not handle owner /admin restart")
	}
	if !sender.restarted {
		t.Fatal("restart was not requested for owner")
	}
	if sender.text != "已请求重启 NapCat" {
		t.Fatalf("sent text = %q", sender.text)
	}
}

func TestGroupCommandRouterRejectsBanWhenNotAuthorized(t *testing.T) {
	sender := &recordingModerator{}
	router := NewGroupCommandRouter(Options{
		Admin: commands.NewAdminHandler(commands.NewMemoryAdminStore()),
	})

	handled, err := router.Handle(context.Background(), GroupMessage{
		GroupID: 123,
		UserID:  456,
		SelfID:  999,
		Text:    "/admin ban 60",
		AtUsers: []int64{999, 321},
	}, sender)

	if err != nil {
		t.Fatalf("Handle returned error: %v", err)
	}
	if !handled {
		t.Fatal("Handle did not handle unauthorized /admin ban")
	}
	if sender.bannedUserID != 0 {
		t.Fatalf("ban was requested for unauthorized user: %d", sender.bannedUserID)
	}
	if sender.text != "~你好像没有权限执行该项操作耶~" {
		t.Fatalf("sent text = %q", sender.text)
	}
}

type countingAdminStore struct {
	*commands.MemoryAdminStore
	isAdminCalls int
}

func (s *countingAdminStore) IsAdmin(ctx context.Context, userID int64) (bool, error) {
	s.isAdminCalls++
	return s.MemoryAdminStore.IsAdmin(ctx, userID)
}

func TestGroupCommandRouterChecksPermissionOnceForRegularAdminCommand(t *testing.T) {
	sender := &recordingModerator{}
	store := &countingAdminStore{MemoryAdminStore: commands.NewMemoryAdminStore()}
	if err := store.AddAdmin(context.Background(), 456); err != nil {
		t.Fatalf("AddAdmin returned error: %v", err)
	}
	router := NewGroupCommandRouter(Options{
		Admin: commands.NewAdminHandler(store),
	})

	handled, err := router.Handle(context.Background(), GroupMessage{
		GroupID: 123,
		UserID:  456,
		SelfID:  999,
		Text:    "/admin 所有管理员",
		AtUsers: []int64{999},
	}, sender)

	if err != nil {
		t.Fatalf("Handle returned error: %v", err)
	}
	if !handled {
		t.Fatal("Handle did not handle /admin 所有管理员")
	}
	if store.isAdminCalls != 1 {
		t.Fatalf("IsAdmin calls = %d, want 1", store.isAdminCalls)
	}
	if sender.text != "当前管理员：456" {
		t.Fatalf("sent text = %q", sender.text)
	}
}
