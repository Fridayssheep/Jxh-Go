package settings

import (
	"time"

	"github.com/zjutjh/jxh-go/internal/audit"
	"github.com/zjutjh/jxh-go/internal/auth"
)

type FeatureKey string

const (
	FeatureKeywordReply  FeatureKey = "keyword_reply"
	FeatureAIQA          FeatureKey = "ai_qa"
	FeatureQuote         FeatureKey = "quote"
	FeatureLinkCleaner   FeatureKey = "link_cleaner"
	FeatureWelcome       FeatureKey = "welcome"
	FeatureCustomCommand FeatureKey = "custom_commands"
)

const DefaultWelcomeTemplate = "欢迎来到浙江工业大学，精弘网络欢迎各位的到来！\n输入 菜单 获取精小弘机器人的菜单哦！\n请及时修改群名片\n格式如下：专业/大类+姓名"

type Basic struct {
	Enabled bool
}

type Welcome struct {
	Enabled         bool
	MessageTemplate string
}

type Features struct {
	KeywordReply  Basic
	AIQA          Basic
	Quote         Basic
	LinkCleaner   Basic
	Welcome       Welcome
	CustomCommand Basic
}

type BasicPatch struct {
	Enabled auth.Field[bool]
}

type WelcomePatch struct {
	Enabled         auth.Field[bool]
	MessageTemplate auth.Field[string]
}

type GlobalPatch struct {
	KeywordReply  auth.Field[BasicPatch]
	AIQA          auth.Field[BasicPatch]
	Quote         auth.Field[BasicPatch]
	LinkCleaner   auth.Field[BasicPatch]
	Welcome       auth.Field[WelcomePatch]
	CustomCommand auth.Field[BasicPatch]
}

type BasicOverride struct {
	Enabled bool
}

type WelcomeOverride struct {
	Enabled         *bool
	MessageTemplate *string
}

type Overrides struct {
	KeywordReply  *BasicOverride
	AIQA          *BasicOverride
	Quote         *BasicOverride
	LinkCleaner   *BasicOverride
	Welcome       *WelcomeOverride
	CustomCommand *BasicOverride
}

type OverrideFeaturePatch struct {
	Set             bool
	Clear           bool
	Enabled         auth.Field[*bool]
	MessageTemplate auth.Field[*string]
}

type GroupPatch struct {
	KeywordReply  OverrideFeaturePatch
	AIQA          OverrideFeaturePatch
	Quote         OverrideFeaturePatch
	LinkCleaner   OverrideFeaturePatch
	Welcome       OverrideFeaturePatch
	CustomCommand OverrideFeaturePatch
}

type Global struct {
	Features  Features
	Version   uint64
	UpdatedAt time.Time
	UpdatedBy *audit.Actor
}

type Group struct {
	GroupID       string
	Effective     Features
	Overrides     Overrides
	GlobalVersion uint64
	Version       uint64
	UpdatedAt     *time.Time
	UpdatedBy     *audit.Actor
}

type MutationContext struct {
	Actor      auth.Principal
	Request    auth.MutationContext
	OccurredAt time.Time
}

type UpdateGlobalMutation struct {
	Context          MutationContext
	ExpectedRevision uint64
	Patch            GlobalPatch
}

type UpdateGroupMutation struct {
	Context          MutationContext
	GroupID          string
	ExpectedRevision uint64
	Patch            GroupPatch
}

type DeleteGroupMutation struct {
	Context          MutationContext
	GroupID          string
	ExpectedRevision uint64
}

type RuntimeGroup struct {
	GroupID   string
	Overrides Overrides
	Version   uint64
}

type RuntimeState struct {
	Global Global
	Groups []RuntimeGroup
}

func DefaultFeatures() Features {
	return Features{
		KeywordReply: Basic{Enabled: true}, AIQA: Basic{Enabled: true}, Quote: Basic{Enabled: true},
		LinkCleaner: Basic{Enabled: true}, Welcome: Welcome{Enabled: true, MessageTemplate: DefaultWelcomeTemplate},
		CustomCommand: Basic{Enabled: true},
	}
}
