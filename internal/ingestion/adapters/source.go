package adapters

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	original "github.com/ex-business/ExCommission/internal/affiliateflow/adapters"
	facts "github.com/ex-business/ExCommission/internal/affiliateflow/domain"
	factports "github.com/ex-business/ExCommission/internal/affiliateflow/ports"
	identity "github.com/ex-business/ExCommission/internal/identity/domain"
	model "github.com/ex-business/ExCommission/internal/ingestion/domain"
	catalog "github.com/ex-business/ExCommission/internal/platform/adapters/catalog"
	foundation "github.com/ex-business/ExCommission/internal/platform/domain"
	db "github.com/ex-business/ExCommission/internal/platform/ports"
	"time"
)

type Registry struct {
	Query     db.Executor
	KeysFile  string
	InitError error
}

func (r Registry) Source(ctx context.Context, tx db.Executor, sc foundation.Scope, id string, lock bool) (model.Source, uint64, error) {
	if r.InitError != nil {
		return model.Source{}, 0, r.InitError
	}
	head, e := (Repository{}).Head(ctx, tx, sc, lock)
	if e != nil {
		return model.Source{}, 0, e
	}
	for _, s := range head.Config.Sources {
		if s.ID == id {
			return s, head.Revision, nil
		}
	}
	return model.Source{}, 0, errors.New("PERMISSION_DENIED:INGESTION_SOURCE_NOT_FOUND")
}

type ConfiguredSource struct {
	Registry Registry
	Subject  string
	ID       string
}

func (r Registry) Resolve(ctx context.Context, p identity.Principal, id string) (factports.Source, error) {
	s, _, e := r.Source(ctx, r.Query, foundation.Scope{Tenant: p.Tenant, App: p.App}, id, false)
	if e != nil {
		return nil, e
	}
	if p.OpsCredential != "" || s.Subject != p.Subject {
		return nil, identity.ErrPermission
	}
	return ConfiguredSource{Registry: r, Subject: p.Subject, ID: id}, nil
}
func (s ConfiguredSource) verify(ctx context.Context, tx db.Executor, sc foundation.Scope, raw []byte, signature, prefix string, lock bool) (model.Source, uint64, error) {
	source, rev, e := s.Registry.Source(ctx, tx, sc, s.ID, lock)
	if e != nil {
		return source, rev, e
	}
	if source.Subject != s.Subject {
		return source, rev, identity.ErrPermission
	}
	key, e := FindKey(s.Registry.KeysFile, sc, source.KeyRef)
	if e != nil {
		return source, rev, e
	}
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(prefix + "\n" + sc.Tenant + "\n" + sc.App + "\n" + source.ID + "\n"))
	mac.Write(raw)
	sig, e := hex.DecodeString(signature)
	if e != nil || !hmac.Equal(sig, mac.Sum(nil)) {
		return source, rev, errors.New("SOURCE_SIGNATURE_INVALID")
	}
	return source, rev, nil
}
func (s ConfiguredSource) Verify(ctx context.Context, sc foundation.Scope, raw []byte, signature string) (facts.Event, error) {
	cfg, _, e := s.verify(ctx, s.Registry.Query, sc, raw, signature, "affiliate-ingestion-v1", false)
	if e != nil {
		return facts.Event{}, e
	}
	if !cfg.Enabled {
		return facts.Event{}, errors.New("PERMISSION_DENIED:INGESTION_SOURCE_DISABLED")
	}
	envelope, e := model.DecodeEnvelope(raw)
	if e != nil || envelope.Source != s.ID {
		return facts.Event{}, model.ErrEvent
	}
	f, e := Map(cfg, envelope)
	if e != nil {
		return facts.Event{}, e
	}
	if e = ValidateScale(ctx, s.Registry.Query, sc, cfg, envelope, f); e != nil {
		return facts.Event{}, e
	}
	return facts.Event{Identity: f.Identity, Source: cfg.ID, EventID: f.EventID, BusinessID: f.BusinessID, ExternalUser: f.ExternalUser, Kind: f.Kind, Currency: f.Currency, Amount: f.Amount, ValidBet: f.ValidBet, Venue: f.Venue, At: f.At}, nil
}
func Map(s model.Source, e model.Envelope) (model.Fact, error) {
	for _, r := range s.Rules {
		if r.EventType == e.EventType && r.Version == e.Version {
			return r.Map(e.Data)
		}
	}
	return model.Fact{}, errors.New("VALIDATION_FAILED:INGESTION_EVENT_TYPE_NOT_ALLOWED")
}
func (s ConfiguredSource) VerifyWatermark(ctx context.Context, sc foundation.Scope, raw []byte, signature string) (facts.Watermark, error) {
	cfg, _, e := s.verify(ctx, s.Registry.Query, sc, raw, signature, "affiliate-watermark-v1", false)
	if e != nil {
		return facts.Watermark{}, e
	}
	key, e := FindKey(s.Registry.KeysFile, sc, cfg.KeyRef)
	if e != nil {
		return facts.Watermark{}, e
	}
	// 停用后仍允许按原认证关闭历史来源日；停用不放行新业务事实。
	return (original.HMACSource{Scope: sc, Source: cfg.ID, Key: key}).VerifyWatermark(ctx, sc, raw, signature)
}

