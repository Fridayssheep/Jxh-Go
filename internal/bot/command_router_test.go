package bot

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zjutjh/jxh-go/internal/ai"
	"github.com/zjutjh/jxh-go/internal/cache"
	"github.com/zjutjh/jxh-go/internal/commands"
	"github.com/zjutjh/jxh-go/internal/grouprequest"
	"github.com/zjutjh/jxh-go/internal/knowledge"
	"github.com/zjutjh/jxh-go/internal/quote"
	"github.com/zjutjh/jxh-go/internal/triggerstats"
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
	banErr       error
	restarted    bool
}

type quoteCommandSender struct {
	recordingSender
	messages []QuotedMessage
	count    int
}

func (s *quoteCommandSender) GetQuoteMessages(_ context.Context, _, _ int64, count int) ([]QuotedMessage, error) {
	s.count = count
	return s.messages, nil
}

type recordingQuoteGenerator struct {
	payload quote.Payload
}

func (q *recordingQuoteGenerator) Generate(_ context.Context, payload quote.Payload) (string, error) {
	q.payload = payload
	return "R0lGODlh", nil
}

type botGroupRequestStore struct {
	records []grouprequest.Record
}

func (s *botGroupRequestStore) UpsertGroupJoinRequest(ctx context.Context, record grouprequest.Record) error {
	_ = ctx
	s.records = append(s.records, record)
	return nil
}

func (s *botGroupRequestStore) ListGroupJoinRequests(ctx context.Context, limit int) ([]grouprequest.Record, error) {
	_ = ctx
	records := append([]grouprequest.Record(nil), s.records...)
	if limit > 0 && len(records) > limit {
		records = records[:limit]
	}
	return append([]grouprequest.Record(nil), records...), nil
}

type groupRequestSyncSender struct {
	recordingSender
	count    int
	requests []grouprequest.Record
}

func (s *groupRequestSyncSender) FetchGroupJoinRequests(ctx context.Context, count int) ([]grouprequest.Record, error) {
	_ = ctx
	s.count = count
	return append([]grouprequest.Record(nil), s.requests...), nil
}

