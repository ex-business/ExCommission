package domain

import "errors"

type Scope struct{ Tenant, App string }

func (s Scope) Valid() bool     { return ValidID(s.Tenant) && ValidID(s.App) }
func ValidID(value string) bool { return len(value) > 0 && len([]rune(value)) <= 128 }

var ErrConflict = errors.New("IDEMPOTENCY_CONFLICT")
var ErrValidation = errors.New("VALIDATION_FAILED")
