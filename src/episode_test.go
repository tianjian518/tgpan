package services

import "testing"

func TestParseEpisode(t *testing.T) {
	cases := []struct {
		in     string
		ok     bool
		title  string
		season int
		ep     int
		why    string
	}{
		// ---- 应当识别成功 ----
		{"狂飙 第05集.mp4", true, "狂飙", 1, 5, "中文第N集"},
		{"狂飙 第5集.mp4", true, "狂飙", 1, 5, "中文第N集（不补零）"},
		{"狂飙_第05集_高清.mp4", true, "狂飙", 1, 5, "下划线分隔"},
		{"狂飙.第05话.mkv", true, "狂飙", 1, 5, "话 也认"},
		{"狂飙 EP05.mp4", true, "狂飙", 1, 5, "EP 前缀"},
		{"狂飙.EP05.1080p.mp4", true, "狂飙", 1, 5, "EP + 画质"},
		{"狂飙 E05.mp4", true, "狂飙", 1, 5, "E 前缀"},
		{"Breaking.Bad.S01E05.1080p.WEB-DL.mp4", true, "Breaking Bad", 1, 5, "标准美剧格式"},
		{"Breaking.Bad.s1e5.mp4", true, "Breaking Bad", 1, 5, "小写 s/e"},
		{"权力的游戏 S02E03 1080p.mkv", true, "权力的游戏", 2, 3, "中文名 + 第二季"},
		{"狂飙 第一季 第05集.mp4", true, "狂飙", 1, 5, "中文季 + 中文集"},
		{"狂飙 第二季 第12集.mp4", true, "狂飙", 2, 12, "第二季"},
		{"狂飙 第12季 第05集.mp4", true, "狂飙", 12, 5, "阿拉伯数字季"},
		{"狂飙 05集.mp4", true, "狂飙", 1, 5, "简写 N集"},
		{"狂飙 [05].mp4", true, "狂飙", 1, 5, "方括号集数"},
		{"狂飙 [E05].mp4", true, "狂飙", 1, 5, "方括号 E 集数"},
		{"【某某压制】狂飙 第05集.mp4", true, "狂飙", 1, 5, "带发布组水印"},
		{"[FHD]狂飙.EP05.mp4", true, "狂飙", 1, 5, "带画质前缀"},
		{"狂飙 第05集 国语中字.mp4", true, "狂飙", 1, 5, "带语言标签"},

		// ---- 应当识别失败（宁可漏判）----
		{"狂飙 05.mp4", false, "", 0, 0, "纯数字结尾，分不清是第几集还是别的"},
		{"05.mp4", false, "", 0, 0, "纯数字"},
		{"2024年春晚.mp4", false, "", 0, 0, "年份不能当集数"},
		{"复仇者联盟4终局之战.mp4", false, "", 0, 0, "电影，没有集数"},
		{"流浪地球2.2023.1080p.mp4", false, "", 0, 0, "带数字的电影"},
		{"E.mp4", false, "", 0, 0, "只有一个字母"},
		{"第05集.mp4", false, "", 0, 0, "只有集数没有剧名"},
		{"", false, "", 0, 0, "空字符串"},
		{"狂飙 第9999集.mp4", false, "", 0, 0, "集数超上限，判为误匹配"},
	}

	for _, c := range cases {
		got := ParseEpisode(c.in)
		if got.Ok != c.ok {
			t.Errorf("[%s] ParseEpisode(%q).Ok = %v, 期望 %v（%s）",
				c.why, c.in, got.Ok, c.ok, c.why)
			continue
		}
		if !c.ok {
			continue
		}
		if got.Title != c.title {
			t.Errorf("[%s] ParseEpisode(%q).Title = %q, 期望 %q",
				c.why, c.in, got.Title, c.title)
		}
		if got.Season != c.season {
			t.Errorf("[%s] ParseEpisode(%q).Season = %d, 期望 %d",
				c.why, c.in, got.Season, c.season)
		}
		if got.Episode != c.ep {
			t.Errorf("[%s] ParseEpisode(%q).Episode = %d, 期望 %d",
				c.why, c.in, got.Episode, c.ep)
		}
	}
}

