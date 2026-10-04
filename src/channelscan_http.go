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
		writeJSON(w, http.StatusUnauthorized, map[string]any{"code": 401, "message": "请先登录"})
		return
	}
	userId, _ := strconv.ParseInt(claims.Subject, 10, 64)

	var scans []models.ChannelScan
	if err := e.api.db.Where("user_id = ?", userId).Order("created_at ASC").
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
	claims, err := e.verifyCookieUser(r)
	if err != nil {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"code": 401, "message": "请先登录"})
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
		writeJSON(w, http.StatusUnauthorized, map[string]any{"code": 401, "message": "请先登录"})
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
	claims, err := e.verifyCookieUser(r)
	if err != nil {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"code": 401, "message": "请先登录"})
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

	res, err := e.api.FilesScanChannel(r.Context(), ScanRequest{
		ChannelId:   channelId,
		Incremental: true,
		Limit:       500,
	})
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
