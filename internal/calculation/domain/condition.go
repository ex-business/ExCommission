package domain

import (
	"encoding/json"
	"errors"
	plan "github.com/ex-business/ExCommission/internal/plans/domain"
	"strings"
	"time"
)

type Truth uint8

const (
	Unknown Truth = iota
	False
	True
)

func Evaluate(condition plan.Condition, facts Facts) (Truth, error) {
	if condition.Logic != "" {
		truths := make([]Truth, 0, len(condition.Children))
		var failure error
		for _, child := range condition.Children {
			truth, err := Evaluate(child, facts)
			if err != nil && failure == nil {
				failure = err
			}
			truths = append(truths, truth)
		}
		if failure != nil {
			return Unknown, failure
		}
		switch condition.Logic {
		case "AND":
			hasUnknown := false
			for _, truth := range truths {
				if truth == False {
					return False, nil
				}
				if truth == Unknown {
					hasUnknown = true
				}
			}
			if hasUnknown {
				return Unknown, nil
			}
			return True, nil
		case "OR":
			hasUnknown := false
			for _, truth := range truths {
				if truth == True {
					return True, nil
				}
				if truth == Unknown {
					hasUnknown = true
				}
			}
			if hasUnknown {
				return Unknown, nil
			}
			return False, nil
		case "NOT":
			if len(truths) != 1 {
				return Unknown, errors.New("VALIDATION_FAILED")
			}
			if truths[0] == Unknown {
				return Unknown, nil
			}
			if truths[0] == True {
				return False, nil
			}
			return True, nil
		default:
			return Unknown, errors.New("VALIDATION_FAILED")
		}
	}
	cmp := 0
	switch condition.Metric {
	case "basis_amount", "quantity":
		actual, unit := facts.Basis, facts.Currency
		if condition.Metric == "quantity" {
			actual, unit = facts.Quantity, facts.QuantityUnit
		}
		if actual == "" || unit == "" {
			return Unknown, nil
		}
		if unit != condition.Unit {
			return Unknown, errors.New("UNIT_MISMATCH")
		}
		var expected string
		if err := json.Unmarshal(condition.Value, &expected); err != nil {
			return Unknown, errors.New("VALIDATION_FAILED")
		}
		a, err := ParseDecimal(actual)
		if err != nil {
			return Unknown, err
		}
		b, err := ParseDecimal(expected)
		if err != nil {
			return Unknown, err
		}
		cmp = a.Atoms().Cmp(b.Atoms())
	case "confirmed":
		if facts.Confirmed == nil {
			return Unknown, nil
		}
		var expected bool
		if err := json.Unmarshal(condition.Value, &expected); err != nil {
			return Unknown, err
		}
		if *facts.Confirmed != expected {
			cmp = 1
		}
	case "product_code", "scene":
		actual := facts.Product
		if condition.Metric == "scene" {
			actual = facts.Scene
		}
		if actual == "" {
			return Unknown, nil
		}
		if condition.Operator == "IN" {
			var values []string
			if err := json.Unmarshal(condition.Value, &values); err != nil {
				return Unknown, err
			}
			for _, value := range values {
				if actual == value {
					return True, nil
				}
			}
			return False, nil
		}
		var expected string
		if err := json.Unmarshal(condition.Value, &expected); err != nil {
			return Unknown, err
		}
		cmp = strings.Compare(actual, expected)
	case "business_time":
		if facts.BusinessTime.IsZero() {
			return Unknown, nil
		}
		var value string
		if err := json.Unmarshal(condition.Value, &value); err != nil {
			return Unknown, err
		}
		expected, err := time.Parse(time.RFC3339Nano, value)
		if err != nil {
			return Unknown, err
		}
		cmp = facts.BusinessTime.Compare(expected)
	default:
		return Unknown, errors.New("VALIDATION_FAILED")
	}
	match := false
	switch condition.Operator {
	case "EQ":
		match = cmp == 0
	case "NE":
		match = cmp != 0
	case "GT":
		match = cmp > 0
	case "GTE":
		match = cmp >= 0
	case "LT":
		match = cmp < 0
	case "LTE":
		match = cmp <= 0
	default:
		return Unknown, errors.New("VALIDATION_FAILED")
	}
	if match {
		return True, nil
	}
	return False, nil
}
