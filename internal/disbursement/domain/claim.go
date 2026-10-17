package domain

// ClaimState projects authoritative instruction/payment facts without mutating either state machine.
func ClaimState(instruction, payment, reconciliation string) string {
	if reconciliation == "RECONCILIATION_EXCEPTION" {
		return "RECONCILIATION_EXCEPTION"
	}
	if payment == "SUCCEEDED" {
		return "CLAIMED"
	}
	if payment == "FAILED_FINAL" {
		return "FAILED"
	}
	switch instruction {
	case "CANCELLED":
		return "CANCELLED"
	case "CLAIMABLE":
		return "CLAIMABLE"
	case "QUEUED", "LINKED":
		return "PROCESSING"
	case "BLOCKED":
		return "FAILED"
	default:
		return "PREPARING"
	}
}
