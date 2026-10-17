package domain

import (
	money "github.com/ex-business/ExCommission/internal/payments/domain"
	"strings"
	"testing"
)

func TestActualAmountAndRemarks(t *testing.T) {
	for _, v := range []string{"0", "-1", "01", "1.2", strings.Repeat("9", 39)} {
		if ValidAmount(money.Money{Currency: "USD", Minor: v}) {
			t.Fatal(v)
		}
	}
	if !ValidAmount(money.Money{Currency: "USD", Minor: "123"}) || !ValidRemark(strings.Repeat("备", 1000)) || ValidRemark(strings.Repeat("备", 1001)) {
		t.Fatal("amount/note boundary")
	}
}
