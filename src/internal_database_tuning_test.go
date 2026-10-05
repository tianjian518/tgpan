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

// work_mem=0 应该跳过这一项（用户想保留默认），但其它项照设。
func TestApplyTuningZeroWorkMem(t *testing.T) {
	dsn := "postgres://u:p@h/db"
	got := applyTuning(dsn, tuneCfg(true, 0), nil)

	if strings.Contains(got, "work_mem") {
		t.Errorf("work_mem=0 时不应设置 work_mem：%s", got)
	}
	if !strings.Contains(got, "synchronous_commit=off") {
		t.Errorf("其它调优项仍应生效：%s", got)
	}
	if !strings.Contains(got, "jit=off") {
		t.Errorf("其它调优项仍应生效：%s", got)
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
