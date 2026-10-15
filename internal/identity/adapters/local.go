package adapters

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"strings"

	"github.com/ex-business/ExCommission/internal/identity/domain"
)

// LocalProvider 是仅用于本地沙箱的显式可信身份映射；不接受客户端自报角色和作用域。
type LocalProvider struct{ identities map[[32]byte]domain.Principal }

func NewLocalProvider(tokens map[string]domain.Principal) *LocalProvider {
	p := &LocalProvider{identities: make(map[[32]byte]domain.Principal)}
	for token, identity := range tokens {
		if len(token) >= 32 {
			p.identities[sha256.Sum256([]byte(token))] = identity
		}
	}
	return p
}
func (p *LocalProvider) Authenticate(_ context.Context, header string) (domain.Principal, error) {
	if !strings.HasPrefix(header, "Bearer ") {
		return domain.Principal{}, domain.ErrUnauthenticated
	}
	candidate := sha256.Sum256([]byte(strings.TrimPrefix(header, "Bearer ")))
	for hash, identity := range p.identities {
		if subtle.ConstantTimeCompare(candidate[:], hash[:]) == 1 {
			return identity, nil
		}
	}
	return domain.Principal{}, domain.ErrUnauthenticated
}
