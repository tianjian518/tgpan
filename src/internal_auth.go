package auth

import (
	"context"
	"fmt"
	"strconv"

	"github.com/golang-jwt/jwt/v5"
	"github.com/ogen-go/ogen/ogenerrors"
	"github.com/tgdrive/teldrive/internal/api"
	"github.com/tgdrive/teldrive/internal/cache"
	"github.com/tgdrive/teldrive/internal/config"
	"github.com/tgdrive/teldrive/pkg/models"
	"github.com/tgdrive/teldrive/pkg/types"
	"gorm.io/gorm"
)

type authContextKey string

const authKey authContextKey = "authUser"

func Encode(secret string, claims *types.JWTClaims) (string, error) {

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)

	return token.SignedString([]byte(secret))
}

func Decode(secret string, token string) (*types.JWTClaims, error) {
	claims := &types.JWTClaims{}

	tkn, err := jwt.ParseWithClaims(token, claims, func(token *jwt.Token) (any, error) {
		// 必须锁定签名算法族。
		//
		// jwt/v5 对 alg=none 有默认防护，但如果 keyFunc 不校验算法，
		// 攻击者在某些实现差异下可以诱导服务端用非预期算法验签
		// （经典的算法混淆）。这里只接受 HMAC 系列（我们签发用的就是 HS256），
		// 其余一律拒绝。
		if _, ok := token.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", token.Header["alg"])
		}
		return []byte(secret), nil
	})
	if err != nil {
		return nil, err
	}
	if !tkn.Valid {
		return nil, fmt.Errorf("invalid token")
	}
	return claims, err
}

func GetUser(c context.Context) int64 {
	authUser, ok := c.Value(authKey).(*types.JWTClaims)
	if !ok || authUser == nil {
		return 0
	}
	userId, _ := strconv.ParseInt(authUser.Subject, 10, 64)
	return userId
}

func GetJWTUser(c context.Context) *types.JWTClaims {
	authUser, ok := c.Value(authKey).(*types.JWTClaims)
	if !ok {
		return nil
	}
	return authUser
}

func VerifyUser(ctx context.Context, db *gorm.DB, cache cache.Cacher, secret, authCookie string) (*types.JWTClaims, error) {
	claims, err := Decode(secret, authCookie)

	if err != nil {
		return nil, err
	}

	var session *models.Session

	session, err = GetSessionByHash(ctx, db, cache, claims.Hash)

	if err != nil {
		return nil, fmt.Errorf("invalid session")
	}

	claims.TgSession = session.Session

	return claims, nil
}

func GetSessionByHash(ctx context.Context, db *gorm.DB, cache cache.Cacher, hash string) (*models.Session, error) {
	var session models.Session
	key := fmt.Sprintf("sessions:%s", hash)

	err := cache.Get(ctx, key, &session)

	if err != nil {
		if err := db.Model(&models.Session{}).Where("hash = ?", hash).First(&session).Error; err != nil {
			return nil, err
		}
		cache.Set(ctx, key, &session, 0)
	}

	return &session, nil

}

type securityHandler struct {
	db    *gorm.DB
	cache cache.Cacher
	cfg   *config.JWTConfig

	// masterClaims 由闸门（gate）注入：当浏览器 Cookie 校验失败时，
	// 用服务端保存的 TG 主凭证兜底。
	//
	// 触发场景：已经扫码配对过 TG（主凭证落盘），但当前浏览器
	// 没带有效 Cookie —— 例如内网免密入口、或换了一台新设备。
	// 这时不再要求重新扫码，直接用主凭证放行。
	//
	// 返回 nil 表示当前没有可用主凭证（未配对），照常拒绝。
	masterClaims func() *types.JWTClaims
}

func (s *securityHandler) SetMasterClaimsProvider(f func() *types.JWTClaims) {
	s.masterClaims = f
}

func (s *securityHandler) HandleApiKeyAuth(ctx context.Context, operationName api.OperationName, t api.ApiKeyAuth) (context.Context, error) {
	return s.handleAuth(ctx, t.APIKey)
}

func (s *securityHandler) HandleBearerAuth(ctx context.Context, operationName api.OperationName, t api.BearerAuth) (context.Context, error) {
	return s.handleAuth(ctx, t.Token)
}

func (s *securityHandler) handleAuth(ctx context.Context, token string) (context.Context, error) {
	claims, err := VerifyUser(ctx, s.db, s.cache, s.cfg.Secret, token)
	if err != nil {
		// 浏览器 Cookie 不可用（缺失 / 过期 / 换设备）→ 尝试主凭证兜底。
		if s.masterClaims != nil {
			if mc := s.masterClaims(); mc != nil {
				return context.WithValue(ctx, authKey, mc), nil
			}
		}
		return nil, &ogenerrors.SecurityError{Err: err}
	}
	return context.WithValue(ctx, authKey, claims), nil
}

func NewSecurityHandler(db *gorm.DB, cache cache.Cacher, cfg *config.JWTConfig) *securityHandler {
	return &securityHandler{db: db, cache: cache, cfg: cfg}
}

// WithUser 把已校验的 JWT claims 放进 context，供 GetUser / GetJWTUser 使用。
// 用于自行处理 HTTP 请求（不经 ogen 安全中间件）的场景。
func WithUser(ctx context.Context, claims *types.JWTClaims) context.Context {
	return context.WithValue(ctx, authKey, claims)
}

var _ api.SecurityHandler = (*securityHandler)(nil)
