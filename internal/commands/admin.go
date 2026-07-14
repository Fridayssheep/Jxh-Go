package commands

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
)

type AdminInput struct {
	GroupID    int64
	ActorID    int64
	ActorRole  string
	TargetRole string
	Text       string
	AtUsers    []int64
}

const (
	GroupRoleOwner  = "owner"
	GroupRoleAdmin  = "admin"
	GroupRoleMember = "member"
)

type AdminRecord struct {
	GroupID       int64
	UserID        int64
	ManualGranted bool
	QQRole        string
}

type AdminStore interface {
	SyncAdminRole(ctx context.Context, groupID, userID int64, role string) (AdminRecord, error)
	SetManualAdmin(ctx context.Context, groupID, userID int64, granted bool, role string) error
	ClearManualAdmins(ctx context.Context, groupID int64) error
	ListAdmins(ctx context.Context, groupID int64) ([]AdminRecord, error)
	AddBlacklist(ctx context.Context, userID int64) error
	RemoveBlacklist(ctx context.Context, userID int64) error
	ClearBlacklist(ctx context.Context) error
	ListBlacklist(ctx context.Context) ([]int64, error)
}

type SchedulerStore interface {
	ListScheduledJobs(ctx context.Context) ([]ScheduledJobView, error)
	AddScheduledJob(ctx context.Context, job ScheduledJobInput) (uint64, error)
	RemoveScheduledJob(ctx context.Context, id uint64) error
}

type ScheduledJobInput struct {
	Type     string
	TimeHHMM string
	GroupID  int64
	Message  string
}

type ScheduledJobView struct {
	ID       uint64
	Type     string
	TimeHHMM string
	GroupID  int64
	Message  string
	Enabled  bool
}

type AdminHandler struct {
	store AdminStore
}

func NewAdminHandler(store AdminStore) *AdminHandler {
	return &AdminHandler{store: store}
}

func (h *AdminHandler) PermissionMessage(ctx context.Context, input AdminInput) (string, error) {
	if h.store == nil {
		return "管理员存储未初始化", nil
	}
	role, ok := NormalizeGroupRole(input.ActorRole)
	if !ok || input.GroupID <= 0 || input.ActorID <= 0 {
		return "暂时无法确认群身份，请稍后重试", nil
	}
	record, err := h.store.SyncAdminRole(ctx, input.GroupID, input.ActorID, role)
	if err != nil {
		return "", err
	}
	if !IsNativeGroupAdmin(role) && !record.ManualGranted {
		return "~你好像没有权限执行该项操作耶~", nil
	}
	return "", nil
}

func (h *AdminHandler) Handle(ctx context.Context, input AdminInput) (string, error) {
	if msg, err := h.PermissionMessage(ctx, input); msg != "" || err != nil {
		return msg, err
	}
	return h.ExecuteAuthorized(ctx, input)
}

