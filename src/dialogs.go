package services

// ---------------------------------------------------------------------------
// 已关注频道同步（tg_dialogs）
//
// 需求：扫描页要让用户直接从「我已经关注的频道」里挑一个点一下，
//       而不是手填 TG 频道 ID。
//
// 为什么不能让前端直接拉：
//
//	messages.getDialogs 只能走真实 TG 会话，一次要几百毫秒到几秒，
//	频道多的时候还要翻页，还可能吃 FLOOD_WAIT。放在 HTTP 请求里同步拉，
//	用户会盯着一个转圈圈转很久的列表。
//
// 所以拆成两条路：
//   写路径（后台）  SyncUserDialogs 定时把对话列表刷进 tg_dialogs 表
//   读路径（接口）  ListUserDialogs 只查库，毫秒级返回
//
// 定时器设计（沿用 scan_scheduler 的思路）：
//   - 心跳 10 分钟醒一次，检查哪些用户该同步了
//   - 每个用户单独记下次同步时间，实际同步间隔取自 syncInterval（默认 30 分钟）
//   - 失败退避：连续失败就拉长间隔，最长 6 小时，避免坏账号把日志刷爆
//   - 启动延迟：等服务起来、TG 连接池预热完再开始
// ---------------------------------------------------------------------------

import (
	"context"
	"errors"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"
	"github.com/tgdrive/teldrive/internal/auth"
	"github.com/tgdrive/teldrive/internal/logging"
	"github.com/tgdrive/teldrive/internal/tgc"
	"github.com/tgdrive/teldrive/pkg/models"
	"github.com/tgdrive/teldrive/pkg/types"
	"go.uber.org/zap"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	// dialogSyncInterval 是单个用户的默认同步间隔。
	//
	// 取 30 分钟而不是 2 分钟：对话列表变化很慢（关注/取关频道是低频操作），
	// 而且它是**全量翻页**调用，比增量扫描贵得多。扫太勤纯属浪费配额。
	dialogSyncInterval = 30 * time.Minute

	// dialogSyncTick 是同步调度器的心跳粒度
	dialogSyncTick = 10 * time.Minute

	// dialogSyncStartupDelay 是服务启动后的首轮延迟，
	// 等 TG 连接池预热、数据库迁移跑完再开始
	dialogSyncStartupDelay = 60 * time.Second

	// dialogSyncMaxBackoff 是失败退避上限
	dialogSyncMaxBackoff = 6 * time.Hour

	// dialogPageLimit 是单页拉多少条对话。
	// TG 上限是 100，取满以减少往返次数。
	dialogPageLimit = 100

	// dialogMaxPages 是最多翻多少页，防止异常情况下无限翻页。
	// 100 页 × 100 条 = 10000 个对话，正常人关注不了这么多。
	dialogMaxPages = 100

	// dialogSyncTimeout 单个用户的同步超时
	dialogSyncTimeout = 5 * time.Minute

	// dialogTickBudget 是单轮 tick 的总时间预算。
	//
	// 为什么要它：syncOne 是**串行阻塞**的，单个用户最长可跑
	// dialogSyncTimeout（5 分钟）。多账号场景下一轮 tick 可能
	// 远远超过 dialogSyncTick（10 分钟）—— 结果是 ticker 事件堆积、
	// 调度器一刻不停地在跑、日志刷屏。
	//
	// 取 8 分钟（略小于 tick 间隔）：留出空档，让每轮之间有喘息，
	// 也让"日志上看不出调度器到底在跑还是在歇"这件事消失。
	dialogTickBudget = 8 * time.Minute
)

// DialogView 返回给前端的「已关注频道」条目
type DialogView struct {
	ChannelId int64  `json:"channelId"`
	Title     string `json:"title"`
	Username  string `json:"username,omitempty"`
	IsChannel bool   `json:"isChannel"`
	// Scanned 是否已经扫过（在 channel_scans 里有记录且有文件夹）
	Scanned  bool  `json:"scanned"`
	Imported int64 `json:"imported"`
	// FileCount 网盘里这个频道文件夹下的文件数
	FileCount int64 `json:"fileCount"`
}