func (s *recordingModerator) SetGroupBan(ctx context.Context, groupID, userID int64, duration time.Duration) error {
	_ = ctx
	s.bannedGroup = groupID
	s.bannedUserID = userID
	s.banDuration = int64(duration.Seconds())
	return s.banErr
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

func TestPipelineShowsHelpWhenOnlyMentioningBot(t *testing.T) {
	sender := &recordingSender{}
	pipeline := NewPipeline(Options{Sender: sender})

	err := pipeline.HandleGroupMessage(context.Background(), GroupMessage{
		GroupID: 123,
		UserID:  456,
		SelfID:  999,
		AtUsers: []int64{999},
	})

	if err != nil {
		t.Fatalf("HandleGroupMessage returned error: %v", err)
	}
	for _, want := range []string{"/test", "/reload", "/ai <问题>", "/q", "/admin"} {
		if !strings.Contains(sender.text, want) {
			t.Fatalf("bot help %q does not contain %q", sender.text, want)
		}
	}
}

func TestPipelineIgnoresEmptyMessageWithoutMention(t *testing.T) {
	sender := &recordingSender{}
	pipeline := NewPipeline(Options{Sender: sender})

	if err := pipeline.HandleGroupMessage(context.Background(), GroupMessage{GroupID: 123, UserID: 456, SelfID: 999}); err != nil {
		t.Fatalf("HandleGroupMessage returned error: %v", err)
	}
	if sender.text != "" {
		t.Fatalf("sent text = %q, want no response", sender.text)
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

func TestGroupCommandRouterRecordsAIRetrievalTriggers(t *testing.T) {
	sender := &recordingSender{}
	statsStore := &recordingTriggerStats{}
	chat := &ai.StaticChat{Response: "交通说明"}
	router := NewGroupCommandRouter(Options{
		AI: ai.NewService(ai.Options{
			Retriever: ai.StaticRetriever{Documents: []ai.Document{{
				ID:      "traffic",
				Content: "知识正文：交通说明",
				Metadata: map[string]string{
					"keyword": "交通",
				},
				Score: 0.9,
			}}},
			Chat: chat,
		}),
		TriggerStats: triggerstats.NewService(statsStore, triggerstats.Options{}),
	})

	handled, err := router.Handle(context.Background(), GroupMessage{
		GroupID:   123,
		UserID:    456,
		MessageID: 789,
		SelfID:    999,
		Text:      "/ai 交通怎么走",
		AtUsers:   []int64{999},
	}, sender)

	if err != nil {
		t.Fatalf("Handle returned error: %v", err)
	}
	if !handled {
		t.Fatal("Handle did not handle /ai")
	}
	if sender.text != "交通说明" {
		t.Fatalf("sent text = %q", sender.text)
	}
	if len(statsStore.events) != 1 {
		t.Fatalf("stats events = %d, want 1", len(statsStore.events))
	}
	event := statsStore.events[0]
	if event.TriggerType != triggerstats.TriggerTypeAIRetrieval || event.SourceKey != "traffic" || event.Keyword != "交通" {
		t.Fatalf("recorded event = %+v", event)
	}
}

func TestGroupCommandRouterQuotesMultipleMessages(t *testing.T) {
	sender := &quoteCommandSender{messages: []QuotedMessage{
		{MessageID: 10, UserID: 1, Nickname: "one", RawMessage: "first"},
		{MessageID: 11, UserID: 2, Nickname: "two", RawMessage: "second"},
		{MessageID: 99, UserID: 3, Nickname: "bot caller", RawMessage: "/q 3"},
	}}
	generator := &recordingQuoteGenerator{}
	router := NewGroupCommandRouter(Options{Quote: generator})

	handled, err := router.Handle(context.Background(), GroupMessage{
		GroupID: 123, SelfID: 999, MessageID: 99, ReplyMessageID: 10,
		Text: "/q 3", AtUsers: []int64{999},
	}, sender)

	if err != nil || !handled {
		t.Fatalf("Handle = (%v, %v), want handled without error", handled, err)
	}
	if sender.count != 3 || len(generator.payload) != 2 {
		t.Fatalf("count = %d, payload = %#v", sender.count, generator.payload)
	}
}

func TestParseQuoteCount(t *testing.T) {
	for input, want := range map[string]int{"/q": 1, "/q 3": 3, "/q 10": 10} {
		got, err := parseQuoteCount(input)
		if err != nil || got != want {
			t.Fatalf("parseQuoteCount(%q) = (%d, %v), want %d", input, got, err, want)
		}
	}
	for _, input := range []string{"/q 0", "/q 11", "/q x", "/q 2 extra"} {
		if _, err := parseQuoteCount(input); err == nil {
			t.Fatalf("parseQuoteCount(%q) succeeded", input)
		}
	}
}

func TestGroupCommandRouterShowsAdminHelpWithoutPermission(t *testing.T) {
	sender := &recordingSender{}
	router := NewGroupCommandRouter(Options{
		Admin: commands.NewAdminHandler(commands.NewMemoryAdminStore()),
	})

	handled, err := router.Handle(context.Background(), GroupMessage{
		GroupID: 123,
		UserID:  456,
		SelfID:  999,
		Text:    "/admin",
		AtUsers: []int64{999},
	}, sender)

	if err != nil {
		t.Fatalf("Handle returned error: %v", err)
	}
	if !handled {
		t.Fatal("Handle did not handle bare /admin")
	}
	for _, want := range []string{"群主", "管理员", "/admin ban", "/admin 群申请", "/admin 词条统计", "不能禁言群主、群管理员或机器人自己"} {
		if !strings.Contains(sender.text, want) {
			t.Fatalf("admin help %q does not contain %q", sender.text, want)
		}
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

func TestGroupCommandRouterExplainsNapCatBanRestrictionsOnError(t *testing.T) {
	sender := &recordingModerator{banErr: errors.New("cannot ban admin")}
	router := NewGroupCommandRouter(Options{
		Admin: commands.NewAdminHandler(commands.NewMemoryAdminStore()),
	})

	handled, err := router.Handle(context.Background(), GroupMessage{
		GroupID: 123,
		UserID:  456,
		SelfID:  999,
		IsOwner: true,
		Text:    "/admin ban 60",
		AtUsers: []int64{999, 321},
	}, sender)

	if err != nil {
		t.Fatalf("Handle returned error: %v", err)
	}
	if !handled {
		t.Fatal("Handle did not handle failed /admin ban")
	}
	if sender.bannedUserID != 321 {
		t.Fatalf("ban target = %d, want 321", sender.bannedUserID)
	}
	if !strings.Contains(sender.text, "cannot ban admin") || !strings.Contains(sender.text, "不能禁言群主、群管理员或机器人自己") {
		t.Fatalf("sent text = %q", sender.text)
	}
}

func TestGroupCommandRouterExportsGroupRequestsForOwner(t *testing.T) {
	sender := &recordingModerator{}
	exportDir := t.TempDir()
	store := &botGroupRequestStore{records: []grouprequest.Record{{
		ID:         1,
		RequestKey: "flag-1",
		Flag:       "flag-1",
		GroupID:    123,
		UserID:     456,
		Comment:    "申请信息",
		Status:     grouprequest.StatusPending,
		Source:     grouprequest.SourceEvent,
	}}}
	router := NewGroupCommandRouter(Options{
		Admin: commands.NewAdminHandler(commands.NewMemoryAdminStore()),
		GroupRequests: grouprequest.NewService(store, grouprequest.Options{
			ExportDir: exportDir,
			Now:       func() time.Time { return time.Date(2026, 7, 10, 20, 30, 0, 0, time.Local) },
		}),
	})

	handled, err := router.Handle(context.Background(), GroupMessage{
		GroupID: 123,
		UserID:  456,
		SelfID:  999,
		IsOwner: true,
		Text:    "/admin 群申请 导出",
		AtUsers: []int64{999},
	}, sender)

	if err != nil {
		t.Fatalf("Handle returned error: %v", err)
	}
	if !handled {
		t.Fatal("Handle did not handle group request export")
	}
	entries, err := os.ReadDir(exportDir)
	if err != nil || len(entries) != 1 || !entries[0].IsDir() {
		t.Fatalf("export entries = %v, err %v", entries, err)
	}
	runDir := filepath.Join(exportDir, entries[0].Name())
	if _, err := os.Stat(filepath.Join(runDir, "group_123.xlsx")); err != nil {
		t.Fatalf("local group export does not exist: %v", err)
	}
	if !strings.Contains(sender.text, "已在本地导出全部群申请 1 条") || !strings.Contains(sender.text, runDir) {
		t.Fatalf("sent text = %q", sender.text)
	}
}

func TestGroupCommandRouterCreatesPersistentLocalGroupRequestExport(t *testing.T) {
	sender := &recordingModerator{}
	exportDir := t.TempDir()
	router := NewGroupCommandRouter(Options{
		Admin: commands.NewAdminHandler(commands.NewMemoryAdminStore()),
		GroupRequests: grouprequest.NewService(&botGroupRequestStore{records: []grouprequest.Record{{
			ID:         1,
			RequestKey: "flag-1",
			GroupID:    123,
			UserID:     456,
		}}}, grouprequest.Options{
			ExportDir: exportDir,
			Now:       func() time.Time { return time.Date(2026, 7, 10, 20, 30, 0, 0, time.Local) },
		}),
	})

	handled, err := router.Handle(context.Background(), GroupMessage{
		GroupID: 123,
		UserID:  456,
		SelfID:  999,
		IsOwner: true,
		Text:    "/admin 群申请 导出",
		AtUsers: []int64{999},
	}, sender)

	if err != nil {
		t.Fatalf("Handle returned error: %v", err)
	}
	if !handled {
		t.Fatal("Handle did not handle group request export")
	}
	entries, err := os.ReadDir(exportDir)
	if err != nil || len(entries) != 1 {
		t.Fatalf("export entries = %v, err %v", entries, err)
	}
	if !strings.Contains(sender.text, "已在本地导出全部群申请") || strings.Contains(sender.text, "上传") {
		t.Fatalf("sent text = %q", sender.text)
	}
}

func TestGroupCommandRouterExportsLocallyWithoutUploader(t *testing.T) {
	sender := &recordingModerator{}
	exportDir := t.TempDir()
	router := NewGroupCommandRouter(Options{
		Admin: commands.NewAdminHandler(commands.NewMemoryAdminStore()),
		GroupRequests: grouprequest.NewService(&botGroupRequestStore{records: []grouprequest.Record{{
			ID:         1,
			RequestKey: "flag-1",
			GroupID:    123,
			UserID:     456,
		}}}, grouprequest.Options{
			ExportDir: exportDir,
			Now:       func() time.Time { return time.Date(2026, 7, 10, 20, 30, 0, 0, time.Local) },
		}),
	})

	handled, err := router.Handle(context.Background(), GroupMessage{
		GroupID: 123,
		UserID:  456,
		SelfID:  999,
		IsOwner: true,
		Text:    "/admin 群申请 导出",
		AtUsers: []int64{999},
	}, sender)

	if err != nil {
		t.Fatalf("Handle returned error: %v", err)
	}
	if !handled {
		t.Fatal("Handle did not handle group request export")
	}
	entries, err := os.ReadDir(exportDir)
	if err != nil {
		t.Fatalf("read export dir: %v", err)
	}
	if len(entries) != 1 || !entries[0].IsDir() {
		t.Fatalf("export dir contains %v, want one run directory", entries)
	}
	exportPath := filepath.Join(exportDir, entries[0].Name())
	if !strings.Contains(sender.text, "已在本地导出全部群申请") || !strings.Contains(sender.text, exportPath) {
		t.Fatalf("sent text = %q", sender.text)
	}
}

func TestGroupCommandRouterExportsEveryGroupIntoSeparateFiles(t *testing.T) {
	sender := &recordingModerator{}
	exportDir := t.TempDir()
	store := &botGroupRequestStore{records: []grouprequest.Record{
		{ID: 1, RequestKey: "current", GroupID: 123, StudentID: "10000001"},
		{ID: 2, RequestKey: "other", GroupID: 456, StudentID: "20000002"},
	}}
	router := NewGroupCommandRouter(Options{
		Admin:         commands.NewAdminHandler(commands.NewMemoryAdminStore()),
		GroupRequests: grouprequest.NewService(store, grouprequest.Options{ExportDir: exportDir}),
	})

	_, err := router.Handle(context.Background(), GroupMessage{
		GroupID: 123, UserID: 789, SelfID: 999, IsOwner: true,
		Text: "/admin 群申请 导出 全部", AtUsers: []int64{999},
	}, sender)
	if err != nil {
		t.Fatalf("Handle returned error: %v", err)
	}
	entries, err := os.ReadDir(exportDir)
	if err != nil || len(entries) != 1 {
		t.Fatalf("export entries = %v, err %v", entries, err)
	}
	runDir := filepath.Join(exportDir, entries[0].Name())
	for _, groupID := range []string{"123", "456"} {
		if _, err := os.Stat(filepath.Join(runDir, "group_"+groupID+".xlsx")); err != nil {
			t.Fatalf("group %s export missing: %v", groupID, err)
		}
	}
	if !strings.Contains(sender.text, "全部群申请 2 条") || !strings.Contains(sender.text, "按 2 个群") {
		t.Fatalf("sent text = %q", sender.text)
	}
}

func TestGroupCommandRouterRejectsGroupRequestExportWhenNotAuthorized(t *testing.T) {
	sender := &recordingModerator{}
	exportDir := t.TempDir()
	router := NewGroupCommandRouter(Options{
		Admin: commands.NewAdminHandler(commands.NewMemoryAdminStore()),
		GroupRequests: grouprequest.NewService(&botGroupRequestStore{records: []grouprequest.Record{{
			ID:         1,
			RequestKey: "flag-1",
		}}}, grouprequest.Options{ExportDir: exportDir}),
	})

	handled, err := router.Handle(context.Background(), GroupMessage{
		GroupID: 123,
		UserID:  456,
		SelfID:  999,
		Text:    "/admin 群申请 导出",
		AtUsers: []int64{999},
	}, sender)

	if err != nil {
		t.Fatalf("Handle returned error: %v", err)
	}
	if !handled {
		t.Fatal("Handle did not handle unauthorized group request export")
	}
	entries, err := os.ReadDir(exportDir)
	if err != nil {
		t.Fatalf("read export dir: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("export dir contains %d files, want none", len(entries))
	}
	if sender.text != "~你好像没有权限执行该项操作耶~" {
		t.Fatalf("sent text = %q", sender.text)
	}
}

func TestGroupCommandRouterSyncsGroupRequestsForOwner(t *testing.T) {
	sender := &groupRequestSyncSender{requests: []grouprequest.Record{{
		RequestKey: "flag-1",
		Flag:       "flag-1",
		GroupID:    123,
		UserID:     456,
		Comment:    "申请信息",
	}}}
	store := &botGroupRequestStore{}
	router := NewGroupCommandRouter(Options{
		Admin:         commands.NewAdminHandler(commands.NewMemoryAdminStore()),
		GroupRequests: grouprequest.NewService(store, grouprequest.Options{}),
	})

	handled, err := router.Handle(context.Background(), GroupMessage{
		GroupID: 123,
		UserID:  456,
		SelfID:  999,
		IsOwner: true,
		Text:    "/admin 群申请 同步 3",
		AtUsers: []int64{999},
	}, sender)

	if err != nil {
		t.Fatalf("Handle returned error: %v", err)
	}
	if !handled {
		t.Fatal("Handle did not handle group request sync")
	}
	if sender.count != 3 {
		t.Fatalf("fetch count = %d, want 3", sender.count)
	}
	if len(store.records) != 1 || store.records[0].Flag != "flag-1" {
		t.Fatalf("stored records = %+v", store.records)
	}
	if sender.text != "已同步群申请 1 条" {
		t.Fatalf("sent text = %q", sender.text)
	}
}

func TestGroupCommandRouterSyncsGroupRequestsWithDefaultCount(t *testing.T) {
	sender := &groupRequestSyncSender{}
	router := NewGroupCommandRouter(Options{
		Admin:         commands.NewAdminHandler(commands.NewMemoryAdminStore()),
		GroupRequests: grouprequest.NewService(&botGroupRequestStore{}, grouprequest.Options{}),
	})

	handled, err := router.Handle(context.Background(), GroupMessage{
		GroupID: 123,
		UserID:  456,
		SelfID:  999,
		IsOwner: true,
		Text:    "/admin 群申请 同步",
		AtUsers: []int64{999},
	}, sender)

	if err != nil {
		t.Fatalf("Handle returned error: %v", err)
	}
	if !handled {
		t.Fatal("Handle did not handle group request sync")
	}
	if sender.count != 20 {
		t.Fatalf("fetch count = %d, want 20", sender.count)
	}
}

func TestGroupCommandRouterRejectsGroupRequestSyncWhenNotAuthorized(t *testing.T) {
	sender := &groupRequestSyncSender{requests: []grouprequest.Record{{Flag: "flag-1"}}}
	store := &botGroupRequestStore{}
	router := NewGroupCommandRouter(Options{
		Admin:         commands.NewAdminHandler(commands.NewMemoryAdminStore()),
		GroupRequests: grouprequest.NewService(store, grouprequest.Options{}),
	})

	handled, err := router.Handle(context.Background(), GroupMessage{
		GroupID: 123,
		UserID:  456,
		SelfID:  999,
		Text:    "/admin 群申请 同步 3",
		AtUsers: []int64{999},
	}, sender)

	if err != nil {
		t.Fatalf("Handle returned error: %v", err)
	}
	if !handled {
		t.Fatal("Handle did not handle unauthorized group request sync")
	}
	if sender.count != 0 {
		t.Fatalf("fetch count = %d, want no fetch", sender.count)
	}
	if len(store.records) != 0 {
		t.Fatalf("stored records = %+v, want none", store.records)
	}
	if sender.text != "~你好像没有权限执行该项操作耶~" {
		t.Fatalf("sent text = %q", sender.text)
	}
}

func TestGroupCommandRouterShowsTriggerStatsForOwner(t *testing.T) {
	sender := &recordingModerator{}
	exportDir := t.TempDir()
	statsStore := &recordingTriggerStats{summaries: []triggerstats.Summary{{
		SourceKey:     "menu",
		Keyword:       "菜单",
		TriggerType:   triggerstats.TriggerTypeKeywordReply,
		Count:         3,
		LastTriggered: time.Date(2026, 7, 10, 20, 30, 0, 0, time.Local),
	}}}
	now := time.Date(2026, 7, 10, 20, 30, 0, 0, time.FixedZone("CST", 8*60*60))
	router := NewGroupCommandRouter(Options{
		Admin:        commands.NewAdminHandler(commands.NewMemoryAdminStore()),
		TriggerStats: triggerstats.NewService(statsStore, triggerstats.Options{Now: func() time.Time { return now }, ExportDir: exportDir}),
	})

	handled, err := router.Handle(context.Background(), GroupMessage{
		GroupID: 123,
		UserID:  456,
		SelfID:  999,
		IsOwner: true,
		Text:    "/admin 词条统计 7d",
		AtUsers: []int64{999},
	}, sender)

	if err != nil {
		t.Fatalf("Handle returned error: %v", err)
	}
	if !handled {
		t.Fatal("Handle did not handle trigger stats")
	}
	entries, err := os.ReadDir(exportDir)
	if err != nil || len(entries) != 1 || entries[0].IsDir() {
		t.Fatalf("stats export entries = %v, err %v", entries, err)
	}
	exportPath := filepath.Join(exportDir, entries[0].Name())
	if !strings.Contains(sender.text, "已在本地导出全部群的词条统计 1 项") || !strings.Contains(sender.text, exportPath) {
		t.Fatalf("sent text = %q", sender.text)
	}
	wantSince := time.Date(2026, 7, 4, 0, 0, 0, 0, now.Location())
	if statsStore.since == nil || !statsStore.since.Equal(wantSince) {
		t.Fatalf("stats since = %v, want %v", statsStore.since, wantSince)
	}
	if statsStore.limit != 0 {
		t.Fatalf("stats limit = %d, want all summaries", statsStore.limit)
	}
}

func TestGroupCommandRouterReportsUnavailableTriggerStats(t *testing.T) {
	sender := &recordingModerator{}
	router := NewGroupCommandRouter(Options{
		Admin:        commands.NewAdminHandler(commands.NewMemoryAdminStore()),
		TriggerStats: triggerstats.NewService(&recordingTriggerStats{err: errors.New("redis unavailable")}, triggerstats.Options{}),
	})

	handled, err := router.Handle(context.Background(), GroupMessage{
		GroupID: 123, UserID: 456, SelfID: 999, IsOwner: true,
		Text: "/admin 词条统计 7d", AtUsers: []int64{999},
	}, sender)
	if err != nil {
		t.Fatalf("Handle returned error: %v", err)
	}
	if !handled || sender.text != "词条统计服务暂不可用" {
		t.Fatalf("handled/text = %v/%q", handled, sender.text)
	}
}

func TestGroupCommandRouterKeepsAIAnswerWhenStatsFails(t *testing.T) {
	sender := &recordingModerator{}
	router := NewGroupCommandRouter(Options{
		AI: ai.NewService(ai.Options{
			Retriever: ai.StaticRetriever{Documents: []ai.Document{{
				ID:       "doc-1",
				Content:  "答案材料",
				Metadata: map[string]string{"keyword": "菜单"},
				Score:    0.9,
			}}},
			Chat: &ai.StaticChat{Response: "AI 答案"},
		}),
		TriggerStats: triggerstats.NewService(&recordingTriggerStats{err: errors.New("stats unavailable")}, triggerstats.Options{}),
	})

	handled, err := router.Handle(context.Background(), GroupMessage{
		GroupID: 123,
		UserID:  456,
		SelfID:  999,
		Text:    "/ai 菜单",
		AtUsers: []int64{999},
	}, sender)

	if err != nil {
		t.Fatalf("Handle returned error: %v", err)
	}
	if !handled {
		t.Fatal("Handle did not handle /ai")
	}
	if sender.text != "AI 答案" {
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
