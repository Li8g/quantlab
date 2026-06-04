package agent

import (
	"testing"

	"quantlab/internal/wire"
)

func TestExchangeConfig_Environment(t *testing.T) {
	tests := []struct {
		name string
		cfg  ExchangeConfig
		want string
	}{
		{"mock", ExchangeConfig{Name: "mock"}, wire.EnvironmentMock},
		{"mock wins over base_url", ExchangeConfig{Name: "mock", BaseURL: "https://api.binance.com"}, wire.EnvironmentMock},
		{"testnet", ExchangeConfig{Name: "binance_spot", BaseURL: "https://testnet.binance.vision"}, wire.EnvironmentTestnet},
		{"mainnet", ExchangeConfig{Name: "binance_spot", BaseURL: "https://api.binance.com"}, wire.EnvironmentMainnet},
		{"unknown base_url", ExchangeConfig{Name: "binance_spot", BaseURL: "https://example.test"}, ""},
		{"empty base_url", ExchangeConfig{Name: "binance_spot"}, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.cfg.Environment(); got != tc.want {
				t.Errorf("Environment() = %q, want %q", got, tc.want)
			}
		})
	}
}
