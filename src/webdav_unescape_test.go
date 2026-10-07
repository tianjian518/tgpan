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
