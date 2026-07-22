package commands

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/zjutjh/jxh-go/internal/scheduler"
)

const (
	GroupRoleOwner  = "owner"
	GroupRoleAdmin  = "admin"
	GroupRoleMember = "member"

	schedFmtHelp = "格式：/admin 定时任务 添加 <每天|单次> <时间> <群聊ID> <消息内容>\n每天任务时间：HH:MM\n单次任务时间：YYYY-MM-DD HH:MM"
)

type SchedulerStore interface {
	ListScheduledJobs(ctx context.Context) ([]ScheduledJobView, error)
	AddScheduledJob(ctx context.Context, job ScheduledJobInput) (uint64, error)
	RemoveScheduledJob(ctx context.Context, id uint64) (bool, error)
}

type ScheduledJobInput struct {
	Type      string
	TimeHHMM  string
	RunDate   *time.Time
	GroupID   int64
	Message   string
	CreatedAt time.Time
}

type ScheduledJobView struct {
	ID       uint64
	Type     string
	TimeHHMM string
	RunDate  *time.Time
	GroupID  int64
	Message  string
}

type AdminHandler struct {
	store SchedulerStore
	now   func() time.Time
}

func NewAdminHandler(store SchedulerStore, now func() time.Time) *AdminHandler {
	if now == nil {
		now = time.Now
	}
	return &AdminHandler{store: store, now: now}
}

func (h *AdminHandler) Execute(ctx context.Context, input string) (string, error) {
	if h == nil || h.store == nil {
		return "定时任务存储未初始化", nil
	}
	// Normalize full-width spaces to half-width for user convenience
	text := strings.ReplaceAll(strings.TrimSpace(input), "　", " ")
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
			scheduledAt := job.TimeHHMM
			if job.RunDate != nil {
				scheduledAt = job.RunDate.Format("2006-01-02") + " " + scheduledAt
			}
			lines = append(lines, fmt.Sprintf("%d. %s %s 群:%d %s", job.ID, job.Type, scheduledAt, job.GroupID, job.Message))
		}
		return strings.Join(lines, "\n"), nil
	case strings.HasPrefix(text, "定时任务 添加 "):
		rest := strings.TrimPrefix(text, "定时任务 添加 ")
		// First split: get job type
		typeAndRest := strings.SplitN(rest, " ", 2)
		if len(typeAndRest) < 2 {
			return schedFmtHelp, nil
		}
		jobType := typeAndRest[0]
		if jobType != scheduler.JobTypeDaily && jobType != scheduler.JobTypeOnce {
			return "任务类型只能是每天或单次", nil
		}
		var runDate *time.Time
		var timeHHMM string
		var afterTime string
		now := h.now()
		if jobType == scheduler.JobTypeOnce {
			dateTimeSplit := strings.SplitN(typeAndRest[1], " ", 3)
			if len(dateTimeSplit) < 3 {
				return "单次任务格式：/admin 定时任务 添加 单次 YYYY-MM-DD HH:MM <群聊ID> <消息内容>", nil
			}
			runAt, err := time.ParseInLocation("2006-01-02 15:04", dateTimeSplit[0]+" "+dateTimeSplit[1], now.Location())
			if err != nil {
				return "日期时间格式不正确，请使用 YYYY-MM-DD HH:MM", nil
			}
			if runAt.Before(now.Truncate(time.Minute)) {
				return "单次任务时间不能早于当前时间", nil
			}
			runDate = &runAt
			timeHHMM = dateTimeSplit[1]
			afterTime = dateTimeSplit[2]
		} else {
			timeAndRest := strings.SplitN(typeAndRest[1], " ", 2)
			if len(timeAndRest) < 2 {
				return schedFmtHelp, nil
			}
			if _, err := time.Parse("15:04", timeAndRest[0]); err != nil {
				return "时间格式不正确，请使用 HH:MM", nil
			}
			timeHHMM = timeAndRest[0]
			afterTime = timeAndRest[1]
		}
		groupAndMsg := strings.SplitN(afterTime, " ", 2)
		if len(groupAndMsg) < 2 {
			return schedFmtHelp, nil
		}
		groupID, err := strconv.ParseInt(groupAndMsg[0], 10, 64)
		if err != nil || groupID <= 0 {
			return "群聊ID格式不正确", nil
		}
		messageText := strings.TrimSpace(groupAndMsg[1])
		if messageText == "" {
			return "消息内容不能为空", nil
		}
		id, err := h.store.AddScheduledJob(ctx, ScheduledJobInput{
			Type:      jobType,
			TimeHHMM:  timeHHMM,
			RunDate:   runDate,
			GroupID:   groupID,
			Message:   messageText,
			CreatedAt: now,
		})
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("已添加定时任务 %d", id), nil
	case strings.HasPrefix(text, "定时任务 移除 "):
		id, err := strconv.ParseUint(strings.TrimSpace(strings.TrimPrefix(text, "定时任务 移除 ")), 10, 64)
		if err != nil {
			return "任务编号格式不正确", nil
		}
		removed, err := h.store.RemoveScheduledJob(ctx, id)
		if err != nil {
			return "", err
		}
		if !removed {
			return "未找到该定时任务", nil
		}
		return "已移除定时任务", nil
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
