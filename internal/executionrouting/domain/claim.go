// Package domain 定义两条计佣链路共用的业务归属，不替代各引擎原有的验签和正文防重。
package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"

	foundation "github.com/ex-business/ExCommission/internal/platform/domain"
)

const General = "GENERAL"
const Affiliate = "AFFILIATE"

// Claim 的Kind必须为来源约定的标准事件名；别名在来源映射阶段规范化，不能在此猜测业务含义。
type Claim struct {
	Source, EventID, BusinessID, Kind, Engine string
	At                                        time.Time
}

var ErrOwned = errors.New("STATE_CONFLICT:EXECUTION_ENGINE_CONFLICT")

func (c Claim) Validate() error {
	for _, v := range []string{c.Source, c.EventID, c.BusinessID, c.Kind} {
		if !foundation.ValidID(v) || strings.TrimSpace(v) != v {
			return foundation.ErrValidation
		}
	}
	if c.Engine != General && c.Engine != Affiliate {
		return foundation.ErrValidation
	}
	return nil
}

// Keys 同时约束事件编号和业务单号，换事件编号不能绕过业务防重；JSON编码避免分隔符碰撞。
func (c Claim) Keys() [2]string {
	digest := func(parts ...string) string {
		raw, _ := json.Marshal(parts)
		sum := sha256.Sum256(raw)
		return hex.EncodeToString(sum[:])
	}
	return [2]string{digest("EVENT", c.Source, c.EventID), digest("BUSINESS", c.Source, c.Kind, c.BusinessID)}
}
