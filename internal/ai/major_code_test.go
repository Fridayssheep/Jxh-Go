package ai

import (
	"testing"

	"github.com/zjutjh/jxh-go/internal/groups/joinrequests"
)

func TestParseMajorCodeJudgement(t *testing.T) {
	result, err := parseMajorCodeJudgement(`{"decision":"match","confidence":"high","reason":"聚合样本一致。"}`)
	if err != nil {
		t.Fatal(err)
	}
	if result.Decision != joinrequests.MajorCodeMatch || result.Confidence != joinrequests.ConfidenceHigh {
		t.Fatalf("unexpected judgement: %+v", result)
	}
	if _, err := parseMajorCodeJudgement(`{"decision":"match","confidence":"high","reason":"ok","extra":true}`); err == nil {
		t.Fatal("expected unknown field to fail")
	}
	if _, err := parseMajorCodeJudgement(`{"decision":"approve","confidence":"high","reason":"ok"}`); err == nil {
		t.Fatal("expected invalid decision to fail")
	}
}