// ExecuteAuthorized 执行已通过鉴权的管理员命令。
func (h *AdminHandler) ExecuteAuthorized(ctx context.Context, input AdminInput) (string, error) {
	text := strings.TrimSpace(input.Text)
	switch {
	case text == "添加管理员":
		userID, ok := firstAt(input.AtUsers)
		if !ok {
			return "请 @ 要添加的管理员", nil
		}
		role, ok := NormalizeGroupRole(input.TargetRole)
		if !ok {
			return "暂时无法确认该成员身份，请稍后重试", nil
		}
		if IsNativeGroupAdmin(role) {
			if _, err := h.store.SyncAdminRole(ctx, input.GroupID, userID, role); err != nil {
				return "", err
			}
			return fmt.Sprintf("该用户是%s，已经拥有当前群的 bot 操作权限", groupRoleLabel(role)), nil
		}
		return "已添加当前群手动授权管理员", h.store.SetManualAdmin(ctx, input.GroupID, userID, true, role)
	case text == "移除管理员":
		userID, ok := firstAt(input.AtUsers)
		if !ok {
			return "请 @ 要移除的管理员", nil
		}
		role, ok := NormalizeGroupRole(input.TargetRole)
		if !ok {
			return "暂时无法确认该成员身份，请稍后重试", nil
		}
		if IsNativeGroupAdmin(role) {
			if _, err := h.store.SyncAdminRole(ctx, input.GroupID, userID, role); err != nil {
				return "", err
			}
			return fmt.Sprintf("该用户是%s，权限由 QQ 群角色提供，无法移除", groupRoleLabel(role)), nil
		}
		return "已移除当前群手动授权管理员", h.store.SetManualAdmin(ctx, input.GroupID, userID, false, role)
	case text == "移除所有管理员":
		return "已移除当前群所有手动授权管理员", h.store.ClearManualAdmins(ctx, input.GroupID)
	case text == "所有管理员":
		users, err := h.store.ListAdmins(ctx, input.GroupID)
		if err != nil {
			return "", err
		}
		return formatAdminRecords(users), nil
	case text == "添加黑名单":
		userID, ok := firstAt(input.AtUsers)
		if !ok {
			return "请 @ 要添加的黑名单用户", nil
		}
		return "已添加黑名单", h.store.AddBlacklist(ctx, userID)
	case text == "移除黑名单":
		userID, ok := firstAt(input.AtUsers)
		if !ok {
			return "请 @ 要移除的黑名单用户", nil
		}
		return "已移除黑名单", h.store.RemoveBlacklist(ctx, userID)
	case text == "移除所有黑名单":
		return "已移除所有黑名单", h.store.ClearBlacklist(ctx)
	case text == "所有黑名单":
		users, err := h.store.ListBlacklist(ctx)
		if err != nil {
			return "", err
		}
		return "当前黑名单：" + joinIDs(users), nil
	case text == "定时任务 查看":
		scheduler, ok := h.store.(SchedulerStore)
		if !ok {
			return "定时任务存储未初始化", nil
		}
		jobs, err := scheduler.ListScheduledJobs(ctx)
		if err != nil {
			return "", err
		}
		if len(jobs) == 0 {
			return "~当前没有定时任务~", nil
		}
		lines := []string{"当前定时任务列表:"}
		for _, job := range jobs {
			lines = append(lines, fmt.Sprintf("%d. %s %s 群:%d %s", job.ID, job.Type, job.TimeHHMM, job.GroupID, job.Message))
		}
		return strings.Join(lines, "\n"), nil
	case strings.HasPrefix(text, "定时任务 添加 "):
		scheduler, ok := h.store.(SchedulerStore)
		if !ok {
			return "定时任务存储未初始化", nil
		}
		parts := strings.SplitN(strings.TrimPrefix(text, "定时任务 添加 "), " ", 4)
		if len(parts) < 4 {
			return "格式：/admin 定时任务 添加 <每天|单次> <时间> <群聊ID> <消息内容>", nil
		}
		groupID, err := strconv.ParseInt(parts[2], 10, 64)
		if err != nil {
			return "群聊ID格式不正确", nil
		}
		id, err := scheduler.AddScheduledJob(ctx, ScheduledJobInput{Type: parts[0], TimeHHMM: parts[1], GroupID: groupID, Message: parts[3]})
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("已添加定时任务 %d", id), nil
	case strings.HasPrefix(text, "定时任务 移除 "):
		scheduler, ok := h.store.(SchedulerStore)
		if !ok {
			return "定时任务存储未初始化", nil
		}
		id, err := strconv.ParseUint(strings.TrimSpace(strings.TrimPrefix(text, "定时任务 移除 ")), 10, 64)
		if err != nil {
			return "任务编号格式不正确", nil
		}
		return "已移除定时任务", scheduler.RemoveScheduledJob(ctx, id)
	default:
		return "未知管理命令", nil
	}
}

func firstAt(users []int64) (int64, bool) {
	if len(users) == 0 {
		return 0, false
	}
	return users[0], true
}

func joinIDs(users []int64) string {
	if len(users) == 0 {
		return "无"
	}
	sort.Slice(users, func(i, j int) bool { return users[i] < users[j] })
	parts := make([]string, len(users))
	for i, user := range users {
		parts[i] = fmt.Sprintf("%d", user)
	}
	return strings.Join(parts, "、")
}

func NormalizeGroupRole(role string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(role)) {
	case GroupRoleOwner:
		return GroupRoleOwner, true
	case GroupRoleAdmin:
		return GroupRoleAdmin, true
	case GroupRoleMember:
		return GroupRoleMember, true
	default:
		return "", false
	}
}

func IsNativeGroupAdmin(role string) bool {
	return role == GroupRoleOwner || role == GroupRoleAdmin
}

func groupRoleLabel(role string) string {
	if role == GroupRoleOwner {
		return "QQ群主"
	}
	return "QQ群管理员"
}

