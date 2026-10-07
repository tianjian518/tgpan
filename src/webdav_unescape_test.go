package services

import "testing"

func TestUnescapeTolerant(t *testing.T) {
	cases := []struct{ in, want string }{
		// 正常编码
		{"/webdav/%E7%94%B5%E5%BD%B1", "/webdav/电影"},
		// 原始 UTF-8（播放器常这么发）
		{"/webdav/电影", "/webdav/电影"},
		// 百分号 —— 修复前会整个失败
		{"/webdav/100%某剧", "/webdav/100%某剧"},
		{"/webdav/50%off.mp4", "/webdav/50%off.mp4"},
		{"/webdav/评分98%.mkv", "/webdav/评分98%.mkv"},
		// 编码过的百分号必须还原成 %
		{"/webdav/100%25某剧", "/webdav/100%某剧"},
		// 空格
		{"/webdav/%20", "/webdav/ "},
		// % 在末尾
		{"/webdav/abc%", "/webdav/abc%"},
		{"/webdav/abc%2", "/webdav/abc%2"},
		// 混合：部分编码 + 部分裸 %
		{"/webdav/%E7%94%B5%E5%BD%B1/100%某剧", "/webdav/电影/100%某剧"},
		// 没有 %
		{"/webdav/plain.mp4", "/webdav/plain.mp4"},
		// 空
		{"", ""},
	}
	for _, c := range cases {
		got := unescapeTolerant(c.in)
		if got != c.want {
			t.Errorf("unescapeTolerant(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
// 直接验证 parsePath 对含 % 的路径的处理
func TestParsePathWithPercent(t *testing.T) {
	h := &webdavHandler{}
	cases := []struct{ in, want string }{
		{"/webdav/100%25%E6%9F%90%E5%89%A7", "100%某剧"},   // %25 编码
		{"/webdav/100%某剧", "100%某剧"},                     // 裸 %
		{"/webdav/50%off.mp4", "50%off.mp4"},                 // 裸 %
		{"/webdav/%E6%99%AE%E9%80%9A%E7%94%B5%E5%BD%B1.mp4", "普通电影.mp4"},
		{"/webdav/电影/a.mp4", "电影/a.mp4"},
		{"/api/webdav/100%25test", "100%test"},               // /api 前缀 + %
	}
	for _, c := range cases {
		got, err := h.parsePath(c.in)
		if err != nil {
			t.Errorf("parsePath(%q) 报错: %v", c.in, err)
			continue
		}
		if got != c.want {
			t.Errorf("parsePath(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// 路径穿越必须仍然被拦住（安全性不能因为容错而退化）
func TestParsePathTraversal(t *testing.T) {
	h := &webdavHandler{}
	bad := []string{
		"/webdav/../../etc/passwd",
		"/webdav/a/../../../etc",
	}
	for _, in := range bad {
		got, err := h.parsePath(in)
		if err == nil && got != "" && got[0] != '.' && got != "etc/passwd" {
			t.Logf("parsePath(%q) = %q (无 err)", in, got)
		}
		if err == nil {
			t.Errorf("parsePath(%q) 未拦截穿越，返回 %q", in, got)
		}
	}
}
