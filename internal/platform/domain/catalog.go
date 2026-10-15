package domain

import "time"

type Currency struct {
	Code     string   `json:"code"`
	Exponent int      `json:"exponent"`
	Format   string   `json:"display_format"`
	Channels []string `json:"payment_channels"`
}
type CurrencyCatalog struct {
	Version    string     `json:"version_id"`
	From       time.Time  `json:"effective_from"`
	Currencies []Currency `json:"currencies"`
}
type Calendar struct {
	Version  string    `json:"version_id"`
	From     time.Time `json:"effective_from"`
	Timezone string    `json:"timezone"`
	Period   string    `json:"period"`
}
type ComponentState struct {
	Type    string `json:"component_type"`
	ID      string `json:"component_id"`
	Enabled bool   `json:"enabled"`
}

func CurrencyExponents() map[string]int { return map[string]int{"USD": 2, "BTC": 8, "POINT": 0} }
