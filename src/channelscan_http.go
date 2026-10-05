package services

// ---------------------------------------------------------------------------
// 频道自动扫描的 HTTP 接口
//
// 给网页界面用：
//   GET    /scan/channels          列出已登记的频道（含游标、上次结果、开关）
//   POST   /scan/channels          登记/更新一个频道的自动扫描
//   DELETE /scan/channels/{id}     取消某个频道的自动扫描
//   POST   /scan/channels/{id}/run 立即扫描一次（不用等定时器）
// ---------------------------------------------------------------------------

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/tgdrive/teldrive/pkg/models"
)

// ChannelScanView 返回给前端的频道扫描状态
type ChannelScanView struct {
	ChannelId     int64  `json:"channelId"`
	ChannelName   string `json:"channelName"`
	FolderID      string `json:"folderId"`
	FolderName    string `json:"folderName"`
	Enabled       bool   `json:"enabled"`
	IntervalSec   int    `json:"intervalSeconds"`
	LastMessageID int    `json:"lastMessageId"`
	LastScanAt    string `json:"lastScanAt,omitempty"`
	LastError     string `json:"lastError,omitempty"`
	TotalImported int64  `json:"totalImported"`
	// FileCount 这个频道目前在网盘里的文件数
	FileCount int64 `json:"fileCount"`
}

// ChannelScanRegister 登记自动扫描的请求体
type ChannelScanRegister struct {
	ChannelId       int64  `json:"channelId"`
	FolderName      string `json:"folderName,omitempty"`
	Enabled         bool   `json:"enabled"`
	IntervalSeconds int    `json:"intervalSeconds,omitempty"`
	// ScanNow 是否登记后立刻扫一次（默认 true，让用户马上看到效果）
	ScanNow *bool `json:"scanNow,omitempty"`
}

// ChannelsScanListHTTP 列出所有已登记的频道
func (e *extendedService) ChannelsScanListHTTP(w http.ResponseWriter, r *http.Request) {
	claims, err := e.verifyCookieUser(r)
	if err != nil {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"code": 401, "message": "登录已过期，请重新登录后再试"})
		return
	}
	userId, _ := strconv.ParseInt(claims.Subject, 10, 64)

	var scans []models.ChannelScan
	// 次级排序键不能省：同一秒内登记的多个频道 created_at 会一模一样，
	// 只按它排的话每次查询顺序都可能不一样，界面上的行会莫名其妙地跳来跳去。
	//
	// 这里用 channel_id —— 注意这张表**没有** id 列，主键就是 channel_id，
	// 写 "id" 会直接 SQLSTATE 42703（column "id" does not exist）。
	if err := e.api.db.Where("user_id = ?", userId).Order("created_at ASC, channel_id ASC").
		Find(&scans).Error; err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"code": 500, "message": err.Error()})
		return
	}

	// 取默认间隔，前端要展示
	defInterval := int(defaultScanInterval.Seconds())

	out := make([]ChannelScanView, 0, len(scans))
	for _, s := range scans {
		v := ChannelScanView{
			ChannelId:     s.ChannelId,
			ChannelName:   s.ChannelName,
			FolderID:      s.FolderID,
			Enabled:       s.Enabled,
			IntervalSec:   defInterval,
			LastMessageID: s.LastMessageID,
			LastError:     s.LastError,
			TotalImported: s.TotalImported,
		}
		if s.IntervalSeconds != nil && *s.IntervalSeconds > 0 {
			v.IntervalSec = *s.IntervalSeconds
		}
		if s.LastScanAt != nil {
			v.LastScanAt = s.LastScanAt.Format(time.RFC3339)
		}

		// 取文件夹名与文件数，前端要展示「频道 -> 文件夹」的映射
		var folder models.File
		if s.FolderID != "" {
			if err := e.api.db.Where("id = ?", s.FolderID).First(&folder).Error; err == nil {
				v.FolderName = folder.Name
			}
		}
		var cnt int64
		if s.FolderID != "" {
			e.api.db.Model(&models.File{}).
				Where("user_id = ? AND parent_id = ? AND status = ?", userId, s.FolderID, "active").
				Count(&cnt)
		}
		v.FileCount = cnt

		out = append(out, v)
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"channels":        out,
		"defaultInterval": defInterval,
		"webdavMountPath": webdavPrefix,
	})
}

