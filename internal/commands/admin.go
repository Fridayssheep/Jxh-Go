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
	RemoveScheduledJob(ctx context.Context, id uint64) error
}

type ScheduledJobInput struct {
	Type     string
	TimeHHMM string
	RunDate  *time.Time
	GroupID  int64
	Message  string
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
}

func NewAdminHandler(store SchedulerStore) *AdminHandler {
	return &AdminHandler{store: store}
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
			dateStr := ""
			if job.RunDate != nil {
				dateStr = " 日期:" + job.RunDate.Format("2006-01-02")
			}
			lines = append(lines, fmt.Sprintf("%d. %s %s%s 群:%d %s", job.ID, job.Type, job.TimeHHMM, dateStr, job.GroupID, job.Message))
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
		if jobType == scheduler.JobTypeOnce {
			// Parse date and time for single-run tasks: YYYY-MM-DD HH:MM
			dateTimeSplit := strings.SplitN(typeAndRest[1], " ", 3)
			if len(dateTimeSplit) < 3 {
				return "单次任务格式：/admin 定时任务 添加 单次 YYYY-MM-DD HH:MM <群聊ID> <消息内容>", nil
			}
			parsedDate, err := time.Parse("2006-01-02", dateTimeSplit[0])
			if err != nil {
				return "日期格式不正确，请使用 YYYY-MM-DD", nil
			}
			if _, err := time.Parse("15:04", dateTimeSplit[1]); err != nil {
				return "时间格式不正确，请使用 HH:MM", nil
			}
			runDate = &parsedDate
			timeHHMM = dateTimeSplit[1]
			afterTime = dateTimeSplit[2]
		} else {
			// Daily tasks: HH:MM <groupID> <message>
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
		// Parse group ID and message
		groupAndMsg := strings.SplitN(afterTime, " ", 2)
		if len(groupAndMsg) < 2 {
			return schedFmtHelp, nil
		}
		groupID, err := strconv.ParseInt(groupAndMsg[0], 10, 64)
		if err != nil || groupID <= 0 {
			return "群聊ID格式不正确", nil
		}
		id, err := h.store.AddScheduledJob(ctx, ScheduledJobInput{
			Type:     jobType,
			TimeHHMM: timeHHMM,
			RunDate:  runDate,
			GroupID:  groupID,
			Message:  groupAndMsg[1],
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