// ---------------------------------------------------------------------------
// 写路径：把对话列表同步进库
// ---------------------------------------------------------------------------

// SyncUserDialogs 拉取该用户关注的全部频道/群组，覆盖写入 tg_dialogs。
//
// 「覆盖」的语义：先按 user_id 删掉旧记录，再批量插入新的。
// 这样取关的频道会自动消失，不需要做差异比对。
// 整个过程放在一个事务里，避免删完插入失败导致列表空白。
func (a *apiService) SyncUserDialogs(ctx context.Context) error {
	userId := auth.GetUser(ctx)
	if userId == 0 {
		return &apiError{err: errors.New("登录已过期，请重新登录后再试"), code: 401}
	}
	return a.syncDialogsForUser(ctx, userId, auth.GetJWTUser(ctx))
}

// syncDialogsForUser 是同步的实体。调度器也调它。
func (a *apiService) syncDialogsForUser(ctx context.Context, userId int64,
	jwtUser *types.JWTClaims) error {

	if jwtUser == nil || jwtUser.TgSession == "" {
		return &apiError{err: errors.New("找不到 TG 会话，请先在网页里登录"), code: 401}
	}

	client, err := tgc.AuthClient(ctx, &a.cnf.TG, jwtUser.TgSession,
		a.newMiddlewares(ctx, 5)...)
	if err != nil {
		return &apiError{err: err}
	}

		var dialogs []models.TGDialog

	// notModified 标记：TG 说"列表没变"时，这一轮一个 dialog 都拿不到。
	// 此时**绝不能用空列表去覆盖写库** —— 那等于把用户已缓存的频道全删了。
	// 声明在闭包外，因为写库决策在 client.Run 之后才做。
	notModified := false

	// complete 标记：这一轮是否把整个对话列表**完整**翻完了。
	//
	// 为什么必须有：TG 的 dialogs 分页返回的 chats 数组是
	// "到目前为止所有页的累积集合"，只有翻到最后一页才是完整的。
	// 如果中途某页报错（网络抖动、限流超时），dialogs 里只有**前半截**，
	// 这时候去做"先删后插"覆盖写，等于把后半截频道全删了。
	// 所以只有 complete == true 才允许覆盖写，否则本轮作废。
	complete := false

	err = client.Run(ctx, func(ctx context.Context) error {
		tgAPI := client.API()

		offsetPeer := tg.InputPeerClass(&tg.InputPeerEmpty{})
		offsetDate := 0
		offsetID := 0

		seen := map[int64]bool{}

		for page := 0; page < dialogMaxPages; page++ {
			req := &tg.MessagesGetDialogsRequest{
				OffsetPeer: offsetPeer,
				OffsetDate: offsetDate,
				OffsetID:   offsetID,
				Limit:      dialogPageLimit,
			}

			res, err := tgAPI.MessagesGetDialogs(ctx, req)
			if err != nil {
				// 限流就等一下重试这一次，其它错误直接上抛。
				// 等太久（超过总超时的一半）就没必要等了，交给退避逻辑。
				if d, ok := floodWaitDuration(err); ok {
					if d > dialogSyncTimeout/2 {
						return err
					}
					time.Sleep(d)
					continue
				}
				return err
			}

			// MessagesGetDialogs 的返回可能是
			//   *tg.MessagesDialogs（旧格式，一次给全）
			//   *tg.MessagesDialogsSlice（分页）
			//   *tg.MessagesDialogsNotModified（没变化）
			var (
				chatList []tg.ChatClass
				userList []tg.UserClass
				dlgList  []tg.DialogClass
				finish   bool
			)

			switch m := res.(type) {
			case *tg.MessagesDialogs:
				chatList = m.Chats
				userList = m.Users
				dlgList = m.Dialogs
				finish = len(m.Dialogs) < dialogPageLimit
			case *tg.MessagesDialogsSlice:
				chatList = m.Chats
				userList = m.Users
				dlgList = m.Dialogs
				finish = len(m.Dialogs) < dialogPageLimit
			case *tg.MessagesDialogsNotModified:
				// 列表没变化（TG 按 hash 缓存判定）。
				// 注意这里**不能**当成"正常拿到空列表"处理：
				// 空列表会让后面的覆盖写把库里已有的记录删干净。
				// 正确做法是把整轮标记为"无变化"，直接跳过写库。
				notModified = true
				finish = true
			default:
				finish = true
			}

			for _, c := range chatList {
				ch, ok := c.(*tg.Channel)
				if !ok {
					// 只收频道/超级群，普通小群（*tg.Chat）不要
					continue
				}
				if seen[ch.ID] {
					continue
				}
				seen[ch.ID] = true

				// 能出现在对话列表里就说明已经关注/加入了，不用再过滤。
				d := models.TGDialog{
					UserId:      userId,
					ChannelId:   ch.ID,
					AccessHash:  ch.AccessHash,
					Title:       ch.Title,
					Username:    ch.Username,
					IsChannel:   ch.Broadcast,
					MemberCount: ch.ParticipantsCount,
					SyncedAt:    time.Now().UTC(),
				}
				dialogs = append(dialogs, d)
			}

			if finish || len(dlgList) == 0 {
				// 正常翻到底（或本页已空），整轮数据完整
				complete = true
				return nil
			}

			// ---- 计算下一页游标 ----
			//
			// DL 里坑比较多，逐条说明：
			//
			// 1) DialogClass 有两个实现：*tg.Dialog 和 *tg.DialogFolder。
			//    后者是"聊天文件夹"（用户自己分的组），**没有 TopMessage**。
			//    如果它恰好排在页尾、而我们直接断言 *tg.Dialog 失败就 return，
			//    分页会在这里**中断** —— 后面所有频道都收不到。
			//    所以要从后往前找，跳过 Folder 取第一条真正的 Dialog。
			//
			// 2) offsetPeer 必须**无条件**跟着最后一条 dialog 更新。
			//    它和 offsetID 是一对游标，TG 按「(peer, id) 之后」找下一页。
			//    如果只对 PeerChannel 更新、其它类型（私聊 PeerUser、
			//    小群 PeerChat）保持上一页的值，游标就错位了 ——
			//    轻则漏掉中间的频道，重则反复翻同一页直到撞上页数上限。
			//
			// 3) offsetDate 用的是消息**日期**，不是 ID。这里取不到日期
			//    （dialogs 只带 TopMessage id），所以传 0 表示"不限日期"。
			//    TG 见到 date=0 会退化成纯按 (peer, id) 定位，这正好是我们
			//    想要的语义，比硬把 id 塞进 date 干净。
			var cursor *tg.Dialog
			for i := len(dlgList) - 1; i >= 0; i-- {
				if d, ok := dlgList[i].(*tg.Dialog); ok && d.TopMessage != 0 {
					cursor = d
					break
				}
			}
			if cursor == nil {
				// 整页都没有可用 Dialog（理论上不会），硬翻会死循环，收工
				return nil
			}

			offsetID = cursor.TopMessage
			offsetDate = 0

			switch peer := cursor.Peer.(type) {
			case *tg.PeerChannel:
				offsetPeer = &tg.InputPeerChannel{
					ChannelID:  peer.ChannelID,
					AccessHash: accessHashOf(chatList, peer.ChannelID),
				}
			case *tg.PeerChat:
				offsetPeer = &tg.InputPeerChat{ChatID: peer.ChatID}
			case *tg.PeerUser:
				offsetPeer = &tg.InputPeerUser{
					UserID:     peer.UserID,
					AccessHash: accessHashOfUser(userList, peer.UserID),
				}
			default:
				// 认不出的 peer 类型：不动 offsetPeer，
				// 但至少 offsetID 推进了，不会原地打转
				_ = peer
			}

			// 降速，避免触发限流
			time.Sleep(300 * time.Millisecond)
		}

		return nil
	})

	if err != nil {
		return &apiError{err: err}
	}

	// 该不该拿这一轮的数据去覆盖写库？
	// 抽成纯函数是为了能单测 —— 这个判断错了会**静默删用户数据**，
	// 属于最不该靠人眼审查的一类逻辑。
	if !shouldOverwriteDialogs(notModified, complete) {
		if notModified {
			logging.Component("DIALOG").Debug("dialog.sync.not_modified",
				zap.Int64("user_id", userId))
		} else {
			logging.Component("DIALOG").Warn("dialog.sync.incomplete",
				zap.Int64("user_id", userId),
				zap.Int("partial", len(dialogs)),
				zap.String("action", "skip_overwrite"))
		}
		return nil
	}

	// 覆盖写：先删后插，放在一个事务里。
	// 删完插入失败会整体回滚，不会留下空列表。
	return a.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("user_id = ?", userId).
			Delete(&models.TGDialog{}).Error; err != nil {
			return err
		}
		if len(dialogs) == 0 {
			// 完整翻到底、但确实一个频道都没有 → 用户全取关了，清空是对的
			return nil
		}
		// OnConflict 兜底：同一批里理论上不会有重复（seen 已去重），
		// 但 TG 返回的 chats 数组偶尔会重复给出同一个频道，
		// 没有这个子句会整批插入失败（主键冲突）。
		return tx.Clauses(clause.OnConflict{
			Columns: []clause.Column{{Name: "user_id"}, {Name: "channel_id"}},
			DoUpdates: clause.AssignmentColumns([]string{
				"access_hash", "title", "username", "is_channel",
				"member_count", "synced_at",
			}),
		}).CreateInBatches(&dialogs, 200).Error
	})
}

