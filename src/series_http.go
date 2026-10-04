package services

// ---------------------------------------------------------------------------
// 剧集归档的 HTTP 接口
//
//   GET  /scan/series              列出当前用户的剧集文件夹（剧名 + 集数 + 文件数）
//   POST /scan/series/rename       重命名 / 移动一个文件（手动纠正）
//   POST /scan/series/merge        把两个剧集文件夹合并
//   POST /scan/series/rescan       对已有文件重跑一次识别（纠正后重排）
//
// 设计取舍：
//   自动识别永远会有漏判和错判。漏判不致命（文件平铺着也能用），但错判
//   会让人找不到片子。所以这里给用户一个「随时能改回来」的出口：
//   改个名、拖到别的文件夹，下次扫描不会把它改回去（扫描只处理新消息）。
// ---------------------------------------------------------------------------

import (
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/tgdrive/teldrive/internal/category"
	"github.com/tgdrive/teldrive/pkg/models"
	"gorm.io/gorm"
)

// SeriesFileItem 剧集文件夹里的一个文件，供前端做「点选改名」
type SeriesFileItem struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Episode int    `json:"episode"` // 识别出的集号，0 表示没识别出来
	Size    int64  `json:"size"`
}

// SeriesFolderView 一个剧集文件夹的概览
type SeriesFolderView struct {
	ID       string `json:"id"`
	Title    string `json:"title"`
	FileNum  int64  `json:"fileCount"`
	FolderID string `json:"folderId,omitempty"` // 所属频道文件夹
	// EpisodeCount 已规范命名的集数（SxxExx 格式）
	EpisodeCount int `json:"episodeCount"`
	// FirstEpisode / LastEpisode 最小、最大集号，前端用来显示「第1-24集」
	FirstEpisode int      `json:"firstEpisode"`
	LastEpisode  int      `json:"lastEpisode"`
	Samples      []string `json:"samples,omitempty"`
	// Files 该剧的全部文件，前端用来让用户点选要改哪个（不用手输 ID）
	Files []SeriesFileItem `json:"files,omitempty"`
}

// SeriesListHTTP 列出剧集文件夹
//
// 判定标准：一个文件夹，如果它底下存在名字符合「剧名 SxxExx.xxx」的文件，
// 就当剧集文件夹。不另建表 —— 文件夹本身就是数据来源，不会出现表跟目录
// 对不上的情况。
func (e *extendedService) SeriesListHTTP(w http.ResponseWriter, r *http.Request) {
	claims, err := e.verifyCookieUser(r)
	if err != nil {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"code": 401, "message": "请先登录"})
		return
	}
	userId, _ := strconv.ParseInt(claims.Subject, 10, 64)

	var folders []models.File
	if err := e.api.db.Where("user_id = ? AND type = ? AND status = ?",
		userId, "folder", "active").Find(&folders).Error; err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"code": 500, "message": err.Error()})
		return
	}

	out := make([]SeriesFolderView, 0, len(folders))

	// 一次把所有文件夹的文件捞出来，在内存里按 parent_id 分组。
	//
	// 原来的写法是「每个文件夹查一次文件」，频道 + 剧集文件夹一多就是
	// 几十上百次往返（N+1）。这里改成一次 IN 查询，整体快一个量级。
	folderIDs := make([]string, 0, len(folders))
	for _, f := range folders {
		folderIDs = append(folderIDs, f.ID)
	}
	filesByParent := map[string][]models.File{}
	if len(folderIDs) > 0 {
		var allFiles []models.File
		if err := e.api.db.Where("user_id = ? AND parent_id IN ? AND type = ? AND status = ?",
			userId, folderIDs, "file", "active").Find(&allFiles).Error; err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"code": 500, "message": err.Error()})
			return
		}
		for _, fl := range allFiles {
			if fl.ParentId != nil {
				filesByParent[*fl.ParentId] = append(filesByParent[*fl.ParentId], fl)
			}
		}
	}

	for _, f := range folders {
		files := filesByParent[f.ID]
		if len(files) == 0 {
			continue
		}

		v := SeriesFolderView{ID: f.ID, Title: f.Name, FileNum: int64(len(files))}
		if f.ParentId != nil {
			v.FolderID = *f.ParentId
		}

		minEp, maxEp := 0, 0
		names := make([]string, 0, len(files))
		items := make([]SeriesFileItem, 0, len(files))
		for _, file := range files {
			names = append(names, file.Name)
			var sz int64
			if file.Size != nil {
				sz = *file.Size
			}
			ep := ParseEpisode(file.Name)
			item := SeriesFileItem{ID: file.ID, Name: file.Name, Size: sz}
			if ep.Ok && ep.Title == f.Name {
				item.Episode = ep.Episode
				v.EpisodeCount++
				if minEp == 0 || ep.Episode < minEp {
					minEp = ep.Episode
				}
				if ep.Episode > maxEp {
					maxEp = ep.Episode
				}
			}
			items = append(items, item)
		}

		// 没有一集能对上文件夹名，说明这不是剧集文件夹（可能是用户自己
		// 建的分类文件夹），跳过
		if v.EpisodeCount == 0 {
			continue
		}
		v.FirstEpisode, v.LastEpisode = minEp, maxEp

		sort.Strings(names)
		if len(names) > 3 {
			names = names[:3]
		}
		v.Samples = names

		// 文件按集号排序，前端展示时顺序才自然（第1集在最前）
		sort.SliceStable(items, func(i, j int) bool {
			a, b := items[i].Episode, items[j].Episode
			if a == 0 || b == 0 {
				return items[i].Name < items[j].Name
			}
			return a < b
		})
		// 单剧文件太多时截断，避免一次响应过大
		if len(items) > 300 {
			items = items[:300]
		}
		v.Files = items

		out = append(out, v)
	}

	sort.Slice(out, func(i, j int) bool { return out[i].Title < out[j].Title })
	writeJSON(w, http.StatusOK, map[string]any{"series": out, "total": len(out)})
}

