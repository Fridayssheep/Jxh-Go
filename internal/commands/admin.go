package commands

import (
	"context"
	"fmt"
	"strconv"
	"strings"
)

const (
	GroupRoleOwner  = "owner"
	GroupRoleAdmin  = "admin"
	GroupRoleMember = "member"
)

type AdminInput struct {
	Text string
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
	store SchedulerStore
}

func NewAdminHandler(store SchedulerStore) *AdminHandler {
	return &AdminHandler{store: store}
}

func (h *AdminHandler) Execute(ctx context.Context, input AdminInput) (string, error) {
	if h == nil || h.store == nil {
		return "定时任务存储未初始化", nil
	}
	text := strings.TrimSpace(input.Text)
	switch {
	case text == "定时任务 查看":
		jobs, err := h.store.ListScheduledJobs(ctx)
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
		parts := strings.SplitN(strings.TrimPrefix(text, "定时任务 添加 "), " ", 4)
		if len(parts) < 4 {
			return "格式：/admin 定时任务 添加 <每天|单次> <时间> <群聊ID> <消息内容>", nil
		}
		groupID, err := strconv.ParseInt(parts[2], 10, 64)
		if err != nil {
			return "群聊ID格式不正确", nil
		}
		id, err := h.store.AddScheduledJob(ctx, ScheduledJobInput{Type: parts[0], TimeHHMM: parts[1], GroupID: groupID, Message: parts[3]})
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("已添加定时任务 %d", id), nil
	case strings.HasPrefix(text, "定时任务 移除 "):
		id, err := strconv.ParseUint(strings.TrimSpace(strings.TrimPrefix(text, "定时任务 移除 ")), 10, 64)
		if err != nil {
			return "任务编号格式不正确", nil
		}
		return "已移除定时任务", h.store.RemoveScheduledJob(ctx, id)
	default:
		return "未知管理命令", nil
	}
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