// shouldOverwriteDialogs 决定这一轮同步的结果能不能拿去覆盖写库。
//
// 覆盖写是「先按 user_id 删光、再插入新列表」，所以一旦判断错，
// 用户的频道列表会被**静默清空** —— 界面上看起来就是"我关注的频道全没了"。
//
// 两个必须拦住的情况：
//
//	notModified  TG 按 hash 判定"列表没变"，这一轮压根没返回数据。
//	             此时 dialogs 是空的，覆盖写 = 清空。必须跳过（库里那份就是最新的）。
//
//	!complete    没把整个对话列表翻完（中途报错、或撞上 dialogMaxPages 上限）。
//	             此时 dialogs 只有前半截，覆盖写 = 把后半截频道删掉。
//	             宁可这轮不更新，等下轮重试。
//
// 注意这里**不检查 len(dialogs) == 0**：完整翻到底却一条频道都没有，
// 是"用户把关注的频道全取关了"的正常情况，这时**应该**清空。
// 区分"没拿到数据"和"确实没有数据"，是这段逻辑的核心。
func shouldOverwriteDialogs(notModified, complete bool) bool {
	return !notModified && complete
}

// floodWaitDuration 从错误里取 FLOOD_WAIT 的等待时长。
//
// 单独抽出来是为了把 tgerr 的细节收敛在一处 ——
// 上层只关心"要等多久"，不关心错误怎么包装的。
func floodWaitDuration(err error) (time.Duration, bool) {
	return tgerr.AsFloodWait(err)
}

