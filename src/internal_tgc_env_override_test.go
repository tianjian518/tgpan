package tgc

import (
	"testing"

	"github.com/tgdrive/teldrive/internal/config"
)

// TestApplyEnvOverridesReplacesWebCredential 验证「网页版凭据被自动换掉」。
//
// 背景：Teldrive 上游默认 app-id=2496（web.telegram.org 网页版共享凭据），
// Telegram 官方明确网页版收不到登录验证码。启动时必须自动替换。
func TestApplyEnvOverridesReplacesWebCredential(t *testing.T) {
	cases := []struct {
		name    string
		in      config.TGConfig
		wantID  int
		wantHsh string
	}{
		{
			name:    "两个都是网页版凭据",
			in:      config.TGConfig{AppId: 2496, AppHash: "8da85b0d5bfe62527e5b244c209159c3"},
			wantID:  27335138,
			wantHsh: "2459555ba95421148c682e2dc3031bb6",
		},
		{
			name:    "只命中 app-id",
			in:      config.TGConfig{AppId: 2496, AppHash: "some-other-hash"},
			wantID:  27335138,
			wantHsh: "2459555ba95421148c682e2dc3031bb6",
		},
		{
			name:    "只命中 app-hash",
			in:      config.TGConfig{AppId: 999999, AppHash: "8da85b0d5bfe62527e5b244c209159c3"},
			wantID:  27335138,
			wantHsh: "2459555ba95421148c682e2dc3031bb6",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cfg := c.in
			ApplyEnvOverrides(&cfg)
			if cfg.AppId != c.wantID {
				t.Errorf("AppId = %d, want %d", cfg.AppId, c.wantID)
			}
			if cfg.AppHash != c.wantHsh {
				t.Errorf("AppHash = %q, want %q", cfg.AppHash, c.wantHsh)
			}
		})
	}
}

// TestApplyEnvOverridesKeepsUserCredential 验证「用户自己填的凭据不被覆盖」。
//
// 这是最要紧的一条：用户如果申请了自己的 api_id/api_hash，绝不能被程序改掉，
// 否则用户会看到「我明明填了自己的凭据，怎么没生效」。
func TestApplyEnvOverridesKeepsUserCredential(t *testing.T) {
	cfg := config.TGConfig{AppId: 1234567, AppHash: "my-own-hash"}
	ApplyEnvOverrides(&cfg)
	if cfg.AppId != 1234567 {
		t.Errorf("用户凭据被覆盖了：AppId = %d, want 1234567", cfg.AppId)
	}
	if cfg.AppHash != "my-own-hash" {
		t.Errorf("用户凭据被覆盖了：AppHash = %q, want %q", cfg.AppHash, "my-own-hash")
	}
}

// TestApplyEnvOverridesIdempotent 验证幂等性。
//
// 【为什么幂等很重要】
// 这个函数现在会在两处调用：启动时（cmd/run.go）和每次建 TG 客户端时
// （internal/tgc）。多次调用结果必须一致，否则「启动时显示一个值、
// 运行时用另一个值」的老问题会以新形式重现。
func TestApplyEnvOverridesIdempotent(t *testing.T) {
	cfg := config.TGConfig{AppId: 2496, AppHash: "8da85b0d5bfe62527e5b244c209159c3"}
	ApplyEnvOverrides(&cfg)
	first := cfg

	for i := 0; i < 5; i++ {
		ApplyEnvOverrides(&cfg)
	}

	if cfg.AppId != first.AppId || cfg.AppHash != first.AppHash {
		t.Errorf("多次调用结果不一致：第 1 次 %d/%q，第 6 次 %d/%q",
			first.AppId, first.AppHash, cfg.AppId, cfg.AppHash)
	}
}

// TestApplyEnvOverridesNilSafe 验证 nil 不 panic。
func TestApplyEnvOverridesNilSafe(t *testing.T) {
	ApplyEnvOverrides(nil) // 不 panic 即通过
}

// TestApplyEnvOverridesSetsRealDevice 验证默认切换为真实移动端身份。
func TestApplyEnvOverridesSetsRealDevice(t *testing.T) {
	cfg := config.TGConfig{AppId: 2496, AppHash: "8da85b0d5bfe62527e5b244c209159c3"}
	ApplyEnvOverrides(&cfg)

	if cfg.DeviceModel == "Firefox" || cfg.DeviceModel == "" {
		t.Errorf("设备身份没切换：DeviceModel = %q", cfg.DeviceModel)
	}
	if cfg.SystemVersion == "" {
		t.Error("SystemVersion 为空")
	}
}