// Guard在业务事务内重新验签并锁定当前配置，阻止停用/密钥轮换与事实入账的竞态。
func (r Registry) Guard(ctx context.Context, tx db.Executor, p identity.Principal, raw []byte, signature string, event facts.Event) error {
	sc := foundation.Scope{Tenant: p.Tenant, App: p.App}
	s := ConfiguredSource{Registry: r, Subject: p.Subject, ID: event.Source}
	cfg, _, e := s.verify(ctx, tx, sc, raw, signature, "affiliate-ingestion-v1", true)
	if e != nil {
		return e
	}
	if !cfg.Enabled {
		return errors.New("PERMISSION_DENIED:INGESTION_SOURCE_DISABLED")
	}
	return nil
}
func (r Registry) Receipt(ctx context.Context, tx db.Executor, sc foundation.Scope, event facts.Event, raw []byte, accepted, late bool, at time.Time) error {
	e, err := model.DecodeEnvelope(raw)
	if err != nil {
		return err
	}
	_, rev, err := r.Source(ctx, tx, sc, event.Source, false)
	if err != nil {
		return err
	}
	sum := sha256.Sum256(raw)
	digest := hex.EncodeToString(sum[:])
	status := "DUPLICATE"
	if accepted {
		status = "ACCEPTED"
	}
	if late {
		status = "LATE_ADJUSTMENT"
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO ingestion_receipts VALUES(?,?,?,?,?,?,?,?,?,?,?) ON DUPLICATE KEY UPDATE event_id=event_id`, sc.Tenant, sc.App, event.Source, event.EventID, event.BusinessID, e.EventType, e.Version, rev, digest, status, at)
	if err != nil {
		return err
	}
	var old string
	if err = tx.QueryRowContext(ctx, `SELECT body_hash FROM ingestion_receipts WHERE tenant_id=? AND app_id=? AND source_id=? AND event_id=? FOR UPDATE`, sc.Tenant, sc.App, event.Source, event.EventID).Scan(&old); err != nil {
		return err
	}
	if old != digest {
		return foundation.ErrConflict
	}
	return nil
}

type Members struct {
	factports.Members
	Registry Registry
}

func (m Members) Resolve(ctx context.Context, tx db.Executor, sc foundation.Scope, source, user string) (facts.Member, error) {
	if source == "sandbox" {
		return m.Members.Resolve(ctx, tx, sc, source, user)
	}
	s, _, e := m.Registry.Source(ctx, tx, sc, source, false)
	if e != nil {
		return facts.Member{}, e
	}
	return m.Members.Resolve(ctx, tx, sc, s.MemberSource, user)
}

// ExplicitMarshal保留预览输出为固定字段，未知外部数据不进入响应和接入记录。
func ExplicitMarshal(f model.Fact) json.RawMessage { raw, _ := json.Marshal(f); return raw }

// 金额小数位必须与业务时点的权威币种目录一致，不能将BTC主单位误按USD精度入账。
func ValidateScale(ctx context.Context, tx db.Executor, sc foundation.Scope, source model.Source, e model.Envelope, f model.Fact) error {
	for _, rule := range source.Rules {
		if rule.EventType == e.EventType && rule.Version == e.Version {
			_, scale, err := (catalog.Repository{}).CurrencyAt(ctx, tx, sc, f.Currency, f.At)
			if err != nil {
				return err
			}
			if scale != rule.CurrencyScale {
				return errors.New("UNIT_MISMATCH")
			}
			return nil
		}
	}
	return model.ErrEvent
}

// 关账与新事实使用相同配置行锁及当前密钥校验，轮换后不能靠已验签的旧请求补关账。
func (r Registry) WatermarkGuard(ctx context.Context, tx db.Executor, p identity.Principal, raw []byte, signature string, m facts.Watermark) error {
	_, _, e := (ConfiguredSource{Registry: r, Subject: p.Subject, ID: m.Source}).verify(ctx, tx, foundation.Scope{Tenant: p.Tenant, App: p.App}, raw, signature, "affiliate-watermark-v1", true)
	return e
}