// accessHashOf 从这一页返回的 chats/users 里找出指定 peer 的 access_hash。
//
// 翻页时要构造 InputPeer（Channel/User 都需要 access_hash），
// 而 dialog 本身只带 peer_id，不带 hash —— 得回本页的 chats/users 数组里捞。
//
// 捞不到就给 0：TG 对"已关注的频道/已加的好友"通常允许 hash=0 的 InputPeer
// （走 MTProto 的 peer 解析）。实在不行下一页会少几条，不影响主流程，
// 也绝不会因为一个 hash 找不到就把整个同步搞崩。
//
// 注意 chats 里**既有频道也有小群**：*tg.Chat 没有 AccessHash 字段，
// 只有 *tg.Channel 和 *tg.User 才有。所以这里两种都要试。
func accessHashOf(peers []tg.ChatClass, peerID int64) int64 {
	for _, c := range peers {
		switch p := c.(type) {
		case *tg.Channel:
			if p.ID == peerID {
				return p.AccessHash
			}
		case *tg.Chat:
			// 小群没有 access_hash，ID 匹配上了也没用，继续找
			_ = p
		}
	}
	return 0
}

// accessHashOfUser 从 users 数组里找用户的 access_hash
func accessHashOfUser(users []tg.UserClass, userID int64) int64 {
	for _, u := range users {
		if usr, ok := u.(*tg.User); ok && usr.ID == userID {
			return usr.AccessHash
		}
	}
	return 0
}

