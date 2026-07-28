package joinrequests

// MergeObservedDecisionStatus keeps confirmed local decisions authoritative.
// A checked-only NapCat snapshot can prove that somebody processed a request,
// but cannot prove whether it was approved or rejected.
func MergeObservedDecisionStatus(current DecisionStatus, observed ObservedStatus) DecisionStatus {
	if observed != ObservedChecked {
		return current
	}
	switch current {
	case DecisionPending, DecisionUnknown:
		return DecisionExternalProcessed
	default:
		return current
	}
}
