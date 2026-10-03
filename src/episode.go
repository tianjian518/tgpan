package services

import (
	"fmt"
	"path"
	"regexp"
	"strconv"
	"strings"
)

// ===========================================================================
//  剧集识别（EP 解析）
//
//  目标：把「狂飙 第05集.mp4」这类文件名，识别出
//
//      剧名 = 狂飙
//      季   = 1（没写就是第 1 季）
//      集   = 5
//
//  然后上层就能把同一部剧的散集，收进「狂飙/」这个子文件夹，并按
//  规范的「狂飙 S01E05.mp4」重命名。
//
//  设计原则：宁可漏判，不要错判。
//  识别不出来就返回 ok=false，让文件平铺在频道文件夹里 —— 这跟现在的
//  行为一致，不会有任何副作用。反过来，如果把不相干的片子误识别成
//  同一部剧、塞进一个文件夹，那比不整理还糟。
// ===========================================================================

// EpisodeInfo 是识别结果
type EpisodeInfo struct {
	Title   string // 剧名（已清洗）
	Season  int    // 季，默认 1
	Episode int    // 集
	Ok      bool   // 是否识别成功
}

// episodePatterns 按「靠谱程度」从高到低排列。
// 只要有一条命中就采用，后续的不再尝试 —— 因为越靠前的越明确。
var episodePatterns = []struct {
	re    *regexp.Regexp
	build func(m []string) (title string, season, ep int)
}{
	// ---- 第一档：带季的完整写法，第一季 第05集 / 第2季 第3集 ----
	// 必须排在「第N集」之前。
	// 否则「狂飙 第二季 第12集」会先被「第12集」那条规则命中，
	// 剧名被切成「狂飙 第二季」，季数也丢了。
	{
		re: regexp.MustCompile(`^(.*?)[\s._\-\[\(]*第\s*([一二三四五六七八九十\d]{1,3})\s*季[\s._\-]*第?\s*(\d{1,4})\s*[集话話期]?`),
		build: func(m []string) (string, int, int) {
			s := chineseNumToInt(m[2])
			e, _ := strconv.Atoi(m[3])
			return m[1], s, e
		},
	},
	// ---- 第二档：S01E05 / s1e5（最标准，几乎不会错认）----
	{
		re: regexp.MustCompile(`(?i)^(.*?)[\s._\-\[\(]*S(\d{1,2})[\s._\-]*E(\d{1,4})(?:[\s._\-]|$)`),
		build: func(m []string) (string, int, int) {
			s, _ := strconv.Atoi(m[2])
			e, _ := strconv.Atoi(m[3])
			return m[1], s, e
		},
	},
	// ---- 第三档：第01集 / 第1集 / 第01話（中文剧集最常见）----
	{
		re: regexp.MustCompile(`^(.*?)[\s._\-\[\(]*第\s*(\d{1,4})\s*[集话話期](?:[\s._\-]|$)`),
		build: func(m []string) (string, int, int) {
			e, _ := strconv.Atoi(m[2])
			return m[1], 1, e
		},
	},
	// ---- 第四档：EP05 / Ep05 / E05（英文缩写）----
	{
		re: regexp.MustCompile(`(?i)^(.*?)[\s._\-\[\(]*(?:EP|E)\s*(\d{1,4})(?:[\s._\-]|$)`),
		build: func(m []string) (string, int, int) {
			e, _ := strconv.Atoi(m[2])
			return m[1], 1, e
		},
	},
	// ---- 第五档：中文集数简写，狂飙 05集 / 狂飙 5集 ----
	{
		re: regexp.MustCompile(`^(.*?)[\s._\-\[\(]+(\d{1,4})\s*[集话話期](?:[\s._\-]|$)`),
		build: func(m []string) (string, int, int) {
			e, _ := strconv.Atoi(m[2])
			return m[1], 1, e
		},
	},
	// ---- 第六档：结尾的 [05] / [E05] ----
	{
		re: regexp.MustCompile(`(?i)^(.*?)[\s._\-]*\[(?:EP|E)?\s*(\d{1,4})\](?:[\s._\-]|$)`),
		build: func(m []string) (string, int, int) {
			e, _ := strconv.Atoi(m[2])
			return m[1], 1, e
		},
	},
}