// ---------------------------------------------------------------------------
// 读路径：接口只查库
// ---------------------------------------------------------------------------

// ListDialogsForUser 列出该用户已关注的频道（含"是否扫过"标记）。
//
// 只读 tg_dialogs + channel_scans + files 做关联，不发任何 TG 请求。
func (a *apiService) ListDialogsForUser(ctx context.Context, includeGroups bool) ([]DialogView, error) {
	userId := auth.GetUser(ctx)
	if userId == 0 {
		return nil, &apiError{err: errors.New("登录已过期，请重新登录后再试"), code: 401}
	}

	q := a.db.Where("user_id = ?", userId)
	if !includeGroups {
		// 只列广播频道。群聊里很少有人发影视资源，
		// 混进来只会让列表变长、更难找。
		q = q.Where("is_channel = ?", true)
	}

	var rows []models.TGDialog
	if err := q.Order("title ASC, channel_id ASC").Find(&rows).Error; err != nil {
		return nil, &apiError{err: err}
	}

	// 一次性把这批频道的扫描状态捞出来，避免 N+1
	ids := make([]int64, 0, len(rows))
	for _, r := range rows {
		ids = append(ids, r.ChannelId)
	}

	type scanBrief struct {
		ChannelId     int64
		FolderID      string
		TotalImported int64
	}
	scanMap := map[int64]scanBrief{}
	if len(ids) > 0 {
		var scans []scanBrief
		if err := a.db.Model(&models.ChannelScan{}).
			Select("channel_id, folder_id, total_imported").
			Where("user_id = ? AND channel_id IN ?", userId, ids).
			Scan(&scans).Error; err != nil {
			return nil, &apiError{err: err}
		}
		for _, s := range scans {
			scanMap[s.ChannelId] = s
		}
	}

	// 文件夹下的文件数：一次 group by 拿全，别一个个 Count
	type cntRow struct {
		ParentId string
		Cnt      int64
	}
	folderCnt := map[string]int64{}
	folderIDs := make([]string, 0, len(scanMap))
	for _, s := range scanMap {
		if s.FolderID != "" {
			folderIDs = append(folderIDs, s.FolderID)
		}
	}
	if len(folderIDs) > 0 {
		var cnts []cntRow
		if err := a.db.Model(&models.File{}).
			Select("parent_id, COUNT(*) as cnt").
			Where("user_id = ? AND parent_id IN ? AND status = ?", userId, folderIDs, "active").
			Group("parent_id").
			Scan(&cnts).Error; err != nil {
			return nil, &apiError{err: err}
		}
		for _, c := range cnts {
			folderCnt[c.ParentId] = c.Cnt
		}
	}

	out := make([]DialogView, 0, len(rows))
	for _, r := range rows {
		v := DialogView{
			ChannelId: r.ChannelId,
			Title:     r.Title,
			Username:  r.Username,
			IsChannel: r.IsChannel,
		}
		if s, ok := scanMap[r.ChannelId]; ok {
			// 有 folder_id 才算"真的扫过并建了文件夹"。
			// 只登记了没扫过的（folder_id 为空）不算 —— 否则界面会显示
			// 「已导入 0 个」的"已扫过"频道，用户点进去发现网盘里啥也没有。
			v.Scanned = s.FolderID != ""
			v.Imported = s.TotalImported
			v.FileCount = folderCnt[s.FolderID]
		}
		out = append(out, v)
	}

	// 稳定排序：先把「扫过的」排前面（用户更常点这些），再按名字。
	// 用 sort.SliceStable 保证同名时顺序不抖。
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Scanned != out[j].Scanned {
			return out[i].Scanned
		}
		return out[i].Title < out[j].Title
	})

	return out, nil
}

// ---------------------------------------------------------------------------
// 定时同步调度器
// ---------------------------------------------------------------------------

type dialogScheduler struct {
	svc *apiService

	mu sync.Mutex
	// nextRun 记录每个用户的下次同步时间（失败退避用）
	nextRun map[int64]time.Time
	// failCount 记录每个用户的连续失败次数
	failCount map[int64]int
	// running 防止同一用户重入
	running map[int64]bool

	stopOnce sync.Once
	stopCh   chan struct{}
}

