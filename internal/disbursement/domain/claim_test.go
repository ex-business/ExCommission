package domain

import "testing"

func TestClaimStateRequiresConfirmedPayment(t *testing.T) {
	for _, v := range []struct{ i, p, r, w string }{
		{"DRAFT", "", "", "PREPARING"}, {"CLAIMABLE", "", "", "CLAIMABLE"}, {"CANCELLED", "", "", "CANCELLED"}, {"QUEUED", "", "", "PROCESSING"}, {"LINKED", "CREATED", "PENDING", "PROCESSING"}, {"LINKED", "UNKNOWN", "PENDING", "PROCESSING"}, {"LINKED", "SUCCEEDED", "MATCHED", "CLAIMED"}, {"LINKED", "FAILED_FINAL", "MATCHED", "FAILED"}, {"BLOCKED", "", "", "FAILED"}, {"LINKED", "SUCCEEDED", "RECONCILIATION_EXCEPTION", "RECONCILIATION_EXCEPTION"},
	} {
		if got := ClaimState(v.i, v.p, v.r); got != v.w {
			t.Errorf("%+v got %s", v, got)
		}
	}
}
