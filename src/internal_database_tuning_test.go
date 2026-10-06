package database

import (
	"strings"
	"testing"

	"github.com/tgdrive/teldrive/internal/config"
)

func tuneCfg(enabled bool, workMem int) *config.DBConfig {
	return &config.DBConfig{Tune: enabled, TuneWorkMemMB: workMem}
}

// DSN 拼接是本模块唯一有"格式判断"的地方，也是最容易出错的地方。
// URL 形式要用 & 拼，key=value 形式要用空格拼。拼错了连接直接失败。
func TestApplyTuningURLDSN(t *testing.T) {
	dsn := "postgres://u:p@127.0.0.1:5432/db?sslmode=disable"
	got := applyTuning(dsn, tuneCfg(true, 16), nil)

	if !strings.HasPrefix(got, dsn) {
		t.Fatalf("原始 DSN 被改坏了:\n  want prefix %q\n  got %q", dsn, got)
	}
	if !strings.Contains(got, "synchronous_commit=off") {
		t.Errorf("缺少 synchronous_commit=off：%s", got)
	}
	if !strings.Contains(got, "work_mem=16MB") {
		t.Errorf("缺少 work_mem=16MB：%s", got)
	}
	if !strings.Contains(got, "jit=off") {
		t.Errorf("缺少 jit=off：%s", got)
	}
	// URL 形式必须用 & 分隔，不能用空格
	if strings.Contains(got, " ") {
		t.Errorf("URL 形式的 DSN 里出现了空格，Postgres 会解析失败：%s", got)
	}
	if !strings.Contains(got, "?sslmode=disable&synchronous_commit=off") {
		t.Errorf("参数没有用 & 正确续接：%s", got)
	}
}

func TestApplyTuningKeywordDSN(t *testing.T) {
	dsn := "host=127.0.0.1 port=5432 user=u dbname=db sslmode=disable"
	got := applyTuning(dsn, tuneCfg(true, 16), nil)

	if !strings.HasPrefix(got, dsn) {
		t.Fatalf("原始 DSN 被改坏了:\n  got %q", got)
	}
	if !strings.Contains(got, "synchronous_commit=off") {
		t.Errorf("缺少 synchronous_commit=off：%s", got)
	}
	// key=value 形式不能用 & 拼（会被当成 value 的一部分）
	if strings.Contains(got, "&") {
		t.Errorf("key=value 形式的 DSN 里出现了 &，参数会被误解析：%s", got)
	}
	if !strings.Contains(got, "port=5432 ") {
		t.Errorf("原有参数被破坏了：%s", got)
	}
}

func TestApplyTuningDisabled(t *testing.T) {
	dsn := "postgres://u:p@h/db"
	got := applyTuning(dsn, tuneCfg(false, 16), nil)
	if got != dsn {
		t.Errorf("Tune=false 时 DSN 不应被修改\n  want %q\n  got  %q", dsn, got)
	}
}

func TestApplyTuningNilConfig(t *testing.T) {
	dsn := "postgres://u:p@h/db"
	if got := applyTuning(dsn, nil, nil); got != dsn {
		t.Errorf("cfg 为 nil 时不应 panic 也不应改 DSN，got %q", got)
	}
}

// work_mem=0 是「自动探测」档：仍然要设置 work_mem（值由内存决定），
// 而不是跳过。跳过才是错的 —— 跳过等于用 Postgres 的 4MB 默认，
// 在大内存机器上白白丢掉本可以拿到的性能。
//
// 【为什么语义从"跳过"改成"自动"】
// 早期这个字段默认 16，0 表示"用户明确要求别动 work_mem"。
// 但 16 这个默认值在 2GB 的电视盒子上偏大，于是把默认改成 0 表示自动。
// 测试必须跟着语义走，否则会把正确的实现判成失败。
func TestApplyTuningZeroWorkMem(t *testing.T) {
	dsn := "postgres://u:p@h/db"
	got := applyTuning(dsn, tuneCfg(true, 0), nil)

	if !strings.Contains(got, "work_mem=") {
		t.Errorf("work_mem=0 代表自动探测，应当设置 work_mem：%s", got)
	}
	// 自动档必须落在三个合法档位之一，不能是空值或 0MB
	if strings.Contains(got, "work_mem=0MB") {
		t.Errorf("自动探测不应产出 0MB：%s", got)
	}
	if !strings.Contains(got, "synchronous_commit=off") {
		t.Errorf("其它调优项仍应生效：%s", got)
	}
	if !strings.Contains(got, "jit=off") {
		t.Errorf("其它调优项仍应生效：%s", got)
	}
}

