package services

// ---------------------------------------------------------------------------
// 频道自动扫描调度器
//
// 需求：每隔一两分钟自动扫一遍所有已登记的 TG 频道，发现新视频就自动
//       变成网盘文件。这样用户在频道里看到新片，网盘和播放器里稍后就有。
//
// 设计要点（都是为了不被 Telegram 封号）：
//
//  1. 增量扫描：每轮只拉比游标更新的消息（通常 0 条），几乎零成本。
//     绝不是每轮都全量翻历史 —— 那样几分钟就会吃 FLOOD_WAIT。
//
//  2. 串行执行：同一时刻只有一个频道在被扫描，避免并发打爆 TG 的
//     单账号请求配额。
//
//  3. 指数退避：某个频道连续失败（限流、网络抖动）就拉长它的下次执行
//     间隔，最长退到 1 小时，避免坏频道拖垮整个调度。
//
//  4. 启动延迟：服务刚起来时不要立刻扫，等 TG 客户端连接池预热完、
//     数据库迁移跑完再开始，否则首轮必然失败。
//
//  5. 跳过冷却中的频道：同一频道两次扫描之间至少间隔一个周期间隔，
//     防止某轮耗时很长时下一轮立刻又触发同一个频道。
// ---------------------------------------------------------------------------

import (
	"context"
	"strconv"
	"sync"
	"time"

	"github.com/tgdrive/teldrive/internal/auth"
	"github.com/tgdrive/teldrive/internal/logging"
	"github.com/tgdrive/teldrive/pkg/models"
	"github.com/tgdrive/teldrive/pkg/types"
	"go.uber.org/zap"
)

const (
	// defaultScanInterval 是默认扫描间隔。
	// 用户要求「每隔一两分钟」，这里取 2 分钟。
	defaultScanInterval = 2 * time.Minute

	// minScanInterval 是最小允许间隔，防止把 TG 打爆。
	// 低于 30 秒没有意义：增量扫描本身也要发 1~2 个请求。
	minScanInterval = 30 * time.Second

	// scanStartupDelay 是服务启动后的首轮延迟
	scanStartupDelay = 45 * time.Second

	// scanFailureBackoffBase 是失败后的退避基数
	scanFailureBackoffBase = 2 * time.Minute

	// scanFailureBackoffMax 是失败退避的上限
	scanFailureBackoffMax = 1 * time.Hour

	// scanTick 是调度器的心跳粒度。
	// 每 30 秒醒一次，检查哪些频道到点了，而不是每个频道一个定时器
	// （频道多的时候定时器会很多，且不好统一控制并发）。
	scanTick = 30 * time.Second
)

// scanScheduler 负责按间隔触发各频道的增量扫描。
type scanScheduler struct {
	svc *apiService

	mu sync.Mutex
	// nextRun 记录每个频道的下次可执行时间（失败退避用）
	nextRun map[int64]time.Time
	// failCount 记录每个频道的连续失败次数
	failCount map[int64]int
	// running 防止同一频道重入
	running map[int64]bool

	stopOnce sync.Once
	stopCh   chan struct{}
}

// newScanScheduler 构造调度器
func newScanScheduler(svc *apiService) *scanScheduler {
	return &scanScheduler{
		svc:       svc,
		nextRun:   map[int64]time.Time{},
		failCount: map[int64]int{},
		running:   map[int64]bool{},
		stopCh:    make(chan struct{}),
	}
}

// Start 启动调度器（非阻塞）。
// 通过 e.scanOnce 保证只启动一次，重复调用是安全的。
func (e *extendedService) StartScanScheduler() {
	e.scanOnce.Do(func() {
		s := newScanScheduler(e.api)
		go s.loop()
		logging.Component("SCAN").Info("scan.scheduler.started",
			zap.Duration("interval", defaultScanInterval))
	})
}

// loop 是调度主循环
func (s *scanScheduler) loop() {
	logger := logging.Component("SCAN")

	// 启动延迟：等 TG 连接池与数据库就绪
	select {
	case <-time.After(scanStartupDelay):
	case <-s.stopCh:
		return
	}

	ticker := time.NewTicker(scanTick)
	defer ticker.Stop()

	for {
		select {
		case <-s.stopCh:
			logger.Info("scan.scheduler.stopped")
			return
		case <-ticker.C:
			s.tick()
		}
	}
}