// seasonOnlyPattern 匹配「第二季」这种只有季、没有集的
var seasonOnlyPattern = regexp.MustCompile(`[\s._\-\[\(]*第\s*([一二三四五六七八九十\d]{1,3})\s*季`)

// LeadingNoise 这些前缀通常在剧名前面，属于发布组水印，识别时要去掉
var leadingNoisePattern = regexp.MustCompile(`^\s*(?:[\s._\-]*【[^】]*】|[\s._\-]*\[[^\]]*\]|[\s._\-]*\([^\)]*\))+`)

// ParseEpisode 从文件名里解析剧集信息。
//
// 返回的 Ok=false 表示「没识别出集数」，调用方应当按普通文件处理。
//
// 注意：这个函数不会去猜。只有明确匹配到集数标识才返回 Ok=true。
// 「狂飙 05.mp4」这种纯数字结尾的一律不认 —— 因为分不清它到底是第 5 集，
// 还是 2024 年、还是画质参数，误判代价比漏判大。
func ParseEpisode(rawName string) EpisodeInfo {
	if rawName == "" {
		return EpisodeInfo{}
	}

	// 去掉扩展名再解析，避免 ".mp4" 干扰
	base := rawName
	if i := strings.LastIndex(base, "."); i > 0 {
		ext := base[i:]
		switch strings.ToLower(ext) {
		case ".mp4", ".mkv", ".avi", ".mov", ".wmv", ".flv", ".webm",
			".ts", ".m2ts", ".rmvb", ".rm", ".mpg", ".mpeg", ".m4v", ".3gp":
			base = base[:i]
		}
	}

	// 先剥掉开头的发布组水印，例如 【某某压制】、[FHD]
	base = leadingNoisePattern.ReplaceAllString(base, "")
	base = strings.TrimSpace(base)

	for _, p := range episodePatterns {
		m := p.re.FindStringSubmatch(base)
		if m == nil {
			continue
		}
		title, season, ep := p.build(m)

		// 集数必须 > 0，否则视为解析失败（例如 E00）
		if ep <= 0 {
			continue
		}
		// 集数上限 2000，超过基本可以断定是年份之类的误匹配
		if ep > 2000 {
			continue
		}

		title = cleanSeriesTitle(title)
		// 剧名太短通常是误匹配（比如只剩一个数字）
		if title == "" || len([]rune(title)) < 1 {
			continue
		}
		// 剧名不能是纯数字
		if isAllDigits(title) {
			continue
		}

		if season <= 0 || season > 50 {
			season = 1
		}

		return EpisodeInfo{Title: title, Season: season, Episode: ep, Ok: true}
	}

	return EpisodeInfo{}
}

// cleanSeriesTitle 清洗剧名，去掉常见的画质/字幕等尾巴
func cleanSeriesTitle(t string) string {
	t = strings.TrimSpace(t)

	// 去掉首尾的分隔符
	t = strings.Trim(t, " ._-·。、")

	// 去掉残留的「第X季」标记。
	// 走到这里季数已经被单独取出来了，剧名里不该再带着它，
	// 否则会变成「狂飙 第二季」这种文件夹名。
	t = seasonOnlyPattern.ReplaceAllString(t, "")

	// 把点分或下划线分隔还原成空格：Breaking.Bad -> Breaking Bad
	// 但如果整体已经是中文，就不动（中文剧名里很少用点分隔）
	if !containsChinese(t) {
		t = strings.ReplaceAll(t, ".", " ")
		t = strings.ReplaceAll(t, "_", " ")
	}

	// 去掉末尾的画质/来源标签
	tailNoise := []string{
		"1080p", "720p", "2160p", "480p", "4k", "8k", "hdr", "hdr10",
		"bluray", "blu-ray", "bdrip", "brrip", "webrip", "web-dl", "webdl",
		"hdrip", "dvdrip", "hdtv", "x264", "x265", "h264", "h265", "hevc",
		"aac", "ac3", "dts", "ddp", "atmos", "10bit", "8bit",
		"国语", "国配", "粤语", "双语", "中字", "内嵌", "外挂", "字幕",
		"高清", "超清", "蓝光", "完整版", "未删减", "抢先版",
	}
	changed := true
	for changed {
		changed = false
		low := strings.ToLower(t)
		for _, n := range tailNoise {
			nl := strings.ToLower(n)
			// 只从尾部剥，避免误伤片名中间的字
			if strings.HasSuffix(low, nl) {
				t = strings.TrimSpace(t[:len(t)-len(n)])
				t = strings.Trim(t, " ._-·。、[]【】()（）")
				changed = true
				break
			}
		}
	}

	t = strings.Trim(t, " ._-·。、")
	if len([]rune(t)) > 80 {
		t = string([]rune(t)[:80])
	}
	return strings.TrimSpace(t)
}