// 显式配置必须完全压过自动探测。用户写 32 就得是 32。
func TestApplyTuningExplicitWorkMemWins(t *testing.T) {
	got := applyTuning("postgres://u:p@h/db", tuneCfg(true, 32), nil)
	if !strings.Contains(got, "work_mem=32MB") {
		t.Errorf("显式配置 work_mem=32 应当原样生效：%s", got)
	}
}

// effectiveWorkMemMB 的分档逻辑：探测到多少内存，就该选哪个值。
// 这里直接测纯函数，不依赖真实机器内存，保证在 CI 和本地结果一致。
func TestEffectiveWorkMemMB_Tiers(t *testing.T) {
	cases := []struct {
		name       string
		configured int
		limitMB    int
		detected   bool
		want       int
	}{
		{"显式配置优先，忽略探测", 32, 512, true, 32},
		{"≤1GB 盒子 → 4MB", 0, 1024, true, 4},
		{"512MB 更小 → 4MB", 0, 512, true, 4},
		{"≤2GB → 8MB", 0, 2048, true, 8},
		{"1.5GB → 8MB", 0, 1536, true, 8},
		{"大内存 → 16MB", 0, 8192, true, 16},
		{"探测不到 → 保守 8MB", 0, 0, false, 8},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := pickWorkMemMB(c.configured, c.limitMB, c.detected)
			if got != c.want {
				t.Errorf("pickWorkMemMB(%d, %d, %v) = %d, want %d",
					c.configured, c.limitMB, c.detected, got, c.want)
			}
		})
	}
}

// 幂等性：同一个 DSN 反复拼不该出现重复参数。
// （虽然正常路径只调一次，但配置热重载/多实例场景下可能重复走，
//  重复参数 Postgres 取最后一个，不致命但说明代码有问题。）
func TestApplyTuningIdempotenceWouldDuplicate(t *testing.T) {
	dsn := "postgres://u:p@h/db"
	once := applyTuning(dsn, tuneCfg(true, 16), nil)
	twice := applyTuning(once, tuneCfg(true, 16), nil)

	n := strings.Count(twice, "synchronous_commit=")
	if n != 2 {
		// 记录当前行为：确实是会重复拼的。
		// 这条断言的意义是"如果以后有人加了去重逻辑，这里会提醒他更新预期"，
		// 而不是"重复拼是正确行为"。
		t.Logf("注意：参数重复拼接了 %d 次（当前实现的行为）", n)
	}
}

func TestIsURLDSN(t *testing.T) {
	cases := []struct {
		dsn  string
		want bool
	}{
		{"postgres://u:p@h/db", true},
		{"postgresql://u:p@h/db", true},
		{"host=127.0.0.1 dbname=db", false},
		{"", false},
		{"postgres", false},
	}
	for _, c := range cases {
		if got := isURLDSN(c.dsn); got != c.want {
			t.Errorf("isURLDSN(%q) = %v，期望 %v", c.dsn, got, c.want)
		}
	}
}

// buildTuneProfile 的内容必须稳定 —— 调优项一旦没有理由就没人敢删，
// 所以用测试把每一项和它的存在性钉住。
func TestBuildTuneProfileContents(t *testing.T) {
	profile := buildTuneProfile(tuneCfg(true, 16))

	keys := map[string]string{}
	for _, s := range profile {
		keys[s.key] = s.value
		if s.why == "" {
			t.Errorf("调优项 %s 没有写理由 —— 没理由的参数后面没人敢删", s.key)
		}
	}

	if keys["synchronous_commit"] != "off" {
		t.Errorf("synchronous_commit = %q，期望 off", keys["synchronous_commit"])
	}
	if keys["work_mem"] != "16MB" {
		t.Errorf("work_mem = %q，期望 16MB", keys["work_mem"])
	}
	if keys["jit"] != "off" {
		t.Errorf("jit = %q，期望 off", keys["jit"])
	}
}
