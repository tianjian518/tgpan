package services

import (
	"crypto/rand"
	"crypto/rsa"
	"strings"
	"testing"

	"github.com/golang-jwt/jwt/v5"
	"github.com/tgdrive/teldrive/internal/auth"
)

// ===========================================================================
//  安全修复的回归测试
//
//  这些用例守护的是「修过的坑不能再回来」。凡是修过的安全问题，
//  这里都要有一条用例能把它钉住。
// ===========================================================================

// TestJWTSigningMethodLocked 验证 JWT 解析锁定了 HMAC 算法族。
//
// 背景与说明（重要，避免误判严重性）：
//
//	原实现里 keyFunc 直接返回 secret，不检查 token.Method。
//	实测确认：jwt/v5 本身已经默认拒绝 alg=none
//	（"'none' signature type is not allowed"），
//	且用 []byte 密钥时 RS256 也无法通过验签。
//	所以这不是一个「当前可被利用」的漏洞，属**纵深防御**性质：
//	显式锁定算法族，避免日后换库/改参数时悄悄退化。
//
// 这条用例的价值在于：把「只接受 HMAC」这个约定固化下来，
// 以后谁改坏了 keyFunc，测试会立刻报警。
func TestJWTSigningMethodLocked(t *testing.T) {
	secret := "test-secret-key-for-unit-test"

	// 1. HS256 正常签发 -> 应当解析成功
	tok := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{"sub": "42"})
	signed, err := tok.SignedString([]byte(secret))
	if err != nil {
		t.Fatalf("签发 HS256 token 失败: %v", err)
	}
	if _, err := auth.Decode(secret, signed); err != nil {
		t.Errorf("HS256 token 应该解析成功，却报错: %v", err)
	}

	// 2. alg=none 的未签名 token -> 必须拒绝
	//    手工构造 header.payload. 形式（签名段为空）
	noneTok := "eyJhbGciOiJub25lIiwidHlwIjoiSldUIn0.eyJzdWIiOiI0MiJ9."
	if _, err := auth.Decode(secret, noneTok); err == nil {
		t.Error("alg=none 的 token 必须被拒绝，却解析成功了")
	}

	// 3. 篡改签名的 token -> 必须拒绝
	if _, err := auth.Decode(secret, signed+"x"); err == nil {
		t.Error("签名被篡改的 token 必须被拒绝")
	}

	// 4. 用错误 secret 签发 -> 必须拒绝
	other, _ := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{"sub": "42"}).
		SignedString([]byte("wrong-secret"))
	if _, err := auth.Decode(secret, other); err == nil {
		t.Error("用错误密钥签发的 token 必须被拒绝")
	}

	// 5. 非 HMAC 算法（RS256）-> 必须拒绝。
	//
	//    这是本修复真正防住的场景：如果 keyFunc 不校验算法，
	//    服务端可能被诱导用公钥当 HMAC 密钥去验 RS256 签名
	//    （经典的 RSA/HMAC 算法混淆攻击）。我们只签发 HS256，
	//    所以遇到 RS256 一律拒绝。
	rsaKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("生成 RSA 密钥失败: %v", err)
	}
	rsTok, err := jwt.NewWithClaims(jwt.SigningMethodRS256, jwt.MapClaims{"sub": "42"}).
		SignedString(rsaKey)
	if err != nil {
		t.Fatalf("签发 RS256 token 失败: %v", err)
	}
	if _, err := auth.Decode(secret, rsTok); err == nil {
		t.Error("RS256 token 必须被拒绝（只接受 HMAC 系列），却解析成功了")
	}
}