// ChannelScanRegisterHTTP 登记/更新频道的自动扫描
func (e *extendedService) ChannelScanRegisterHTTP(w http.ResponseWriter, r *http.Request) {
	// Ctx 版本：下面 FilesScanChannel 靠 auth.GetUser(ctx) 取用户。
	// 用老版本会一路 401（"登记并立即扫描"这个操作以前就是坏的）。
	r, claims, err := e.verifyCookieUserCtx(r)
	if err != nil {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"code": 401, "message": "登录已过期，请重新登录后再试"})
		return
	}
	userId, _ := strconv.ParseInt(claims.Subject, 10, 64)

	var req ChannelScanRegister
	if err := decodeJSONBody(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"code": 400, "message": "请求格式错误: " + err.Error()})
		return
	}
	if req.ChannelId == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"code": 400, "message": "channelId 不能为空"})
		return
	}

	// 默认立刻扫一次，让用户马上看到文件夹建出来
	scanNow := true
	if req.ScanNow != nil {
		scanNow = *req.ScanNow
	}

	if scanNow {
		// 走一次完整扫描（同时会把频道登记进 channel_scans）
		res, err := e.api.FilesScanChannel(r.Context(), ScanRequest{
			ChannelId:       req.ChannelId,
			FolderName:      req.FolderName,
			AutoRegister:    req.Enabled,
			IntervalSeconds: req.IntervalSeconds,
		})
		if err != nil {
			code := http.StatusInternalServerError
			if ae, ok := err.(*apiError); ok && ae.code != 0 {
				code = ae.code
			}
			writeJSON(w, code, map[string]any{"code": code, "message": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"scan": res, "registered": req.Enabled})
		return
	}

	// 不立刻扫，只登记
	cnName := ""
	var existing models.ChannelScan
	if err := e.api.db.Where("channel_id = ? AND user_id = ?", req.ChannelId, userId).
		First(&existing).Error; err == nil {
		cnName = existing.ChannelName
	}
	if err := e.api.upsertChannelScan(userId, req.ChannelId, cnName, "",
		0, req.Enabled, req.IntervalSeconds, 0, ""); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"code": 500, "message": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "registered": req.Enabled})
}

// ChannelScanDeleteHTTP 取消频道的自动扫描
func (e *extendedService) ChannelScanDeleteHTTP(w http.ResponseWriter, r *http.Request, channelIdStr string) {
	claims, err := e.verifyCookieUser(r)
	if err != nil {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"code": 401, "message": "登录已过期，请重新登录后再试"})
		return
	}
	userId, _ := strconv.ParseInt(claims.Subject, 10, 64)

	channelId, err := strconv.ParseInt(normalizeChannelIdStr(channelIdStr), 10, 64)
	if err != nil || channelId == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"code": 400, "message": "频道 ID 无效"})
		return
	}

	// 只关闭自动扫描，不删已导入的文件（文件是用户的资产，不该被顺手清掉）
	res := e.api.db.Model(&models.ChannelScan{}).
		Where("channel_id = ? AND user_id = ?", channelId, userId).
		Updates(map[string]any{"enabled": false, "updated_at": time.Now().UTC()})
	if res.Error != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"code": 500, "message": res.Error.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// ChannelScanRunNowHTTP 立即扫描一次
