package runtime

import (
	"context"
	"errors"
	configs "github.com/ex-business/ExCommission/internal/affiliate/adapters"
	affiliate "github.com/ex-business/ExCommission/internal/affiliate/domain"
	model "github.com/ex-business/ExCommission/internal/affiliateflow/domain"
	catalog "github.com/ex-business/ExCommission/internal/platform/adapters/catalog"
	foundation "github.com/ex-business/ExCommission/internal/platform/domain"
	db "github.com/ex-business/ExCommission/internal/platform/ports"
	"os"
	"sort"
	"strings"
	"time"
)

type affiliateCurrency struct {
	Code    string `json:"code"`
	Scale   int    `json:"currency_scale"`
	Catalog string `json:"catalog_version"`
}

// 扩展钱包币种必须同时在受信服务端能力清单与平台主货币目录存在，不能因配置表单可输入就默认可到账。
func affiliateWalletCurrencies() []string {
	codes := map[string]bool{"USD": true, "BTC": true}
	for _, code := range strings.Split(os.Getenv("AFFILIATE_WALLET_CURRENCIES"), ",") {
		code = strings.TrimSpace(code)
		if code != "" {
			codes[code] = true
		}
	}
	out := []string{}
	for code := range codes {
		out = append(out, code)
	}
	sort.Strings(out)
	return out
}
func validateAffiliateCurrency(ctx context.Context, tx db.Executor, sc foundation.Scope, c affiliate.Config, at time.Time) error {
	supported := false
	for _, code := range affiliateWalletCurrencies() {
		supported = supported || code == c.Currency
	}
	if !supported {
		return errors.New("UNSUPPORTED_CAPABILITY")
	}
	_, scale, e := (catalog.Repository{}).CurrencyAt(ctx, tx, sc, c.Currency, at)
	if e != nil {
		return e
	}
	if scale != c.CurrencyScale {
		return foundation.ErrValidation
	}
	return nil
}
func validateAffiliateRewardCurrency(ctx context.Context, tx db.Executor, sc foundation.Scope, r model.Reward, at time.Time) error {
	v, e := (configs.Repository{}).Version(ctx, tx, sc, r.ConfigRevision)
	if e != nil {
		return e
	}
	if r.Currency != v.Config.Currency {
		return foundation.ErrValidation
	}
	return validateAffiliateCurrency(ctx, tx, sc, v.Config, at)
}
func affiliateCurrencies(ctx context.Context, tx db.Executor, sc foundation.Scope, at time.Time) ([]affiliateCurrency, error) {
	out := []affiliateCurrency{}
	for _, code := range affiliateWalletCurrencies() {
		v, scale, e := (catalog.Repository{}).CurrencyAt(ctx, tx, sc, code, at)
		if e != nil {
			return nil, e
		}
		out = append(out, affiliateCurrency{Code: code, Scale: scale, Catalog: v})
	}
	return out, nil
}
