package runtime

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	model "github.com/ex-business/ExCommission/internal/affiliateflow/domain"
	principal "github.com/ex-business/ExCommission/internal/identity/domain"
	mysql "github.com/ex-business/ExCommission/internal/platform/adapters/mysql"
	foundation "github.com/ex-business/ExCommission/internal/platform/domain"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
)

// TestPrepareAffiliateBrowserDaily 仅用于独立remaining-*验收租户；推进业务时钟，不修改系统时间或生产数据。
// 事实必须先由真实模拟会员和受信来源生成；此助手不创建身份、订单、奖励或钱包记录。
func TestPrepareAffiliateBrowserDaily(t *testing.T) {
	dir := os.Getenv("AFFILIATE_BROWSER_FIXTURE_DIR")
	if dir == "" {
		t.Skip("仅在显式浏览器验收时启用")
	}
	raw, e := os.ReadFile(filepath.Join(dir, "runtime.json"))
	if e != nil {
		t.Fatal(e)
	}
	var cfg map[string]string
	if json.Unmarshal(raw, &cfg) != nil {
		t.Fatal("配置格式错误")
	}
	if cfg["APP_MODE"] != "local" || !strings.HasPrefix(cfg["LOCAL_TENANT"], "remaining-") || cfg["HTTP_ADDR"] != "127.0.0.1:18386" {
		t.Fatal("拒绝非隔离验收作用域")
	}
	for k, v := range cfg {
		t.Setenv(k, v)
	}
	ctx := context.Background()
	store, e := mysql.Open(ctx, cfg["DB_ADDRESS"], cfg["DB_USER"], cfg["MYSQL_PASSWORD"], "excommission")
	if e != nil {
		t.Fatal(e)
	}
	defer store.DB.Close()
	service := affiliateFlowService(store)
	sc := foundation.Scope{Tenant: cfg["LOCAL_TENANT"], App: cfg["LOCAL_APP"]}
	raw, e = os.ReadFile(filepath.Join(dir, "source-facts.json"))
	if e != nil {
		t.Fatal(e)
	}
	var events []model.Event
	if json.Unmarshal(raw, &events) != nil || len(events) != 4 {
		t.Fatal("来源事实不完整")
	}
	now := time.Now().UTC()
	v, e := service.Configs.At(ctx, store.DB, sc, now)
	if e != nil {
		t.Fatal(e)
	}
	start, end := v.Config.Day(now)
	parts := []string{}
	for _, event := range events {
		if event.At.Before(start) || !event.At.Before(end) {
			t.Fatal("仅支持本验收当日事实")
		}
		parts = append(parts, event.Kind+"\n"+event.BusinessID+"\n"+event.BusinessHash())
	}
	sort.Strings(parts)
	simulated := end.Add(time.Hour)
	service.Now = func() time.Time { return simulated }
	mark := model.Watermark{Source: "sandbox", Day: start.Format("2006-01-02"), Count: uint64(len(parts)), Digest: model.ID(parts...)}
	raw, _ = json.Marshal(mark)
	mac := hmac.New(sha256.New, []byte(cfg["LOCAL_AFFILIATE_FLOW_KEY"]))
	mac.Write([]byte("affiliate-watermark-v1\n" + sc.Tenant + "\n" + sc.App + "\nsandbox\n"))
	mac.Write(raw)
	p := principal.Principal{Tenant: sc.Tenant, App: sc.App, Subject: "local-operator"}
	if _, e = service.ReceiveWatermark(ctx, p, raw, hex.EncodeToString(mac.Sum(nil)), "browser-watermark", "browser-watermark"); e != nil {
		t.Fatal(e)
	}
	if e = service.RunOnce(ctx, sc); e != nil {
		t.Fatal(e)
	}
	if e = service.RunOnce(ctx, sc); e != nil {
		t.Fatal(e)
	}
	daily, e := service.DailyRepo.Daily(ctx, store.DB, sc, mark.Day)
	if e != nil || daily.Status != "SETTLED" {
		t.Fatal("未完成结算", e)
	}
	hit := false
	for _, row := range daily.Results {
		if row.Result.Amount == "400" && len(row.Result.RiskDecisions) == 1 {
			hit = true
		}
	}
	if !hit {
		t.Fatal("去重未实际驱动最终计佣")
	}
	raw, _ = json.MarshalIndent(map[string]any{"daily": daily, "simulated_worker_time": simulated, "scope": "独立模拟租户，测试时钟推进次日01点；API和身份源使用实际时间"}, "", "  ")
	if e = os.WriteFile(filepath.Join(dir, "daily-evidence.json"), raw, 0600); e != nil {
		t.Fatal(e)
	}
}