func formatAdminRecords(records []AdminRecord) string {
	if len(records) == 0 {
		return "当前群管理员：无"
	}
	sort.Slice(records, func(i, j int) bool { return records[i].UserID < records[j].UserID })
	parts := make([]string, 0, len(records))
	for _, record := range records {
		var sources []string
		if IsNativeGroupAdmin(record.QQRole) {
			sources = append(sources, groupRoleLabel(record.QQRole))
		}
		if record.ManualGranted {
			sources = append(sources, "手动授权")
		}
		if len(sources) > 0 {
			parts = append(parts, fmt.Sprintf("%d（%s）", record.UserID, strings.Join(sources, "、")))
		}
	}
	if len(parts) == 0 {
		return "当前群管理员：无"
	}
	return "当前群管理员：" + strings.Join(parts, "\n")
}

type MemoryAdminStore struct {
	mu        sync.Mutex
	admins    map[adminKey]AdminRecord
	blacklist map[int64]struct{}
}

type adminKey struct {
	groupID int64
	userID  int64
}

func NewMemoryAdminStore() *MemoryAdminStore {
	return &MemoryAdminStore{admins: map[adminKey]AdminRecord{}, blacklist: map[int64]struct{}{}}
}

func (s *MemoryAdminStore) SyncAdminRole(ctx context.Context, groupID, userID int64, role string) (AdminRecord, error) {
	_ = ctx
	s.mu.Lock()
	defer s.mu.Unlock()
	key := adminKey{groupID: groupID, userID: userID}
	record := s.admins[key]
	record.GroupID = groupID
	record.UserID = userID
	record.QQRole = role
	if role == GroupRoleMember && !record.ManualGranted {
		delete(s.admins, key)
		return record, nil
	}
	s.admins[key] = record
	return record, nil
}

func (s *MemoryAdminStore) SetManualAdmin(ctx context.Context, groupID, userID int64, granted bool, role string) error {
	_ = ctx
	s.mu.Lock()
	defer s.mu.Unlock()
	key := adminKey{groupID: groupID, userID: userID}
	record := s.admins[key]
	record.GroupID = groupID
	record.UserID = userID
	record.ManualGranted = granted
	record.QQRole = role
	if role == GroupRoleMember && !granted {
		delete(s.admins, key)
		return nil
	}
	s.admins[key] = record
	return nil
}

func (s *MemoryAdminStore) ClearManualAdmins(ctx context.Context, groupID int64) error {
	_ = ctx
	s.mu.Lock()
	defer s.mu.Unlock()
	for key, record := range s.admins {
		if key.groupID != groupID {
			continue
		}
		record.ManualGranted = false
		if !IsNativeGroupAdmin(record.QQRole) {
			delete(s.admins, key)
			continue
		}
		s.admins[key] = record
	}
	return nil
}

func (s *MemoryAdminStore) ListAdmins(ctx context.Context, groupID int64) ([]AdminRecord, error) {
	_ = ctx
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]AdminRecord, 0, len(s.admins))
	for key, record := range s.admins {
		if key.groupID == groupID && (record.ManualGranted || IsNativeGroupAdmin(record.QQRole)) {
			out = append(out, record)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].UserID < out[j].UserID })
	return out, nil
}

func (s *MemoryAdminStore) AddBlacklist(ctx context.Context, userID int64) error {
	_ = ctx
	s.mu.Lock()
	defer s.mu.Unlock()
	s.blacklist[userID] = struct{}{}
	return nil
}

func (s *MemoryAdminStore) RemoveBlacklist(ctx context.Context, userID int64) error {
	_ = ctx
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.blacklist, userID)
	return nil
}

func (s *MemoryAdminStore) ClearBlacklist(ctx context.Context) error {
	_ = ctx
	s.mu.Lock()
	defer s.mu.Unlock()
	s.blacklist = map[int64]struct{}{}
	return nil
}

func (s *MemoryAdminStore) ListBlacklist(ctx context.Context) ([]int64, error) {
	_ = ctx
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]int64, 0, len(s.blacklist))
	for user := range s.blacklist {
		out = append(out, user)
	}
	return out, nil
}

func (s *MemoryAdminStore) ListScheduledJobs(ctx context.Context) ([]ScheduledJobView, error) {
	_ = ctx
	return nil, nil
}

func (s *MemoryAdminStore) AddScheduledJob(ctx context.Context, job ScheduledJobInput) (uint64, error) {
	_ = ctx
	_ = job
	return 1, nil
}

func (s *MemoryAdminStore) RemoveScheduledJob(ctx context.Context, id uint64) error {
	_ = ctx
	_ = id
	return nil
}
