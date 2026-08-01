package joinrequests

import (
	"strings"
	"testing"
)

func TestAutomaticDecisionKeyFitsAuditRequestIDAndSeparatesInputs(t *testing.T) {
	const maxAuditRequestIDLength = 64
	key := automaticDecisionKey("flag_valid", 3, AutoApprovalRuleVersion, ActionApprove)
	if len(key) > maxAuditRequestIDLength || !strings.HasPrefix(key, "auto-") {
		t.Fatalf("automatic decision key = %q (length %d)", key, len(key))
	}
	if repeated := automaticDecisionKey("flag_valid", 3, AutoApprovalRuleVersion, ActionApprove); repeated != key {
		t.Fatalf("automatic decision key is not deterministic: %q != %q", repeated, key)
	}

	variants := []string{
		automaticDecisionKey("flag_other", 3, AutoApprovalRuleVersion, ActionApprove),
		automaticDecisionKey("flag_valid", 4, AutoApprovalRuleVersion, ActionApprove),
		automaticDecisionKey("flag_valid", 3, AutoApprovalRuleVersion+1, ActionApprove),
		automaticDecisionKey("flag_valid", 3, AutoApprovalRuleVersion, ActionReject),
	}
	for _, variant := range variants {
		if variant == key {
			t.Fatalf("different automatic decision inputs produced %q", key)
		}
		if len(variant) > maxAuditRequestIDLength {
			t.Fatalf("automatic decision key %q exceeds %d characters", variant, maxAuditRequestIDLength)
		}
	}
}
