package application

import (
	"bytes"
	model "github.com/ex-business/ExCommission/internal/reporting/domain"
	"testing"
)

func TestCSVNeutralizesFormulaValuesAndPreservesMoney(t *testing.T) {
	body, err := CSV(model.Report{Rows: []map[string]string{{"agent_id": "=HYPERLINK(\"https://invalid\")", "net_minor": "-12", "currency": "USD"}}})
	if err != nil || !bytes.Contains(body, []byte("'=HYPERLINK")) || !bytes.Contains(body, []byte("-12")) || bytes.Contains(body, []byte("'-12")) {
		t.Fatal(string(body), err)
	}
}
