package ai

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

const applicantPrompt = `你负责从 QQ 群申请验证信息中提取学生资料的姓名、学号和专业等信息。
只输出一个 JSON 对象，字段固定为 student_id、student_name、major。
字段值必须逐字来自原文；无法确认时使用空字符串。不得推断、补全或改写专业名称。
不要输出 Markdown、解释或额外字段。`

type ApplicantFields struct {
	StudentID   string `json:"student_id"`
	StudentName string `json:"student_name"`
	Major       string `json:"major"`
}

type ApplicantExtractor struct {
	model   model.ToolCallingChatModel
	timeout time.Duration
}

func NewApplicantExtractor(chatModel model.ToolCallingChatModel, timeout time.Duration) *ApplicantExtractor {
	if chatModel == nil {
		return nil
	}
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	return &ApplicantExtractor{model: chatModel, timeout: timeout}
}

func (e *ApplicantExtractor) Extract(ctx context.Context, comment string) (ApplicantFields, error) {
	if e == nil || e.model == nil {
		return ApplicantFields{}, fmt.Errorf("applicant extractor is not initialized")
	}
	comment = strings.TrimSpace(comment)
	if comment == "" {
		return ApplicantFields{}, nil
	}
	ctx, cancel := context.WithTimeout(ctx, e.timeout)
	defer cancel()
	response, err := e.model.Generate(ctx, []*schema.Message{
		schema.SystemMessage(applicantPrompt),
		schema.UserMessage(comment),
	})
	if err != nil {
		return ApplicantFields{}, fmt.Errorf("extract applicant fields: %w", err)
	}
	if response == nil {
		return ApplicantFields{}, fmt.Errorf("extract applicant fields: model returned no message")
	}
	return parseApplicantResponse(comment, response.Content)
}

func parseApplicantResponse(comment, content string) (ApplicantFields, error) {
	content = strings.TrimSpace(content)
	content = strings.TrimPrefix(content, "```json")
	content = strings.TrimPrefix(content, "```")
	content = strings.TrimSuffix(strings.TrimSpace(content), "```")
	start := strings.IndexByte(content, '{')
	end := strings.LastIndexByte(content, '}')
	if start < 0 || end < start {
		return ApplicantFields{}, fmt.Errorf("extract applicant fields: model returned invalid JSON")
	}
	var fields ApplicantFields
	if err := json.Unmarshal([]byte(content[start:end+1]), &fields); err != nil {
		return ApplicantFields{}, fmt.Errorf("extract applicant fields: decode model JSON: %w", err)
	}
	fields.StudentID = strings.TrimSpace(fields.StudentID)
	fields.StudentName = strings.TrimSpace(fields.StudentName)
	fields.Major = strings.TrimSpace(fields.Major)
	if err := validateApplicantField(comment, "student_id", fields.StudentID, 32); err != nil {
		return ApplicantFields{}, err
	}
	if fields.StudentID != "" {
		for _, char := range fields.StudentID {
			if char < '0' || char > '9' {
				return ApplicantFields{}, fmt.Errorf("extract applicant fields: student_id is not numeric")
			}
		}
	}
	if err := validateApplicantField(comment, "student_name", fields.StudentName, 64); err != nil {
		return ApplicantFields{}, err
	}
	for _, char := range fields.StudentName {
		if char >= '0' && char <= '9' {
			return ApplicantFields{}, fmt.Errorf("extract applicant fields: student_name contains digits")
		}
	}
	if err := validateApplicantField(comment, "major", fields.Major, 128); err != nil {
		return ApplicantFields{}, err
	}
	return fields, nil
}

func validateApplicantField(comment, name, value string, maxRunes int) error {
	if value == "" {
		return nil
	}
	if utf8.RuneCountInString(value) > maxRunes {
		return fmt.Errorf("extract applicant fields: %s is too long", name)
	}
	if strings.ContainsAny(value, "\r\n\t") {
		return fmt.Errorf("extract applicant fields: %s contains control characters", name)
	}
	if !strings.Contains(comment, value) {
		return fmt.Errorf("extract applicant fields: %s is not present in source text", name)
	}
	return nil
}