func (e *extendedService) ChannelScanRunNowHTTP(w http.ResponseWriter, r *http.Request, channelIdStr string) {
	// 用 Ctx 版本：RescanChannel 靠 auth.GetUser(ctx) 取用户，
	// 不注入 context 会一路 401（这个按钮以前就是坏的）
	r, claims, err := e.verifyCookieUserCtx(r)
	if err != nil {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"code": 401, "message": "登录已过期，请重新登录后再试"})
		return
	}
	userId, _ := strconv.ParseInt(claims.Subject, 10, 64)

	channelId, err := strconv.ParseInt(normalizeChannelIdStr(channelIdStr), 10, 64)
	if err != nil || channelId == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"code": 400, "message": "频道 ID 无效"})
		return
	}

	// 校验这个频道确实属于当前用户，防止越权扫别人的频道
	var st models.ChannelScan
	if err := e.api.db.Where("channel_id = ? AND user_id = ?", channelId, userId).
		First(&st).Error; err != nil {
		writeJSON(w, http.StatusNotFound, map[string]any{"code": 404, "message": "该频道未登记"})
		return
	}

	res, err := e.api.RescanChannel(r.Context(), channelId, 500)
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

// normalizeChannelIdStr 把路径里的频道 ID 字符串规范化
func normalizeChannelIdStr(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	v, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return s
	}
	return strconv.FormatInt(normalizeChannelId(v), 10)
}

// ---------------------------------------------------------------------------
// 已关注频道列表
//
// 为什么要有这几个接口：
//
//	扫描页以前让人手填 TG 频道 ID。绝大多数人根本不知道自己的频道 ID 是多少 ——
//	得点进消息链接里数那串数字，还要判断加不加 -100 前缀，填错了就报
//	「找不到频道」。而账号本来就关注了这些频道，直接列出来点一下最省事。
//
// 数据来源是 tg_dialogs 缓存表，由后台定时从 Telegram 同步。
// 接口本身**不发任何 TG 请求**，所以毫秒级返回。
// ---------------------------------------------------------------------------

// DialogsListHTTP 列出「我关注的频道」
//
// 查询参数：
//
//	refresh=1     同步拉一次再返回（会慢，几秒；前端只做手动刷新用）
//	groups=1      连群组一起列（默认只列广播频道）
func (e *extendedService) DialogsListHTTP(w http.ResponseWriter, r *http.Request) {
	// 必须用 verifyCookieUserCtx（而不是 verifyCookieUser）：
	// 下面的 SyncUserDialogs / ListDialogsForUser 都靠 auth.GetUser(ctx)
	// 取用户，而老版本只返回 claims、不注入 context —— 会一路 401。
	r, claims, err := e.verifyCookieUserCtx(r)
	if err != nil {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"code": 401, "message": "登录已过期，请重新登录后再试"})
		return
	}
	userId, _ := strconv.ParseInt(claims.Subject, 10, 64)

	includeGroups := r.URL.Query().Get("groups") == "1"

	// refresh=1：立刻同步一次（前端手动刷新按钮走这条）。
	// 这里失败**不算错** —— 库里可能已经有上次同步的数据，
	// 直接把它返回给用户，同时把同步失败的原因带回去当提示。
	var syncErr string
	if r.URL.Query().Get("refresh") == "1" {
		if err := e.api.SyncUserDialogs(r.Context()); err != nil {
			syncErr = err.Error()
		}
	} else {
		// 冷启动兜底：库里还没有这个用户的频道列表时（刚登录、或从没同步过），
		// 顺手同步一次 —— 否则用户打开扫描页看到的是空列表，
		// 而调度器要等到 60s 启动延迟 + 下一个 10 分钟 tick 才会跑。
		//
		// fillDialogsIfEmpty 内部保证：只在列表为空时才真去拉，
		// 且同一用户并发只会有一路在拉，不会把 TG 配额打爆。
		// 它失败不返回错误（这是体验优化，不是功能路径）。
		e.api.fillDialogsIfEmpty(r.Context(), userId)
	}

	list, err := e.api.ListDialogsForUser(r.Context(), includeGroups)
	if err != nil {
		code := http.StatusInternalServerError
		if ae, ok := err.(*apiError); ok && ae.code != 0 {
			code = ae.code
		}
		writeJSON(w, code, map[string]any{"code": code, "message": err.Error()})
		return
	}

	resp := map[string]any{
		"dialogs":         list,
		"total":           len(list),
		"includeGroups":   includeGroups,
		"syncIntervalSec": int(dialogSyncInterval.Seconds()),
	}
	if syncErr != "" {
		resp["syncError"] = syncErr
	}
	writeJSON(w, http.StatusOK, resp)
}
