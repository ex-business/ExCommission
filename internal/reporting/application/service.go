package application

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/csv"
	"encoding/hex"
	"encoding/json"
	"errors"
	identity "github.com/ex-business/ExCommission/internal/identity/domain"
	response "github.com/ex-business/ExCommission/internal/platform/application"
	foundation "github.com/ex-business/ExCommission/internal/platform/domain"
	db "github.com/ex-business/ExCommission/internal/platform/ports"
	model "github.com/ex-business/ExCommission/internal/reporting/domain"
	"github.com/ex-business/ExCommission/internal/reporting/ports"
	"regexp"
	"sort"
	"strings"
	"time"
)

type ExportInput struct {
	Kind    string        `json:"kind"`
	Filters model.Filters `json:"filters"`
	Format  string        `json:"format"`
}
type Service struct {
	Commands db.Commands
	Query    db.Executor
	Reader   ports.Reader
	Exports  ports.Exports
	Now      func() time.Time
	ID       func() string
	TTL      time.Duration
}

var numericCSV = regexp.MustCompile(`^-?[0-9]+$`)

func CSV(report model.Report) ([]byte, error) {
	columns := []string{}
	seen := map[string]bool{}
	for _, row := range report.Rows {
		for column := range row {
			if !seen[column] {
				columns = append(columns, column)
				seen[column] = true
			}
		}
	}
	sort.Strings(columns)
	var body bytes.Buffer
	writer := csv.NewWriter(&body)
	if err := writer.Write(columns); err != nil {
		return nil, err
	}
	for _, row := range report.Rows {
		values := []string{}
		for _, column := range columns {
			value := row[column]
			if value != "" && strings.ContainsAny(value[:1], "=+-@\t\r") && !numericCSV.MatchString(value) {
				value = "'" + value
			}
			values = append(values, value)
		}
		if err := writer.Write(values); err != nil {
			return nil, err
		}
	}
	writer.Flush()
	return body.Bytes(), writer.Error()
}
func (s Service) CreateExport(ctx context.Context, p identity.Principal, input ExportInput, key, request string) (foundation.CommandResult, error) {
	scope := foundation.Scope{Tenant: p.Tenant, App: p.App}
	if saved, found, err := s.Commands.Lookup(ctx, scope, "report-export", key, input); err != nil || found {
		return saved, err
	}
	if input.Format != "CSV" || s.TTL <= 0 {
		return foundation.CommandResult{}, foundation.ErrValidation
	}
	report, err := s.Reader.Read(ctx, scope, input.Kind, input.Filters)
	if err != nil {
		return foundation.CommandResult{}, err
	}
	file, err := CSV(report)
	if err != nil {
		return foundation.CommandResult{}, err
	}
	if len(file) > 10<<20 {
		return foundation.CommandResult{}, errors.New("REPORT_TOO_LARGE")
	}
	snapshot, _ := json.Marshal(report)
	sum := sha256.Sum256(append(append([]byte{}, snapshot...), file...))
	hash := hex.EncodeToString(sum[:])
	return s.Commands.Execute(ctx, scope, "report-export", key, input, func(tx db.Executor) (foundation.CommandResult, error) {
		id := s.ID()
		now := s.Now()
		if err := s.Exports.Save(ctx, tx, scope, id, p.Subject, input.Filters, report, file, hash, now, now.Add(s.TTL)); err != nil {
			return foundation.CommandResult{}, err
		}
		return response.Result(202, id, request, map[string]any{"export_id": id, "status": "READY", "snapshot_hash": hash, "as_of": report.AsOf, "expires_at": now.Add(s.TTL)})
	})
}