// FormatEpisodeName 生成规范化的集数文件名。
//
//	狂飙 + S1 + E5 + .mp4  ->  狂飙 S01E05.mp4
//
// 这样播放器刮削时命中率最高。
func FormatEpisodeName(title string, season, ep int, ext string) string {
	title = sanitizeName(title)
	if ext == "" {
		ext = ".mp4"
	}
	if !strings.HasPrefix(ext, ".") {
		ext = "." + ext
	}
	if season <= 1 {
		return fmt.Sprintf("%s S01E%02d%s", title, ep, ext)
	}
	return fmt.Sprintf("%s S%02dE%02d%s", title, season, ep, ext)
}

// EpisodeResolve 是「文件名 + 配文」联合识别的结果
type EpisodeResolve struct {
	Ok   bool
	Name string // 规范化后的文件名
	Info EpisodeInfo
}

// extractCaptionLine 从消息配文里取出有效的那一行。
//
// 影视频道的配文经常长这样：
//
//	狂飙 第05集
//	剧情简介：安欣和高启强斗智斗勇...
//	#国产剧 #张译
//
// 有用的只有第一行，剩下的当噪音处理。多行一律只取第一行，
// 避免「简介里出现的数字」被误当集数。
func extractCaptionLine(caption string) string {
	if caption == "" {
		return ""
	}
	for _, line := range strings.Split(caption, "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			return line
		}
	}
	return ""
}

// ResolveEpisodeFilename 决定一个视频最终叫什么名字、算不算剧集。
//
// 顺序：先拿文件名去认，认不出来再拿配文第一行去认。
// 文件名优先是有意的 —— 文件名通常是发布者精心写过的（「狂飙 S01E05」），
// 而配文可能写的是整季的广告语，用它覆盖文件名反而会搞错。
func ResolveEpisodeFilename(fname, caption string) EpisodeResolve {
	info := ParseEpisode(fname)
	if !info.Ok {
		info = ParseEpisode(extractCaptionLine(caption))
	}

	if !info.Ok {
		return EpisodeResolve{Ok: false, Name: fname}
	}

	ext := strings.ToLower(path.Ext(fname))
	if ext == "" {
		ext = ".mp4"
	}
	return EpisodeResolve{
		Ok:   true,
		Name: FormatEpisodeName(info.Title, info.Season, info.Episode, ext),
		Info: info,
	}
}

// containsChinese 判断是否含中文字符
func containsChinese(s string) bool {
	for _, r := range s {
		if r >= 0x4E00 && r <= 0x9FFF {
			return true
		}
	}
	return false
}

// isAllDigits 判断是否全是数字
func isAllDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// chineseNumToInt 把「一」「二」「十二」这类中文数字转成整数。
// 只处理到 99，够用了（季数不会超过这个范围）。
func chineseNumToInt(s string) int {
	if s == "" {
		return 1
	}
	// 本身是阿拉伯数字
	if n, err := strconv.Atoi(s); err == nil {
		return n
	}

	digits := map[rune]int{
		'一': 1, '二': 2, '三': 3, '四': 4, '五': 5,
		'六': 6, '七': 7, '八': 8, '九': 9,
	}
	runes := []rune(s)

	// 十、十X、X十、X十X
	for i, r := range runes {
		if r == '十' {
			before := 1
			if i > 0 {
				if v, ok := digits[runes[i-1]]; ok {
					before = v
				}
			}
			after := 0
			if i+1 < len(runes) {
				if v, ok := digits[runes[i+1]]; ok {
					after = v
				}
			}
			return before*10 + after
		}
	}

	// 单个数字
	if len(runes) == 1 {
		if v, ok := digits[runes[0]]; ok {
			return v
		}
	}
	return 1
}
