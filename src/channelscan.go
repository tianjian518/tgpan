package services

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"
	"github.com/tgdrive/teldrive/internal/api"
	"github.com/tgdrive/teldrive/internal/auth"
	"github.com/tgdrive/teldrive/internal/category"
	"github.com/tgdrive/teldrive/internal/tgc"
	"github.com/tgdrive/teldrive/pkg/models"

	"gorm.io/datatypes"
	"gorm.io/gorm"
)

// ---------------------------------------------------------------------------
// 频道扫描：把 TG 频道里已有的视频消息，登记成 Teldrive 网盘文件
//
// 原理：
//   Teldrive 播放文件时，是拿 files.channel_id + files.parts[0].id 去 TG
//   频道里取那条消息的 document，然后流式读取。所以只要往 files 表里插
//   一行记录（指向频道里已存在的消息），这个文件就能被列出和播放。
//
//   本功能通过 TG 账号翻频道历史消息，把每个视频消息变成一行 files 记录。
//   不复制文件、不占空间、不吃带宽。
// ---------------------------------------------------------------------------

// ScanRequest 扫描请求
type ScanRequest struct {
	// ChannelId 要扫描的频道 ID：支持裸 ID(1234567890)、-100 前缀(-1001234567890)
	ChannelId int64 `json:"channelId"`
	// FolderName 网盘里创建的文件夹名（留空则用频道标题）
	FolderName string `json:"folderName,omitempty"`
	// ParentId 挂到哪个目录下（留空为根目录）
	ParentId string `json:"parentId,omitempty"`
	// Limit 最多扫描多少条消息（默认 2000）
	Limit int `json:"limit,omitempty"`
	// MinSize 最小文件字节数，过滤封面小图（默认 1MB）
	MinSize int64 `json:"minSize,omitempty"`
	// Incremental 为 true 时只扫比游标更新的消息（自动扫描用这个）
	Incremental bool `json:"incremental,omitempty"`
	// AutoRegister 为 true 时把频道登记进 channel_scans，交给后台定时扫
	AutoRegister bool `json:"autoRegister,omitempty"`
	// IntervalSeconds 自动扫描间隔（秒），仅 AutoRegister 时有效
	IntervalSeconds int `json:"intervalSeconds,omitempty"`
}

// ScanResult 扫描结果
type ScanResult struct {
	ChannelId   int64  `json:"channelId"`
	ChannelName string `json:"channelName"`
	FolderId    string `json:"folderId"`
	FolderName  string `json:"folderName"`
	Scanned     int    `json:"scanned"`   // 扫过的消息数
	Imported    int    `json:"imported"`  // 新导入的文件数
	Skipped     int    `json:"skipped"`   // 跳过（已存在/太小/非视频）
	TotalSize   int64  `json:"totalSize"` // 导入文件总大小
	// HighWater 本次扫到的最大消息 ID，作为下次增量扫描的游标
	HighWater int `json:"highWater"`
	// AutoScan 是否已登记自动扫描
	AutoScan bool   `json:"autoScan"`
	// EpisodeMatched 其中被识别为剧集、并归入剧名子文件夹的文件数
	EpisodeMatched int `json:"episodeMatched"`
	// SeriesFolders 本次用到的剧名子文件夹数量
	SeriesFolders int    `json:"seriesFolders"`
	Message       string `json:"message"`
}

// normalizeChannelId 兼容 -100 前缀和裸 ID，统一返回裸 ID
func normalizeChannelId(id int64) int64 {
	if id < 0 {
		s := strconv.FormatInt(-id, 10)
		if strings.HasPrefix(s, "100") && len(s) > 3 {
			if v, err := strconv.ParseInt(s[3:], 10, 64); err == nil {
				return v
			}
		}
		return -id
	}
	return id
}

