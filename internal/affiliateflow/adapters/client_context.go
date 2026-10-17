package adapters

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	principal "github.com/ex-business/ExCommission/internal/identity/domain"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// ClaimClientIP 只接受BFF签名上下文，不能从X-Forwarded-For或浏览器自填字段推断领取IP。
// 绑定身份令牌、路径、幂等键与请求摘要，重放仅可能命中原命令，不能借签名领取另一类奖励。
func ClaimClientIP(r *http.Request, p principal.Principal, key []byte, now time.Time) (string, error) {
	deny := errors.New("PERMISSION_DENIED")
	ip := r.Header.Get("X-Affiliate-Client-IP")
	at := r.Header.Get("X-Affiliate-Client-Time")
	digest := r.Header.Get("X-Affiliate-Body-SHA256")
	n, e := strconv.ParseInt(at, 10, 64)
	if e != nil || n < now.Unix()-30 || n > now.Unix()+30 || len(key) < 32 || net.ParseIP(ip) == nil || len(digest) != 64 {
		return "", deny
	}
	token := sha256.Sum256([]byte(r.Header.Get("Authorization")))
	parts := []string{"affiliate-client-v1", p.Tenant, p.App, p.Subject, hex.EncodeToString(token[:]), r.Method, r.URL.RequestURI(), r.Header.Get("Idempotency-Key"), at, ip, digest}
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(strings.Join(parts, "\n")))
	signature, e := hex.DecodeString(r.Header.Get("X-Affiliate-Client-Signature"))
	if e != nil || !hmac.Equal(signature, mac.Sum(nil)) {
		return "", deny
	}
	return net.ParseIP(ip).String(), nil
}
