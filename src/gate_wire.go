package services

import "github.com/tgdrive/teldrive/pkg/types"

// MasterClaimsProvider 是鉴权层需要的「主凭证提供者」接口。
// 定义在这里（而不是 internal/auth）是为了避免包循环依赖：
// internal/auth 不该反向 import pkg/services。
type MasterClaimsProvider interface {
	SetMasterClaimsProvider(func() *types.JWTClaims)
}

// WireMasterClaims 把闸门的主凭证接到鉴权处理器上。
//
// 参数 h 传 *auth.securityHandler 的接口形态（见 internal/auth）。
// apiSrv.gate 为 nil（闸门未启用）时，provider 返回 nil，鉴权行为与原来完全一致。
func WireMasterClaims(h MasterClaimsProvider, apiSrv *apiService) {
	if h == nil {
		return
	}
	h.SetMasterClaimsProvider(func() *types.JWTClaims {
		if apiSrv == nil || apiSrv.gate == nil {
			return nil
		}
		g := apiSrv.gate
		session, userID, ok := g.MasterCredentials()
		if !ok || session == "" || userID == 0 {
			return nil
		}
		return buildMasterClaims(session, userID, g.MasterHash())
	})
}