func TestFormatEpisodeName(t *testing.T) {
	cases := []struct {
		title  string
		season int
		ep     int
		ext    string
		want   string
	}{
		{"狂飙", 1, 5, ".mp4", "狂飙 S01E05.mp4"},
		{"狂飙", 1, 12, ".mp4", "狂飙 S01E12.mp4"},
		{"狂飙", 2, 3, ".mkv", "狂飙 S02E03.mkv"},
		{"Breaking Bad", 1, 1, ".mp4", "Breaking Bad S01E01.mp4"},
		{"权力的游戏", 8, 6, ".mp4", "权力的游戏 S08E06.mp4"},
	}

	for _, c := range cases {
		got := FormatEpisodeName(c.title, c.season, c.ep, c.ext)
		if got != c.want {
			t.Errorf("FormatEpisodeName(%q,%d,%d,%q) = %q, 期望 %q",
				c.title, c.season, c.ep, c.ext, got, c.want)
		}
	}
}

func TestChineseNumToInt(t *testing.T) {
	cases := map[string]int{
		"一": 1, "二": 2, "十": 10, "十一": 11, "二十": 20,
		"二十三": 23, "三": 3, "1": 1, "12": 12, "五": 5,
	}
	for in, want := range cases {
		if got := chineseNumToInt(in); got != want {
			t.Errorf("chineseNumToInt(%q) = %d, 期望 %d", in, got, want)
		}
	}
}

// TestResolveEpisodeFilename 覆盖「文件名认不出、回头看配文」这条链。
// 影视频道里大量文件叫「1.mp4」「05.mp4」，剧名和集数只在配文里，
// 所以这段逻辑必须有测试兜着。
func TestResolveEpisodeFilename(t *testing.T) {
	cases := []struct {
		fname    string
		caption  string
		wantOK   bool
		wantName string
		why      string
	}{
		{"1.mp4", "狂飙 第05集 1080P", true, "狂飙 S01E05.mp4", "文件名没信息，靠配文"},
		{"05.mp4", "【更新】狂飙 第12集", true, "狂飙 S01E12.mp4", "配文带水印"},
		{"狂飙 第05集.mp4", "随便写点什么", true, "狂飙 S01E05.mp4", "文件名优先"},
		{"狂飙 第05集.mp4", "另外一部剧 第09集", true, "狂飙 S01E05.mp4", "文件名优先，配文不覆盖"},
		{"复仇者联盟4.mp4", "复仇者联盟4 终局之战 4K", false, "复仇者联盟4.mp4", "两边都没集数，保持原名"},
		{"狂飙 S02E03.mkv", "", true, "狂飙 S02E03.mkv", "季号保留"},
		{"1.mp4", "", false, "1.mp4", "没有配文可用"},
		// 配文只有集数没有剧名 —— 刻意不归类。
		// 否则「第05集」会建出一个叫「第05集」的文件夹，或者跟别的剧的
		// 第 05 集混到一起，比不整理还糟。
		{"1.mp4", "第05集", false, "1.mp4", "配文只有集数没剧名，拒绝归类"},
	}
	for _, c := range cases {
		got := ResolveEpisodeFilename(c.fname, c.caption)
		if got.Ok != c.wantOK {
			t.Errorf("ResolveEpisodeFilename(%q,%q).Ok = %v, 期望 %v（%s）",
				c.fname, c.caption, got.Ok, c.wantOK, c.why)
			continue
		}
		if got.Name != c.wantName {
			t.Errorf("ResolveEpisodeFilename(%q,%q).Name = %q, 期望 %q（%s）",
				c.fname, c.caption, got.Name, c.wantName, c.why)
		}
	}
}