func newDialogScheduler(svc *apiService) *dialogScheduler {
	return &dialogScheduler{
		svc:       svc,
		nextRun:   map[int64]time.Time{},
		failCount: map[int64]int{},
		running:   map[int64]bool{},
		stopCh:    make(chan struct{}),
	}
}

// StartDialogScheduler 启动对话列表同步调度器（非阻塞、幂等）。
func (e *extendedService) StartDialogScheduler() {
	e.dialogOnce.Do(func() {
		s := newDialogScheduler(e.api)
		go s.loop()
		logging.Component("DIALOG").Info("dialog.scheduler.started",
			zap.Duration("interval", dialogSyncInterval))
	})
}

func (s *dialogScheduler) loop() {
	logger := logging.Component("DIALOG")

	select {
	case <-time.After(dialogSyncStartupDelay):
	case <-s.stopCh:
		return
	}

	ticker := time.NewTicker(dialogSyncTick)
	defer ticker.Stop()

	for {
		select {
		case <-s.stopCh:
			logger.Info("dialog.scheduler.stopped")
			return
		case <-ticker.C:
			s.tick(logger)
		}
	}
}

// tick 找出「有 TG 会话、且到点该同步」的用户并逐个同步。
//
// 注意这里不是"所有用户"，而是"有 session 的用户" ——
// 没登录过的用户根本没法调 TG，捞出来只会白报错。
//
// 执行模型：**串行 + 单轮预算**。
//   - 串行：同时拉多个账号的对话列表会把 TG 配额打爆，必须排队。
//     （这点和 scan_scheduler 一致）
//   - 单轮预算：单个用户的同步最长可跑 dialogSyncTimeout（5 分钟）。
//     如果账号多，一轮 tick 可能远超 tick 间隔。这里加一个时间预算，
//     跑到预算就把剩下的用户留给下一轮 —— 否则 ticker 会堆积，
//     且 tick 之间没有任何喘息，日志也会糊成一团。
func (s *dialogScheduler) tick(logger *zap.Logger) {
	var sessions []models.Session
	if err := s.svc.db.Find(&sessions).Error; err != nil {
		logger.Error("dialog.scheduler.query_failed", zap.Error(err))
		return
	}
	if len(sessions) == 0 {
		return
	}

	deadline := time.Now().Add(dialogTickBudget)
	now := time.Now()

	for i := range sessions {
		sess := &sessions[i]

		s.mu.Lock()
		if next, ok := s.nextRun[sess.UserId]; ok && now.Before(next) {
			s.mu.Unlock()
			continue
		}
		if s.running[sess.UserId] {
			s.mu.Unlock()
			continue
		}
		s.running[sess.UserId] = true
		s.mu.Unlock()

		s.syncOne(logger, sess)

		s.mu.Lock()
		s.running[sess.UserId] = false
		s.mu.Unlock()

		// 超预算就收工，剩下的下轮再说 ——
		// 把 nextRun 留给它们，下轮 tick 会立刻接上
		if time.Now().After(deadline) {
			logger.Info("dialog.scheduler.budget_exhausted",
				zap.Int("done", i+1), zap.Int("total", len(sessions)))
			return
		}
	}
}

func (s *dialogScheduler) syncOne(logger *zap.Logger, sess *models.Session) {
	lg := logger.With(zap.Int64("user_id", sess.UserId))

	ctx, cancel := context.WithTimeout(context.Background(), dialogSyncTimeout)
	defer cancel()

	claims := &types.JWTClaims{
		Hash:      sess.Hash,
		TgSession: sess.Session,
	}
	claims.Subject = strconv.FormatInt(sess.UserId, 10)
	ctx = auth.WithUser(ctx, claims)

	start := time.Now()
	err := s.svc.syncDialogsForUser(ctx, sess.UserId, claims)
	elapsed := time.Since(start)

	if err != nil {
		lg.Warn("dialog.sync_failed", zap.Error(err), zap.Duration("elapsed", elapsed))
		s.recordFailure(sess.UserId)
		return
	}

	s.mu.Lock()
	delete(s.failCount, sess.UserId)
	s.nextRun[sess.UserId] = time.Now().Add(dialogSyncInterval)
	s.mu.Unlock()

	lg.Info("dialog.synced", zap.Duration("elapsed", elapsed))
}