// tick 检查并执行到期的频道扫描
func (s *scanScheduler) tick() {
	logger := logging.Component("SCAN")

	// 取出所有启用的频道
	var scans []models.ChannelScan
	if err := s.svc.db.Where("enabled = ?", true).Find(&scans).Error; err != nil {
		logger.Error("scan.scheduler.query_failed", zap.Error(err))
		return
	}
	if len(scans) == 0 {
		return
	}

	now := time.Now()

	for i := range scans {
		st := &scans[i]

		// 检查是否到点
		s.mu.Lock()
		if next, ok := s.nextRun[st.ChannelId]; ok && now.Before(next) {
			s.mu.Unlock()
			continue
		}
		if s.running[st.ChannelId] {
			s.mu.Unlock()
			continue
		}
		s.running[st.ChannelId] = true
		s.mu.Unlock()

		// 串行执行：扫完一个再扫下一个。
		// 并发会同时消耗 TG 的单账号配额，极易触发 FLOOD_WAIT。
		s.scanOne(logger, st)

		s.mu.Lock()
		s.running[st.ChannelId] = false
		s.mu.Unlock()
	}
}

// scanOne 执行单个频道的增量扫描
func (s *scanScheduler) scanOne(logger *zap.Logger, st *models.ChannelScan) {
	interval := defaultScanInterval
	if st.IntervalSeconds != nil && *st.IntervalSeconds > 0 {
		iv := time.Duration(*st.IntervalSeconds) * time.Second
		if iv < minScanInterval {
			iv = minScanInterval
		}
		interval = iv
	}

	lg := logger.With(
		zap.Int64("channel_id", st.ChannelId),
		zap.String("channel", st.ChannelName),
	)

	// 构造该用户的 context。
	// 自动扫描没有 HTTP 请求，所以要自己造一个带用户身份的 context
	// （扫描逻辑内部要靠它取 TG 会话与 user_id）。
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	// 取用户的 TG 会话信息
	var session models.Session
	if err := s.svc.db.Where("user_id = ?", st.UserId).First(&session).Error; err != nil {
		lg.Warn("scan.scheduler.no_session", zap.Error(err))
		s.recordFailure(st, "找不到 TG 会话，请先在网页里登录")
		return
	}

	// 构造用户的登录身份塞进 context。
	// 自动扫描没有 HTTP 请求，所以要自己造一个带用户身份的 context
	// （扫描逻辑内部靠它取 user_id 和 TG 会话）。
	claims := &types.JWTClaims{
		Hash:      session.Hash,
		TgSession: session.Session,
	}
	claims.Subject = strconv.FormatInt(st.UserId, 10)
	ctx = auth.WithUser(ctx, claims)

	req := ScanRequest{
		ChannelId:   st.ChannelId,
		Incremental: true, // 关键：只扫新消息
		Limit:       500,  // 单轮上限，避免一轮跑太久
		MinSize:     1024 * 1024,
	}

	start := time.Now()
	res, err := s.svc.FilesScanChannel(ctx, req)
	elapsed := time.Since(start)

	if err != nil {
		lg.Warn("scan.scheduler.scan_failed",
			zap.Error(err), zap.Duration("elapsed", elapsed))
		s.recordFailure(st, err.Error())
		return
	}

	// 成功：重置失败计数
	s.mu.Lock()
	delete(s.failCount, st.ChannelId)
	s.nextRun[st.ChannelId] = time.Now().Add(interval)
	s.mu.Unlock()

	if res.Imported > 0 {
		lg.Info("scan.scheduler.new_files",
			zap.Int("imported", res.Imported),
			zap.String("size", humanSize(res.TotalSize)),
			zap.Duration("elapsed", elapsed))
	} else {
		lg.Debug("scan.scheduler.no_new_files",
			zap.Int("scanned", res.Scanned),
			zap.Duration("elapsed", elapsed))
	}
}

// recordFailure 记录一次失败并计算退避时间
func (s *scanScheduler) recordFailure(st *models.ChannelScan, msg string) {
	now := time.Now().UTC()

	s.mu.Lock()
	s.failCount[st.ChannelId]++
	n := s.failCount[st.ChannelId]
	// 指数退避：2min, 4min, 8min ... 上限 1 小时
	backoff := scanFailureBackoffBase * time.Duration(1<<min(n-1, 5))
	if backoff > scanFailureBackoffMax {
		backoff = scanFailureBackoffMax
	}
	s.nextRun[st.ChannelId] = time.Now().Add(backoff)
	s.mu.Unlock()

	// 把错误写回数据库，前端能看到
	s.svc.db.Model(&models.ChannelScan{}).Where("channel_id = ?", st.ChannelId).
		Updates(map[string]any{
			"last_error": msg,
			"updated_at": now,
		})
}

// Stop 停止调度器（进程退出时调用）
func (s *scanScheduler) Stop() {
	s.stopOnce.Do(func() { close(s.stopCh) })
}
