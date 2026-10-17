package domain

import "testing"

func TestSocialPlatformValidation(t *testing.T) {
	// 验证只允许已实现平台且不可重复；其他配置错误不能掩盖社媒校验。
	for _, social := range [][]Social{{{Platform: "javascript:alert(1)", Enabled: true}}, {{Platform: "TELEGRAM"}, {Platform: "TELEGRAM"}}} {
		c := config()
		c.Social = social
		if c.Normalize() == nil {
			t.Fatal("非法社媒配置被接受")
		}
	}
	c := config()
	if err := c.Normalize(); err != nil {
		t.Fatal(err)
	}
	c.Social = []Social{{Platform: "FACEBOOK", Enabled: true}, {Platform: "TELEGRAM"}, {Platform: "WHATSAPP"}, {Platform: "TIKTOK"}, {Platform: "MORE"}}
	if err := c.Normalize(); err != nil {
		t.Fatal(err)
	}
}