// extractVideo 从消息里提取视频信息。
//
// 第三个返回值是消息自带的文字（caption）。很多影视频道发片时文件名里
// 只有「05.mp4」这种没头没尾的东西，真正的剧名和集数写在配文里，所以
// caption 必须带出去给剧集识别当兜底。
func extractVideo(msg *tg.Message) (*tg.Document, string, string, int64, bool) {
	if msg == nil || msg.Media == nil {
		return nil, "", "", 0, false
	}

	media, ok := msg.Media.(*tg.MessageMediaDocument)
	if !ok {
		return nil, "", "", 0, false
	}
	doc, ok := media.Document.(*tg.Document)
	if !ok {
		return nil, "", "", 0, false
	}

	var fname string
	isVideo := false
	for _, attr := range doc.Attributes {
		switch a := attr.(type) {
		case *tg.DocumentAttributeFilename:
			fname = a.FileName
		case *tg.DocumentAttributeVideo:
			isVideo = true
		}
	}

	if !isVideo {
		if fname == "" || category.GetCategory(fname) != category.Video {
			return nil, "", "", 0, false
		}
	}

	if fname == "" {
		fname = fmt.Sprintf("video_%d.mp4", msg.ID)
	}

	return doc, fname, msg.Message, doc.Size, true
}