// SeriesRenameRequest 重命名 / 移动请求
type SeriesRenameRequest struct {
	// FileId 要改的文件
	FileId string `json:"fileId"`
	// Name 新文件名。留空表示不改名，只移动
	Name string `json:"name,omitempty"`
	// ParentId 移到哪个文件夹。留空表示不移动。
	// 特殊值 "root" 表示移回根目录。
	ParentId string `json:"parentId,omitempty"`
}

// SeriesRenameHTTP 手动纠正：改名 / 换文件夹
func (e *extendedService) SeriesRenameHTTP(w http.ResponseWriter, r *http.Request) {
	claims, err := e.verifyCookieUser(r)
	if err != nil {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"code": 401, "message": "请先登录"})
		return
	}
	userId, _ := strconv.ParseInt(claims.Subject, 10, 64)

	var req SeriesRenameRequest
	if err := decodeJSONBody(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"code": 400, "message": "请求格式错误: " + err.Error()})
		return
	}
	if strings.TrimSpace(req.FileId) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"code": 400, "message": "fileId 不能为空"})
		return
	}

	// 只允许改自己的文件
	var file models.File
	if err := e.api.db.Where("id = ? AND user_id = ? AND status = ?",
		req.FileId, userId, "active").First(&file).Error; err != nil {
		writeJSON(w, http.StatusNotFound, map[string]any{"code": 404, "message": "文件不存在"})
		return
	}

	updates := map[string]any{"updated_at": time.Now().UTC()}

	// 目标父目录：不移动就用文件当前所在目录。
	// 重名检查必须针对「最终会落在哪个目录」，否则「改名 + 移动」同时做时，
	// 目标目录里已有同名文件也照样能改进去，造成重名。
	targetParent := file.ParentId
	if req.ParentId != "" {
		if req.ParentId == "root" {
			// 移到根目录：父目录清空
			targetParent = nil
			updates["parent_id"] = nil
		} else {
			// 目标文件夹必须存在且属于本人，防止把文件挂到别人目录下
			var dst models.File
			if err := e.api.db.Where("id = ? AND user_id = ? AND type = ? AND status = ?",
				req.ParentId, userId, "folder", "active").First(&dst).Error; err != nil {
				writeJSON(w, http.StatusNotFound, map[string]any{"code": 404, "message": "目标文件夹不存在"})
				return
			}
			// 不允许移到自己底下，形成环
			if dst.ID == file.ID {
				writeJSON(w, http.StatusBadRequest, map[string]any{"code": 400, "message": "不能移动到自身"})
				return
			}
			pid := req.ParentId
			targetParent = &pid
			updates["parent_id"] = req.ParentId
		}
	}

	if n := strings.TrimSpace(req.Name); n != "" {
		// 名字不能带路径分隔符，否则会在目录树里造出非法节点
		n = sanitizeName(n)
		if n == "" {
			writeJSON(w, http.StatusBadRequest, map[string]any{"code": 400, "message": "文件名不能为空"})
			return
		}
		// 同一目录下不能重名（按目标目录判断）
		var dup int64
		q := e.api.db.Model(&models.File{}).
			Where("user_id = ? AND name = ? AND status = ? AND id <> ?", userId, n, "active", file.ID)
		if targetParent != nil && *targetParent != "" {
			q = q.Where("parent_id = ?", *targetParent)
		} else {
			q = q.Where("parent_id IS NULL")
		}
		q.Count(&dup)
		if dup > 0 {
			writeJSON(w, http.StatusConflict, map[string]any{
				"code": 409, "message": fmt.Sprintf("同目录下已有「%s」，换个名字", n)})
			return
		}
		updates["name"] = n
		// 改名后同步刷新分类，否则「.mkv」的文件还挂着旧分类
		updates["category"] = string(category.GetCategory(n))
	}

	if err := e.api.db.Model(&models.File{}).Where("id = ?", file.ID).
		Updates(updates).Error; err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"code": 500, "message": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// SeriesMergeRequest 合并两个剧集文件夹
