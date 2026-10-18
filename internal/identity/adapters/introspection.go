package adapters

import (
	"context"
	"encoding/json"
	"errors"
	routersdk "github.com/ex-business/ex-service-router/sdk"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/ex-business/ExCommission/internal/identity/domain"
)

type ExternalConfig struct {
	Members      map[string]string `json:"members,omitempty"`
	Endpoint     string            `json:"endpoint"`
	ClientID     string            `json:"client_id"`
	ClientSecret string            `json:"client_secret"`
	Issuer       string            `json:"issuer"`
	Audience     string            `json:"audience"`
	Bindings     map[string]string `json:"bindings"`
}
type Introspection struct {
	Config     ExternalConfig
	Client     *http.Client
	Principals map[string]domain.Principal
	Now        func() time.Time
}

func NewIntrospection(c ExternalConfig, principals map[string]domain.Principal) (*Introspection, error) {
	u, e := url.Parse(c.Endpoint)
	if e != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || c.ClientID == "" || c.ClientSecret == "" || c.Issuer == "" || c.Audience == "" || len(c.Bindings) == 0 {
		return nil, errors.New("invalid external identity configuration")
	}
	for sub, id := range c.Bindings {
		if sub == "" || principals[id].Subject == "" {
			return nil, errors.New("unknown external identity binding")
		}
	}
	return &Introspection{c, &http.Client{Timeout: 3 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, principals, time.Now}, nil
}
func (p *Introspection) Authenticate(ctx context.Context, header string) (domain.Principal, error) {
	if !strings.HasPrefix(header, "Bearer ") || len(header) > 8192 || strings.TrimPrefix(header, "Bearer ") == "" {
		return domain.Principal{}, domain.ErrUnauthenticated
	}
	form := url.Values{"token": {strings.TrimPrefix(header, "Bearer ")}, "token_type_hint": {"access_token"}}
	req, err := http.NewRequestWithContext(ctx, "POST", p.Config.Endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return domain.Principal{}, errors.New("identity provider unavailable")
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetBasicAuth(p.Config.ClientID, p.Config.ClientSecret)
	res, err := routersdk.Do(p.Client, req)
	if err != nil {
		return domain.Principal{}, errors.New("identity provider unavailable")
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return domain.Principal{}, errors.New("identity provider unavailable")
	}
	body, err := io.ReadAll(io.LimitReader(res.Body, 65537))
	if err != nil || len(body) > 65536 {
		return domain.Principal{}, errors.New("identity provider invalid response")
	}
	var v struct {
		Active   bool            `json:"active"`
		Subject  string          `json:"sub"`
		Issuer   string          `json:"iss"`
		Audience json.RawMessage `json:"aud"`
		Expires  int64           `json:"exp"`
	}
	if json.Unmarshal(body, &v) != nil {
		return domain.Principal{}, errors.New("identity provider invalid response")
	}
	var aud string
	var auds []string
	match := false
	if json.Unmarshal(v.Audience, &aud) == nil {
		match = aud == p.Config.Audience
	} else if json.Unmarshal(v.Audience, &auds) == nil {
		for _, a := range auds {
			match = match || a == p.Config.Audience
		}
	}
	id, ok := p.Config.Bindings[v.Subject]
	if !v.Active || !ok || v.Issuer != p.Config.Issuer || !match || v.Expires <= p.Now().Unix() {
		return domain.Principal{}, domain.ErrUnauthenticated
	}
	principal, ok := p.Principals[id]
	if !ok {
		return domain.Principal{}, domain.ErrUnauthenticated
	}
	return principal, nil
}