// FilesScanChannel 扫描一个频道，把视频登记进网盘
func (a *apiService) FilesScanChannel(ctx context.Context, req ScanRequest) (*ScanResult, error) {
	userId := auth.GetUser(ctx)
	if userId == 0 {
		return nil, &apiError{err: fmt.Errorf("unauthorized"), code: 401}
	}

	channelId := normalizeChannelId(req.ChannelId)
	if channelId == 0 {
		return nil, &apiError{err: fmt.Errorf("channelId 不能为空"), code: 400}
	}

	if req.Limit <= 0 {
		req.Limit = 2000
	}
	if req.MinSize <= 0 {
		req.MinSize = 1024 * 1024
	}

	jwtUser := auth.GetJWTUser(ctx)
	if jwtUser == nil {
		return nil, &apiError{err: fmt.Errorf("unauthorized"), code: 401}
	}

	client, err := tgc.AuthClient(ctx, &a.cnf.TG, jwtUser.TgSession, a.newMiddlewares(ctx, 5)...)
	if err != nil {
		return nil, &apiError{err: err}
	}

	result := &ScanResult{ChannelId: channelId}
	var collected []models.File

	// 剧集识别用的累加状态（每部剧单独记一份，见主循环注释）
	seriesTitles := map[string]bool{}
	seriesNames := map[string][]models.File{}
	var nonSeries []models.File
	// epTags[i] 对应 collected[i] 的剧集识别结果
	var epTags []EpisodeInfo

	err = client.Run(ctx, func(ctx context.Context) error {
		tgAPI := client.API()

		// 1. 解析频道
		inputChannel, err := tgc.GetChannelById(ctx, tgAPI, channelId)
		if err != nil {
			return fmt.Errorf("找不到频道 %d，请确认这个 TG 账号已加入该频道", channelId)
		}
		peer := &tg.InputPeerChannel{
			ChannelID:  inputChannel.ChannelID,
			AccessHash: inputChannel.AccessHash,
		}

		full, err := tgc.GetChannelFull(ctx, tgAPI, channelId)
		if err != nil {
			return fmt.Errorf("获取频道信息失败: %w", err)
		}
		result.ChannelName = full.Title

		// 2. 拉已有的 part id 用于去重
		existingParts := map[int64]bool{}
		var existing []models.File
		if err := a.db.Where("user_id = ? AND channel_id = ?", userId, channelId).
			Find(&existing).Error; err != nil {
			return err
		}
		for _, f := range existing {
			if f.Parts != nil {
				for _, p := range *f.Parts {
					existingParts[int64(p.ID)] = true
				}
			}
		}

		// 2.1 增量模式：读游标，只拉比它更新的消息。
		//     TG 的 MessagesGetHistory 是按 ID 倒序返回，OffsetID 表示
		//     "只返回 ID 小于这个值"的消息。所以游标存的是"已扫过的最大 ID"，
		//     下次传进去就自动只拿更新的部分，避免重复翻全部历史
		//     （全量翻历史极易触发 FLOOD_WAIT，严重会封号）。
		var scanState models.ChannelScan
		cursor := 0
		if err := a.db.Where("channel_id = ? AND user_id = ?", channelId, userId).
			First(&scanState).Error; err == nil {
			cursor = scanState.LastMessageID
		} else if err != gorm.ErrRecordNotFound {
			return err
		}

		if req.Incremental && cursor == 0 {
			// 还没扫过，第一次退化为全量
			req.Incremental = false
		}

		// 3. 翻历史消息
		var maxID int
		if req.Incremental {
			// 增量：从游标处往后（更新方向）再取一点重叠，防止边界丢消息
			maxID = 0 // 0 表示从头（最新）开始
		}
		scanned := 0
		stopID := 0
		if req.Incremental {
			stopID = cursor
		}

		for scanned < req.Limit {
			hist, err := tgAPI.MessagesGetHistory(ctx, &tg.MessagesGetHistoryRequest{
				Peer:     peer,
				OffsetID: maxID,
				Limit:    100,
			})
			if err != nil {
				if d, ok := tgerr.AsFloodWait(err); ok {
					secs := int(d.Seconds())
					if secs > 120 {
						return fmt.Errorf("TG 限流，需等待 %d 秒后再试", secs)
					}
					if secs <= 0 {
						secs = 5
					}
					time.Sleep(time.Duration(secs) * time.Second)
					continue
				}
				return fmt.Errorf("拉取历史消息失败: %w", err)
			}

			msgs, ok := hist.(*tg.MessagesChannelMessages)
			if !ok || len(msgs.Messages) == 0 {
				break
			}

			batch := 0
			reachedCursor := false
			for _, m := range msgs.Messages {
				msg, ok := m.(*tg.Message)
				if !ok {
					continue
				}
				batch++
				scanned++

				// 增量模式：碰到已扫过的消息就停，后面的都处理过了
				if req.Incremental && stopID > 0 && msg.ID <= stopID {
					reachedCursor = true
					break
				}

				if msg.ID > maxID {
					maxID = msg.ID
				}

				doc, fname, caption, size, ok := extractVideo(msg)
				if !ok || size < req.MinSize {
					result.Skipped++
					continue
				}
				if existingParts[int64(msg.ID)] {
					result.Skipped++
					continue
				}
				existingParts[int64(msg.ID)] = true

				slice := datatypes.JSONSlice[api.Part]{
					api.Part{ID: int(msg.ID)},
				}

				mimeType := doc.MimeType
				if mimeType == "" {
					mimeType = "video/mp4"
				}
				sizeCopy := size
				now := time.Now().UTC()

				// ---- 剧集识别 ----
				//
				// 先看文件名，文件名认不出来再看消息配文。
				// 只要认出了「第几集」，就把文件改名成规范格式，并挂到一个
				// 以剧名命名的子文件夹下；认不出来就照旧平铺。
				//
				// 注意：这里只是「记下」这个文件属于哪部剧，真正建剧名文件夹、
				// 挂 parent_id 要等频道文件夹建好之后才能做 —— 剧名文件夹是
				// 挂在频道文件夹底下的。
				fileName := cleanMediaName(sanitizeName(fname))

				er := ResolveEpisodeFilename(fileName, caption)
				ep := er.Info
				if er.Ok {
					fileName = er.Name
					seriesTitles[ep.Title] = true
					// 同名去重只在本剧内部做。跨剧去重会把
					// 「A剧 S01E01」和「B剧 S01E01」误判成重名
					fileName = uniqueFileName(seriesNames[ep.Title], fileName)
					seriesNames[ep.Title] = append(seriesNames[ep.Title],
						models.File{Name: fileName})
					result.EpisodeMatched++
				} else {
					// 认不出来的，在本次扫描的全部平铺文件里去重
					fileName = uniqueFileName(nonSeries, fileName)
					nonSeries = append(nonSeries, models.File{Name: fileName})
				}

				collected = append(collected, models.File{
					Name:      fileName,
					Type:      "file",
					MimeType:  mimeType,
					Size:      &sizeCopy,
					UserId:    userId,
					Status:    "active",
					ChannelId: &channelId,
					Parts:     &slice,
					CreatedAt: &now,
					UpdatedAt: &now,
				})
				// 按顺序记下每个文件对应的剧集信息，下标与 collected 一一对应
				epTags = append(epTags, ep)
			}

			if reachedCursor || batch < 100 {
				break
			}
			// 避免触发限流
			time.Sleep(300 * time.Millisecond)
		}

		result.Scanned = scanned
		result.HighWater = maxID

		// 4. 建文件夹并落库
		//
		//    「一频道一文件夹」的关键：文件夹一旦建立就与 channel_id 绑定，
		//    存进 channel_scans.folder_id。之后无论扫多少次、频道改没改名，
		//    都复用同一个文件夹，不会每次扫都新建一个。
		var folder *models.File

		// 先看这个频道是否已有绑定好的文件夹
		if scanState.FolderID != "" {
			var bound models.File
			if err := a.db.Where("id = ? AND user_id = ? AND status = ?",
				scanState.FolderID, userId, "active").First(&bound).Error; err == nil {
				folder = &bound
			}
		}

		// 没有绑定就用「频道名」找或建
		if folder == nil {
			var err error
			folder, err = a.ensureFolder(ctx, userId, folderName(req, full.Title), req.ParentId)
			if err != nil {
				return err
			}
		}

		result.FolderId = folder.ID
		result.FolderName = folder.Name

		// 4.1 建剧名子文件夹
		//
		//   「狂飙 S01E01.mp4」「狂飙 S01E02.mp4」... 全部收进
		//   频道文件夹/狂飙/ 底下，电影和认不出来的照旧平铺在频道文件夹下。
		//
		//   文件夹按剧名去重复用（ensureFolder 内部先查后建），所以同一部剧
		//   无论分几次扫到，都只会有一个文件夹。
		seriesFolders := map[string]string{} // 剧名 -> 文件夹 ID
		if len(seriesTitles) > 0 {
			// 排序后再建，保证多次扫描的创建顺序稳定，日志好看
			names := make([]string, 0, len(seriesTitles))
			for t := range seriesTitles {
				names = append(names, t)
			}
			sort.Strings(names)

			for _, t := range names {
				sf, err := a.ensureFolder(ctx, userId, sanitizeName(t), folder.ID)
				if err != nil {
					return fmt.Errorf("创建剧集文件夹「%s」失败: %w", t, err)
				}
				seriesFolders[t] = sf.ID
			}
			result.SeriesFolders = len(seriesFolders)
		}

		if len(collected) > 0 {
			var totalSize int64
			for i := range collected {
				// 认出了剧集的挂到剧名子文件夹下，其余平铺在频道文件夹下
				if i < len(epTags) && epTags[i].Ok {
					if fid, ok := seriesFolders[epTags[i].Title]; ok && fid != "" {
						collected[i].ParentId = &fid
					} else {
						collected[i].ParentId = &folder.ID
					}
				} else {
					collected[i].ParentId = &folder.ID
				}
				c := string(category.GetCategory(collected[i].Name))
				collected[i].Category = &c
				totalSize += *collected[i].Size
			}

			if err := a.db.CreateInBatches(&collected, 200).Error; err != nil {
				return fmt.Errorf("写入数据库失败: %w", err)
			}
			result.Imported = len(collected)
			result.TotalSize = totalSize
		}

		// 5. 更新扫描状态（游标 + 文件夹绑定 + 自动扫描登记）
		if err := a.upsertChannelScan(userId, channelId, full.Title, folder.ID,
			result.HighWater, req.AutoRegister, req.IntervalSeconds, len(collected), ""); err != nil {
			return fmt.Errorf("更新扫描状态失败: %w", err)
		}
		result.AutoScan = req.AutoRegister

		return nil
	})

	if err != nil {
		return nil, &apiError{err: err}
	}

	if result.Imported == 0 {
		result.Message = fmt.Sprintf("扫描了 %d 条消息，没有发现新的视频（跳过 %d 条：已存在、太小或不是视频）", result.Scanned, result.Skipped)
	} else {
		result.Message = fmt.Sprintf("扫描了 %d 条消息，新导入 %d 个视频，共 %s", result.Scanned, result.Imported, humanSize(result.TotalSize))
	}
	return result, nil
}

