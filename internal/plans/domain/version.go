package domain

import (
	"encoding/json"
	"time"
)

type Draft struct {
	Config   Config `json:"draft"`
	Revision uint64 `json:"revision"`
	Hash     string `json:"draft_hash"`
	Status   string `json:"status"`
}
type Version struct {
	ID     string     `json:"version_id"`
	Number uint64     `json:"version_number"`
	Config Config     `json:"draft"`
	Hash   string     `json:"config_hash"`
	From   time.Time  `json:"effective_from"`
	To     *time.Time `json:"effective_to"`
}

func (d Draft) MarshalJSON() ([]byte, error) {
	body, err := json.Marshal(d.Config)
	if err != nil {
		return nil, err
	}
	var fields map[string]json.RawMessage
	if err = json.Unmarshal(body, &fields); err != nil {
		return nil, err
	}
	fields["draft_hash"], _ = json.Marshal(d.Hash)
	fields["status"], _ = json.Marshal(d.Status)
	fields["revision"], _ = json.Marshal(d.Revision)
	return json.Marshal(fields)
}
