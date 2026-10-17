package runtime

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestAffiliateAcceptanceProcess 仅编译进测试二进制，为隔离本地全链路验收提供业务时钟。
// 启动真实 API/worker 主循环；不直接调用日结、不写奖励或钱包，生产二进制没有此入口。
func TestAffiliateAcceptanceProcess(t *testing.T) {
	dir := os.Getenv("AFFILIATE_PROCESS_ACCEPTANCE_DIR")
	if dir == "" {
		t.Skip("仅显式进程级验收启用")
	}
	role := os.Getenv("AFFILIATE_PROCESS_ACCEPTANCE_ROLE")
	if role != "api" && role != "worker" {
		t.Fatal("验收进程角色无效")
	}
	raw, err := os.ReadFile(filepath.Join(dir, role+"-runtime.json"))
	if err != nil {
		t.Fatal(err)
	}
	var cfg map[string]string
	if json.Unmarshal(raw, &cfg) != nil || cfg["APP_MODE"] != "local" || !strings.HasPrefix(cfg["LOCAL_TENANT"], "fullchain-") || !strings.HasPrefix(cfg["HTTP_ADDR"], "127.0.0.1:") {
		t.Fatal("拒绝非隔离本地验收配置")
	}
	for k, v := range cfg {
		t.Setenv(k, v)
	}
	previous := timeSource
	defer func() { timeSource = previous }()
	timeSource = func() time.Time {
		raw, err := os.ReadFile(filepath.Join(dir, "business-clock.txt"))
		if err != nil {
			panic("验收业务时钟读取失败")
		}
		if strings.TrimSpace(string(raw)) == "REAL" {
			return time.Now()
		}
		now, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(string(raw)))
		if err != nil {
			panic("验收业务时钟格式错误")
		}
		return now
	}
	if err := Run(role); err != nil {
		t.Fatal(err)
	}
}