func folderName(req ScanRequest, title string) string {
	if strings.TrimSpace(req.FolderName) != "" {
		return sanitizeName(req.FolderName)
	}
	return sanitizeName(title)
}

// upsertChannelScan 写入或更新频道扫描状态。
//
// 三种情形：
//  1. 首次扫描      -> 插一行，记下游标与文件夹绑定
//  2. 再次扫描      -> 更新游标（只往大推，不倒退，避免重复扫）
//  3. 登记自动扫描  -> 把 enabled 置 true，交给后台定时器
func (a *apiService) upsertChannelScan(userId, channelId int64, name, folderID string,
	highWater int, enable bool, intervalSec int, imported int, lastErr string) error {

	now := time.Now().UTC()

	var st models.ChannelScan
	err := a.db.Where("channel_id = ?", channelId).First(&st).Error

	if err == gorm.ErrRecordNotFound {
		// 首次登记
		st = models.ChannelScan{
			ChannelId:     channelId,
			UserId:        userId,
			ChannelName:   name,
			FolderID:      folderID,
			Enabled:       enable,
			LastMessageID: highWater,
			LastScanAt:    &now,
			LastError:     lastErr,
			TotalImported: int64(imported),
			CreatedAt:     now,
			UpdatedAt:     now,
		}
		if intervalSec > 0 {
			st.IntervalSeconds = &intervalSec
		}
		return a.db.Create(&st).Error
	}
	if err != nil {
		return err
	}

	// 已存在：更新。
	// 游标只增不减 —— 如果某次扫描因限流提前结束导致 highWater 变小，
	// 直接覆盖会让下次重复扫已有消息，浪费配额。
	updates := map[string]any{
		"channel_name": name,
		"folder_id":    folderID,
		"last_scan_at": now,
		"last_error":   lastErr,
		"updated_at":   now,
	}
	if highWater > st.LastMessageID {
		updates["last_message_id"] = highWater
	}
	if enable {
		updates["enabled"] = true
	}
	if intervalSec > 0 {
		updates["interval_seconds"] = intervalSec
	}
	if imported > 0 {
		updates["total_imported"] = st.TotalImported + int64(imported)
	}
	return a.db.Model(&models.ChannelScan{}).Where("channel_id = ?", channelId).
		Updates(updates).Error
}

