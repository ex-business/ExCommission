package domain

import "errors"

var ErrSelfApproval = errors.New("SELF_APPROVAL_FORBIDDEN")
var ErrStale = errors.New("STALE_APPROVAL")
var ErrState = errors.New("STATE_CONFLICT")

type Request struct {
	ID         string `json:"approval_id"`
	Type       string `json:"request_type"`
	Applicant  string `json:"applicant_id"`
	ObjectRef  string `json:"object_ref"`
	ObjectHash string `json:"object_hash"`
	Payload    []byte `json:"-"`
	Revision   uint64 `json:"revision"`
	Status     string `json:"approval_status"`
	Execution  string `json:"execution_status"`
}

func (r Request) CheckDecision(subject string, revision uint64, hash string) error {
	if subject == r.Applicant {
		return ErrSelfApproval
	}
	if r.Revision != revision || r.ObjectHash != hash {
		return ErrStale
	}
	if r.Status != "PENDING" {
		return ErrState
	}
	return nil
}