// TestDummyBcryptHashIsValidFormat 确保假哈希的格式是合法的 bcrypt。
//
// 这个常量用于「用户名不存在时也跑一次 bcrypt」来抗时序攻击。
// 如果它格式非法，bcrypt 会立即报错返回，耗时不增反降 —— 抗时序就失效了。
func TestDummyBcryptHashIsValidFormat(t *testing.T) {
	if !strings.HasPrefix(dummyBcryptHash, "$2a$") &&
		!strings.HasPrefix(dummyBcryptHash, "$2b$") {
		t.Errorf("dummyBcryptHash 不是合法的 bcrypt 格式: %q", dummyBcryptHash)
	}
	// bcrypt 哈希长度固定 60 字符
	if len(dummyBcryptHash) != 60 {
		t.Errorf("bcrypt 哈希应为 60 字符，实际 %d: %q",
			len(dummyBcryptHash), dummyBcryptHash)
	}
}

// TestStreamCacheScopeIsolated 验证流式接口的缓存不能被跨用户命中。
//
// 背景：这是 H1（跨用户读取文件）修复的一部分。
// 缓存是按 key 全局共享的 —— 如果不同用户用同一个 fileId 拿到同一个 key，
// 后到的用户会直接命中先到用户的缓存条目，从而绕过数据库里的归属校验。
//
// 这条用例保证：只要有用户身份，缓存 key 就一定带身份维度；
// 不同用户的 key 必须不同。
func TestStreamCacheScopeIsolated(t *testing.T) {
	fileID := "0192a1b2-c3d4-7e5f-8a9b-0c1d2e3f4a5b"

	scopeA := streamCacheScope(1001, "")
	scopeB := streamCacheScope(2002, "")
	scopeAnon := streamCacheScope(0, "sharehash123")

	if scopeA == scopeB {
		t.Errorf("不同用户拿到了同一个缓存维度: %q —— 会互相命中缓存", scopeA)
	}
	if scopeA == scopeAnon || scopeB == scopeAnon {
		t.Error("匿名直链的缓存维度与登录用户重合了")
	}
	if scopeA != "1001" {
		t.Errorf("用户 1001 的缓存维度应为 \"1001\"，实际 %q", scopeA)
	}
	if !strings.HasPrefix(scopeAnon, "hash:") {
		t.Errorf("匿名直链的缓存维度应以 hash: 开头，实际 %q", scopeAnon)
	}

	// 不同分享 hash 之间也不能互相命中
	if streamCacheScope(0, "hashA") == streamCacheScope(0, "hashB") {
		t.Error("不同分享 hash 拿到了同一个缓存维度")
	}

	// 缓存维度必须随 fileId 区分（这是 cache.Key 的职责，这里只是确认拼法）
	k1 := fileID + "|" + scopeA
	k2 := fileID + "|" + scopeB
	if k1 == k2 {
		t.Error("拼出的缓存键相同，隔离失效")
	}
}

// 前端「剧集」页签要靠 Episode 字段来排序和显示；如果是 0，
// 说明这个文件没被识别成该剧的集数（可能是被误放进来的别的文件）。
func TestSeriesFileItemEpisodeParsing(t *testing.T) {
	cases := []struct {
		name    string
		title   string
		wantEp  int
		matched bool
	}{
		{"狂飙 S01E05.mp4", "狂飙", 5, true},
		{"狂飙 S01E12.mp4", "狂飙", 12, true},
		{"狂飙 S02E03.mp4", "狂飙", 3, true},
		{"别的剧 S01E01.mp4", "狂飙", 0, false}, // 剧名对不上，不算这一集的
		{"花絮.mp4", "狂飙", 0, false},         // 认不出集数
	}

	for _, c := range cases {
		ep := ParseEpisode(c.name)
		var got int
		matched := ep.Ok && ep.Title == c.title
		if matched {
			got = ep.Episode
		}
		if matched != c.matched {
			t.Errorf("%q vs 剧名 %q: matched=%v, 期望 %v",
				c.name, c.title, matched, c.matched)
			continue
		}
		if got != c.wantEp {
			t.Errorf("%q: 集号=%d, 期望 %d", c.name, got, c.wantEp)
		}
	}
}