// cleanMediaName 清洗视频文件名，提升播放器（网易爆米花/Infuse）的刮削命中率。
//
// 为什么需要：
//   TG 频道里的视频名通常是「【高清】某某电影[1080P]某某压制组.mp4」这种，
//   播放器按此去 TMDB 搜海报必然搜不到。这里把常见的装饰性标记剥掉，
//   尽量还原成「某某电影 (2023).mp4」这种可识别的形式。
//
// 处理规则（保守，不确定的不动）：
//  1. 去掉常见的中文方括号标记：【】［］ 内的推广/画质/来源字样
//  2. 去掉画质标签：1080P / 4K / BluRay / WEB-DL / HDR 等
//  3. 去掉常见站点/压制组标记
//  4. 把点分隔的名字还原成空格（Some.Movie.2023.1080p -> Some Movie 2023）
//  5. 折叠多余空格与连字符
func cleanMediaName(name string) string {
	if name == "" {
		return name
	}

	ext := path.Ext(name)
	base := strings.TrimSuffix(name, ext)

	// 1. 去掉中文方括号及其内容里含推广/画质字样的部分
	//    只删「明显是装饰」的，不含剧情名称，避免误伤片名
	reBracketNoise := regexp.MustCompile(`[【\[［]([^】\]］]*)[】\]］]`)
	base = reBracketNoise.ReplaceAllStringFunc(base, func(m string) string {
		inner := strings.ToLower(m)
		noise := []string{"高清", "超清", "蓝光", "抢先", "完整版", "未删减",
			"1080", "720", "2160", "4k", "hdr", "bluray", "web-dl", "webdl",
			"hd", "hdrip", "bd", "国粤", "双语", "中字", "内嵌", "字幕",
			"更新", "合集", "全集", "连载", "推荐", "热门", "最新"}
		for _, n := range noise {
			if strings.Contains(inner, n) {
				return "" // 整个括号是装饰性的，删掉
			}
		}
		return m // 保留（可能是片名的一部分，比如【阿凡达】）
	})

	// 2. 去掉独立的画质/来源标签（空格或点分隔的）
	labels := []string{
		"1080p", "720p", "2160p", "480p", "4k", "8k", "hdr", "hdr10", "dv",
		"bluray", "blu-ray", "bdrip", "brrip", "webrip", "web-dl", "webdl",
		"hdrip", "dvdrip", "hdtv", "x264", "x265", "h264", "h265", "hevc",
		"aac", "ac3", "dts", "ddp", "atmos", "10bit", "8bit",
	}
	tokens := regexp.MustCompile(`[\.\s_\-]+`).Split(base, -1)
	kept := make([]string, 0, len(tokens))
	for _, tk := range tokens {
		low := strings.ToLower(strings.TrimSpace(tk))
		if low == "" {
			continue
		}
		skip := false
		for _, lb := range labels {
			if low == lb {
				skip = true
				break
			}
		}
		if !skip {
			kept = append(kept, strings.TrimSpace(tk))
		}
	}
	base = strings.Join(kept, " ")

	// 3. 去掉残留的连续分隔符与首尾空白
	base = strings.Trim(base, " .-—_")
	base = regexp.MustCompile(`\s+`).ReplaceAllString(base, " ")

	// 清洗后如果空得离谱，退回原名（宁可不清，也不能把名字搞没）
	if strings.TrimSpace(base) == "" {
		return name
	}

	// 4. 把中英文之间的空格规范一下，但保留扩展名
	if ext == "" {
		return base
	}
	return base + ext
}

