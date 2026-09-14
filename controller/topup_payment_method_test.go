package controller

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNormalizeEpayMethodKeepsConfiguredNamesAndMigratesLegacyAlipay(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{name: "legacy alipay", in: " alipay ", want: "alipay_web"},
		{name: "configured alipay", in: "alipay_web", want: "alipay_web"},
		{name: "wechat", in: "wxpay", want: "wxpay"},
		{name: "custom provider", in: "custom1", want: "custom1"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			require.Equal(t, test.want, normalizeEpayMethod(test.in))
		})
	}
}