// TestExtractCaptionLine 校验配文取行的行为：影视频道的配文常常是多行
// 的「剧名 / 集数 / 剧情简介」，只有前一两行是有效信息。
func TestExtractCaptionLine(t *testing.T) {
	cases := []struct {
		in   string
		want string
		why  string
	}{
		{"狂飙 第05集\n剧情简介：安欣和高启强...", "狂飙 第05集", "只取第一行"},
		{"\n\n  狂飙 第05集  \n简介", "狂飙 第05集", "跳过空行"},
		{"狂飙 第05集", "狂飙 第05集", "单行原样返回"},
		{"", "", "空配文"},
	}
	for _, c := range cases {
		if got := extractCaptionLine(c.in); got != c.want {
			t.Errorf("extractCaptionLine(%q) = %q, 期望 %q（%s）", c.in, got, c.want, c.why)
		}
	}
}

// ===========================================================================
//  影视分类测试
// ===========================================================================

func TestClassifyMedia(t *testing.T) {
	cases := []struct {
		name     string
		fname    string
		caption  string
		isSeries bool
		want     MediaKind
	}{
		// 认出集数 → 电视剧（最高优先级，除非是动漫）
		{"剧集识别成功", "狂飙 S01E05.mp4", "", true, KindTV},
		{"剧集识别成功-无副标题", "狂飙 第05集.mp4", "", true, KindTV},

		// 动漫优先于电视剧
		//   注意：光看「火影忍者 S01E01」是认不出动漫的——文件名里没有动漫
		//   关键词，我们宁可判成电视剧也不瞎猜。动漫要靠配文/标题里的关键词。
		{"动漫靠配文", "火影忍者 S01E01.mp4", "#动漫 #火影", true, KindAnime},
		{"动漫关键词", "咒术回战 动漫.mp4", "", false, KindAnime},
		{"番剧关键词", "间谍过家家 番剧 S02E01.mp4", "", true, KindAnime},
		{"动画英文", "Spy x Family anime S01E01.mkv", "", true, KindAnime},
		{"剧场版", "名侦探柯南 剧场版 万圣节的新娘.mp4", "", false, KindAnime},

		// 电影特征词
		{"电影关键词", "流浪地球2 电影.mp4", "", false, KindMovie},
		{"蓝光", "复仇者联盟4 蓝光原盘.mkv", "", false, KindMovie},
		{"年份+分辨率", "Dune.2021.2160p.BluRay.x265.mkv", "", false, KindMovie},
		{"WEB-DL", "The.Matrix.1999.WEB-DL.1080p.mkv", "", false, KindMovie},
		{"枪版", "某某大片 枪版.mp4", "", false, KindMovie},

		// 电视剧特征词
		{"全集", "琅琊榜 全集.mp4", "", false, KindTV},
		{"完结", "某某剧 完结.mkv", "", false, KindTV},
		{"更新至", "某某剧 更新至20集.mkv", "", false, KindTV},
		{"第二季", "某某剧 第二季.mp4", "", false, KindTV},

		// 中文片名、无集数季数标记 → 倾向电影
		{"纯中文片名", "肖申克的救赎.mp4", "", false, KindMovie},
		{"纯中文片名2", "霸王别姬.mkv", "", false, KindMovie},

		// 含「集」或「季」的认不出的 → 其他（不瞎猜成电影）
		{"有集字但没认出", "某某节目 精彩片段集锦.mp4", "", false, KindOther},
	}

	for _, c := range cases {
		got := ClassifyMedia(c.fname, c.caption, c.isSeries)
		if got != c.want {
			t.Errorf("%s: ClassifyMedia(%q, %q, %v) = %q, want %q",
				c.name, c.fname, c.caption, c.isSeries, got, c.want)
		}
	}
}

func TestKindFolderName(t *testing.T) {
	if KindFolderName(KindMovie) != "电影" {
		t.Error("movie folder name wrong")
	}
	if KindFolderName(KindTV) != "电视剧" {
		t.Error("tv folder name wrong")
	}
	if KindFolderName(KindAnime) != "动漫" {
		t.Error("anime folder name wrong")
	}
	if KindFolderName(KindOther) != "" {
		t.Error("other should have no folder")
	}
}