func (s *dialogScheduler) recordFailure(userId int64) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.failCount[userId]++
	n := s.failCount[userId]
	// 指数退避：30min, 1h, 2h ... 上限 6h
	backoff := dialogSyncInterval * time.Duration(1<<min(n-1, 4))
	if backoff > dialogSyncMaxBackoff {
		backoff = dialogSyncMaxBackoff
	}
	s.nextRun[userId] = time.Now().Add(backoff)
}

// Stop 停止调度器
func (s *dialogScheduler) Stop() {
	s.stopOnce.Do(func() { close(s.stopCh) })
}

// HasDialogs 判断该用户有没有缓存过对话列表（用于决定是否立即同步一次）
func (a *apiService) HasDialogs(ctx context.Context, userId int64) bool {
	var n int64
	if err := a.db.Model(&models.TGDialog{}).
		Where("user_id = ?", userId).Count(&n).Error; err != nil {
		return false
	}
	return n > 0
}

// ---------------------------------------------------------------------------
// 首次填充（冷启动）
// ---------------------------------------------------------------------------

// ensureDialogsOnce 是「同一用户只同步一次」的闸门。
//
// 场景：用户刚登录完，库里 tg_dialogs 是空的，而调度器要等到
// 启动延迟 60s + 下一个 10 分钟 tick 才会跑 —— 用户打开扫描页看到空列表，
// 会以为"这功能坏了"。
//
// 所以读路径上加一个兜底：**列表为空时**顺手同步一次。
// 为什么要加锁、而且按用户分锁：
//   - 前端可能并发发多个请求（切页签、快速刷新）
//   - 不加锁的话会同时发好几路 TG getDialogs，直接把自己限流了
var (
	dialogFillMu    sync.Mutex
	dialogFillUsers = map[int64]bool{}
)

// tryAcquireFill 抢到"该由我来填充"的权利。抢不到说明别人正在填。
func (a *apiService) tryAcquireFill(userId int64) bool {
	dialogFillMu.Lock()
	defer dialogFillMu.Unlock()
	if dialogFillUsers[userId] {
		return false
	}
	dialogFillUsers[userId] = true
	return true
}

func (a *apiService) releaseFill(userId int64) {
	dialogFillMu.Lock()
	delete(dialogFillUsers, userId)
	dialogFillMu.Unlock()
}

// fillDialogsIfEmpty 列表为空时同步一次，给用户一个"能立刻用"的体验。
//
// 语义要点：
//   - 只在**空**的时候做，有数据就直接返回（否则每次开页面都卡几秒）
//   - 抢不到填充权就直接返回（别人在填，等下一次请求就能拿到数据）
//   - 失败**不返回错误**：这是体验优化，不是功能路径。
//     失败了前端还是拿到空列表 + 调度器会兜底，不该因此报 500。
func (a *apiService) fillDialogsIfEmpty(ctx context.Context, userId int64) {
	claims := auth.GetJWTUser(ctx)
	if claims == nil {
		return
	}
	if a.HasDialogs(ctx, userId) {
		return
	}
	if !a.tryAcquireFill(userId) {
		return
	}
	defer a.releaseFill(userId)

	// 用独立 context：HTTP 请求可能被前端 abort（用户切页签），
	// 但同步本身值得跑完，否则下次又得从头来。
	// 超时沿用调度器那套；claims 从原 ctx 取出来带过去
	// （syncDialogsForUser 靠它拿 TG session）。
	syncCtx, cancel := context.WithTimeout(context.Background(), dialogSyncTimeout)
	defer cancel()
	syncCtx = auth.WithUser(syncCtx, claims)

	if err := a.syncDialogsForUser(syncCtx, userId, claims); err != nil {
		logging.Component("DIALOG").Warn("dialog.first_fill_failed",
			zap.Int64("user_id", userId), zap.Error(err))
	}
}
