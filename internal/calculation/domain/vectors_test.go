package domain

import (
	"encoding/json"
	plan "github.com/ex-business/ExCommission/internal/plans/domain"
	"math/big"
	"os"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

type numericInput struct {
	Basis      string      `json:"basis"`
	Rate       string      `json:"rate_ppm"`
	Exponent   int         `json:"exponent"`
	Quantity   string      `json:"quantity"`
	UnitPrice  string      `json:"unit_price"`
	Rates      []string    `json:"rates_ppm"`
	Eligible   []bool      `json:"eligible"`
	Tiers      [][]*string `json:"tiers"`
	Pool       string      `json:"pool_minor"`
	Candidates []struct {
		ID       string `json:"id"`
		Weight   string `json:"weight"`
		Eligible bool   `json:"eligible"`
	} `json:"candidates"`
	Totals    []string `json:"refund_totals"`
	OldNet    string   `json:"old_net_minor"`
	PaidMinor string   `json:"paid_minor"`
	Unpaid    string   `json:"unpaid_minor"`
	NewNet    string   `json:"new_net_minor"`
	Original  string   `json:"original_minor"`
	Net       string   `json:"net"`
	Debt      string   `json:"debt"`
	Paid      string   `json:"paid"`
	Recovered string   `json:"recovered"`
	Pending   string   `json:"pending"`
	Available string   `json:"available"`
	Reserved  string   `json:"reserved"`
}

func TestStandardNumericVectors(t *testing.T) {
	file := os.Getenv("EXCOMMISSION_VECTOR_FILE")
	if file == "" {
		file = "../../../docs/local/commission-business-standard-v1/acceptance-vectors.json"
	}
	body, err := os.ReadFile(file)
	if err != nil {
		if os.Getenv("EXCOMMISSION_VECTOR_FILE") == "" {
			t.Skip("local reference vectors not mounted")
		}
		t.Fatal(err)
	}
	var suite struct {
		Version string `json:"standard_version"`
		Vectors []struct {
			ID, Kind string
			Input    numericInput
			Expected json.RawMessage
		}
	}
	if err = json.Unmarshal(body, &suite); err != nil || suite.Version != "1" || len(suite.Vectors) != 22 {
		t.Fatalf("invalid reference suite %v", err)
	}
	for _, vector := range suite.Vectors {
		t.Run(vector.ID, func(t *testing.T) {
			got := evaluateVector(t, vector.Kind, vector.Input)
			var expected any
			if err := json.Unmarshal(vector.Expected, &expected); err != nil {
				t.Fatal(err)
			}
			encoded, err := json.Marshal(got)
			if err != nil {
				t.Fatal(err)
			}
			var normalized any
			if err = json.Unmarshal(encoded, &normalized); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(normalized, expected) {
				t.Fatalf("got %s want %s", encoded, vector.Expected)
			}
		})
	}
}
func number(t *testing.T, value string) *big.Int {
	t.Helper()
	n, ok := new(big.Int).SetString(value, 10)
	if !ok {
		t.Fatalf("bad integer %q", value)
	}
	return n
}
func evaluateVector(t *testing.T, kind string, v numericInput) any {
	input := sampleInput(t)
	if v.Basis != "" {
		input.Facts.Basis = v.Basis
	}
	input.Exponent = v.Exponent
	rule := &input.Plan.Rules[0]
	switch kind {
	case "percent":
		rule.Parameters.RatePPM = v.Rate
	case "unit_price":
		rule.Algorithm = "UNIT_PRICE"
		rule.Parameters = plan.Parameters{UnitPrice: v.UnitPrice, QuantityUnit: "LOT", RewardCurrency: "USD"}
		rule.Funding = nil
		budget := strings.Repeat("9", 38)
		input.Plan.EventBudget = &budget
		input.Facts.Quantity = v.Quantity
		input.Facts.QuantityUnit = "LOT"
	case "differential", "independent":
		rule.Algorithm = strings.ToUpper(kind)
		rule.Recipient = "ANCESTORS"
		input.Plan.AgentPolicy.MaxDepth = len(v.Rates)
		input.Plan.Grades = []plan.Grade{}
		rule.Parameters = plan.Parameters{}
		input.Ancestors = []Actor{}
		for index, rate := range v.Rates {
			grade := "G" + strconv.Itoa(index)
			input.Plan.Grades = append(input.Plan.Grades, plan.Grade{ID: grade, Rank: index, Conditions: []plan.GradeCondition{}})
			eligible := v.Eligible[index]
			input.Ancestors = append(input.Ancestors, Actor{ID: "A" + strconv.Itoa(index), Depth: index + 1, Grade: grade, Eligible: &eligible, Status: "ACTIVE"})
			if kind == "differential" {
				rule.Parameters.Rates = append(rule.Parameters.Rates, plan.Rate{Product: input.Facts.Product, Grade: grade, PPM: rate})
			}
		}
		if kind == "independent" {
			for depth, rate := range v.Rates {
				for _, grade := range input.Plan.Grades {
					d := depth + 1
					rule.Parameters.Rates = append(rule.Parameters.Rates, plan.Rate{Product: input.Facts.Product, Grade: grade.ID, Depth: &d, PPM: rate})
				}
			}
		}
	case "marginal", "highest_rate":
		rule.Algorithm = strings.ToUpper(kind)
		rule.Parameters = plan.Parameters{}
		for _, tier := range v.Tiers {
			rule.Parameters.Tiers = append(rule.Parameters.Tiers, plan.Tier{Lower: *tier[0], Upper: tier[1], PPM: *tier[2]})
		}
	case "weighted":
		candidates := []WeightedCandidate{}
		for _, candidate := range v.Candidates {
			candidates = append(candidates, WeightedCandidate{ID: candidate.ID, Weight: candidate.Weight, Eligible: candidate.Eligible})
		}
		result, err := AllocateWeighted(number(t, v.Pool), candidates)
		if err != nil {
			t.Fatal(err)
		}
		output := map[string]string{"platform_remainder": result.Platform.String()}
		for id, amount := range result.Targets {
			output[id] = amount.String()
		}
		return output
	case "refund_percent":
		basis, err := ParseDecimal(v.Basis)
		if err != nil {
			t.Fatal(err)
		}
		rate, err := plan.PPM(v.Rate)
		if err != nil {
			t.Fatal(err)
		}
		original := percent(basis.Atoms(), rate, v.Exponent)
		previous := new(big.Int).Set(original)
		nets, reductions := []string{}, []string{}
		for _, refund := range v.Totals {
			total, err := ParseDecimal(refund)
			if err != nil {
				t.Fatal(err)
			}
			remaining, err := RemainingBasis(basis, total)
			if err != nil {
				t.Fatal(err)
			}
			net := percent(remaining.Atoms(), rate, v.Exponent)
			nets = append(nets, net.String())
			reductions = append(reductions, new(big.Int).Sub(previous, net).String())
			previous = net
		}
		return map[string]any{"original_minor": original.String(), "net_minor": nets, "reductions_minor": reductions}
	case "refund_allocation":
		used, left, debt, err := AllocateRefund(number(t, v.OldNet), number(t, v.PaidMinor), number(t, v.Unpaid), number(t, v.NewNet))
		if err != nil {
			t.Fatal(err)
		}
		return map[string]string{"unpaid_reduction_minor": used.String(), "new_unpaid_minor": left.String(), "debt_minor": debt.String()}
	case "fixed_refund":
		basis, err := ParseDecimal(v.Basis)
		if err != nil {
			t.Fatal(err)
		}
		outputs := []string{}
		for _, total := range v.Totals {
			refund, err := ParseDecimal(total)
			if err != nil {
				t.Fatal(err)
			}
			remaining, err := RemainingBasis(basis, refund)
			if err != nil {
				t.Fatal(err)
			}
			if remaining.Atoms().Sign() == 0 {
				outputs = append(outputs, "0")
			} else {
				outputs = append(outputs, v.Original)
			}
		}
		return outputs
	case "conservation":
		return Conserves(number(t, v.Net), number(t, v.Debt), number(t, v.Paid), number(t, v.Recovered), number(t, v.Pending), number(t, v.Available), number(t, v.Reserved))
	default:
		t.Fatalf("unimplemented vector %s", kind)
	}
	result := Calculate(input)
	if result.Status != "SUCCEEDED" {
		t.Fatalf("calculation %+v", result)
	}
	if kind == "differential" || kind == "independent" {
		outputs := make([]string, len(v.Rates))
		for index := range outputs {
			outputs[index] = "0"
		}
		for _, item := range result.Items {
			outputs[item.Depth-1] = item.Minor
		}
		return outputs
	}
	if len(result.Items) == 0 {
		return "0"
	}
	return result.Items[0].Minor
}