// ensureFolder 找到或创建文件夹
func (a *apiService) ensureFolder(ctx context.Context, userId int64, name, parentId string) (*models.File, error) {
	var folder models.File
	q := a.db.Where("user_id = ? AND name = ? AND type = ? AND status = ?", userId, name, "folder", "active")
	if parentId != "" {
		q = q.Where("parent_id = ?", parentId)
	} else {
		q = q.Where("parent_id IS NULL")
	}
	err := q.First(&folder).Error
	if err == nil {
		return &folder, nil
	}
	if err != gorm.ErrRecordNotFound {
		return nil, err
	}

	now := time.Now().UTC()
	newFolder := models.File{
		Name:      name,
		Type:      "folder",
		MimeType:  "folder",
		UserId:    userId,
		Status:    "active",
		CreatedAt: &now,
		UpdatedAt: &now,
	}
	if parentId != "" {
		newFolder.ParentId = &parentId
	}
	if err := a.db.Create(&newFolder).Error; err != nil {
		return nil, err
	}
	return &newFolder, nil
}

func sanitizeName(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return "channel"
	}
	r := strings.NewReplacer("/", "_", "\\", "_", ":", "_", "*", "_",
		"?", "_", "\"", "_", "<", "_", ">", "_", "|", "_", "\n", " ", "\r", " ")
	s = r.Replace(s)
	if len(s) > 150 {
		ext := path.Ext(s)
		s = s[:150-len(ext)] + ext
	}
	return s
}

func uniqueFileName(existing []models.File, name string) string {
	taken := map[string]bool{}
	for _, f := range existing {
		taken[f.Name] = true
	}
	if !taken[name] {
		return name
	}
	ext := path.Ext(name)
	base := strings.TrimSuffix(name, ext)
	for i := 1; i < 100000; i++ {
		candidate := fmt.Sprintf("%s (%d)%s", base, i, ext)
		if !taken[candidate] {
			return candidate
		}
	}
	return name
}

func humanSize(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.2f %cB", float64(n)/float64(div), "KMGTPE"[exp])
}

// FilesScanChannelHTTP 是给扩展中间件用的 HTTP 入口
func (e *extendedService) FilesScanChannelHTTP(w http.ResponseWriter, r *http.Request) {
	cookie, err := r.Cookie(authCookieName)
	if err != nil {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"code": 401, "message": "missing auth cookie"})
		return
	}
	user, err := auth.VerifyUser(r.Context(), e.api.db, e.api.cache, e.api.cnf.JWT.Secret, cookie.Value)
	if err != nil {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"code": 401, "message": "invalid token"})
		return
	}
	ctx := auth.WithUser(r.Context(), user)

	var req ScanRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"code": 400, "message": "请求格式错误: " + err.Error()})
		return
	}

	res, err := e.api.FilesScanChannel(ctx, req)
	if err != nil {
		code := http.StatusInternalServerError
		if ae, ok := err.(*apiError); ok && ae.code != 0 {
			code = ae.code
		}
		writeJSON(w, code, map[string]any{"code": code, "message": err.Error()})
		return
	}

	writeJSON(w, http.StatusOK, res)
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}
