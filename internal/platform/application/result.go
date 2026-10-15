package application

import (
	"encoding/json"
	"github.com/ex-business/ExCommission/internal/platform/domain"
)

func Result(status int, resource, request string, data any) (domain.CommandResult, error) {
	body, err := json.Marshal(map[string]any{"data": data, "request_id": request})
	return domain.CommandResult{Status: status, Body: body, ResourceID: resource}, err
}
