package services

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"path"
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
	Message     string `json:"message"`
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

// extractVideo 从消息里提取视频信息
func extractVideo(msg *tg.Message) (*tg.Document, string, int64, bool) {
	if msg == nil || msg.Media == nil {
		return nil, "", 0, false
	}

	media, ok := msg.Media.(*tg.MessageMediaDocument)
	if !ok {
		return nil, "", 0, false
	}
	doc, ok := media.Document.(*tg.Document)
	if !ok {
		return nil, "", 0, false
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
			return nil, "", 0, false
		}
	}

	if fname == "" {
		fname = fmt.Sprintf("video_%d.mp4", msg.ID)
	}

	return doc, fname, doc.Size, true
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

		// 3. 翻历史消息
		var maxID int
		scanned := 0
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
			for _, m := range msgs.Messages {
				msg, ok := m.(*tg.Message)
				if !ok {
					continue
				}
				batch++
				scanned++

				if msg.ID > maxID {
					maxID = msg.ID
				}

				doc, fname, size, ok := extractVideo(msg)
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

				collected = append(collected, models.File{
					Name:      uniqueFileName(collected, sanitizeName(fname)),
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
			}

			if batch < 100 {
				break
			}
			// 避免触发限流
			time.Sleep(300 * time.Millisecond)
		}

		result.Scanned = scanned

		// 4. 建文件夹并落库
		if len(collected) > 0 {
			folder, err := a.ensureFolder(ctx, userId, folderName(req, full.Title), req.ParentId)
			if err != nil {
				return err
			}
			result.FolderId = folder.ID
			result.FolderName = folder.Name

			var totalSize int64
			for i := range collected {
				collected[i].ParentId = &folder.ID
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