type SeriesMergeRequest struct {
	// FromId 要合并掉的文件夹（会被清空后删除）
	FromId string `json:"fromId"`
	// ToId 目标文件夹（保留）
	ToId string `json:"toId"`
}

// SeriesMergeHTTP 把重复建出来的剧集文件夹并成一个
//
// 典型场景：同一部剧，一半文件叫「狂飙 第05集」，另一半叫「狂飙 S01E05」，
// 识别出的剧名不一样（一个是「狂飙」另一个也是「狂飙」但可能带空格差异），
// 就建出两个文件夹。这里让用户手工并一下。
func (e *extendedService) SeriesMergeHTTP(w http.ResponseWriter, r *http.Request) {
	claims, err := e.verifyCookieUser(r)
	if err != nil {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"code": 401, "message": "请先登录"})
		return
	}
	userId, _ := strconv.ParseInt(claims.Subject, 10, 64)

	var req SeriesMergeRequest
	if err := decodeJSONBody(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"code": 400, "message": "请求格式错误"})
		return
	}
	if req.FromId == "" || req.ToId == "" || req.FromId == req.ToId {
		writeJSON(w, http.StatusBadRequest, map[string]any{"code": 400, "message": "fromId / toId 不合法"})
		return
	}

	var from, to models.File
	if err := e.api.db.Where("id = ? AND user_id = ? AND type = ?", req.FromId, userId, "folder").
		First(&from).Error; err != nil {
		writeJSON(w, http.StatusNotFound, map[string]any{"code": 404, "message": "源文件夹不存在"})
		return
	}
	if err := e.api.db.Where("id = ? AND user_id = ? AND type = ?", req.ToId, userId, "folder").
		First(&to).Error; err != nil {
		writeJSON(w, http.StatusNotFound, map[string]any{"code": 404, "message": "目标文件夹不存在"})
		return
	}

	var moved int64
	err = e.api.db.Transaction(func(tx *gorm.DB) error {
		// 把源文件夹下的文件全挂到目标文件夹
		res := tx.Model(&models.File{}).
			Where("user_id = ? AND parent_id = ? AND type = ?", userId, from.ID, "file").
			Updates(map[string]any{"parent_id": to.ID, "updated_at": time.Now().UTC()})
		if res.Error != nil {
			return res.Error
		}
		moved = res.RowsAffected

		// 源文件夹底下的子文件夹（正常不该有，保险起见一起搬）
		if res := tx.Model(&models.File{}).
			Where("user_id = ? AND parent_id = ? AND type = ?", userId, from.ID, "folder").
			Updates(map[string]any{"parent_id": to.ID, "updated_at": time.Now().UTC()}); res.Error != nil {
			return res.Error
		}

		// 源文件夹空了就删（软删，status=deleted）
		return tx.Model(&models.File{}).Where("id = ?", from.ID).
			Updates(map[string]any{"status": "deleted", "updated_at": time.Now().UTC()}).Error
	})
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"code": 500, "message": err.Error()})
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "moved": moved})
}
