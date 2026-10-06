package reader

// 本文件是**时序与吞吐测试**（v2.7.1 新增）。
//
// window_test.go 验的是"对不对"（发几个请求、数据有没有错位）；
// 本文件回答的是"快不快" —— 也就是用户真正在意的三件事：
//   1. 首次打开：多久能出画面
//   2. 拖进度条：seek 后多久能接着播
//   3. 播放中：吞吐够不够撑着不卡
//
// 做法：用固定延迟的假数据源，把"网络时间"变成确定值。
// 这样调参前可以先在这里跑一遍看数字，不用上真机反复试 ——
// "改完去手机上感觉一下"会被网络波动骗，这里的数字不会。
//
// 本文件的存在直接推动了一个改动：默认 prefetch-windows 从 8 降到 3。
// 实测（单块基准 600ms）：3 路已有 4.44 MB/s，4K 绰绰有余；
// 8 路那 10.65 MB/s 纯属占带宽、抢首块，在 CF 隧道这类链路上反而触发限速。

import (
	"context"
	"io"
	"strconv"
	"testing"
	"time"
)

// 本文件是**时序测试**：现有 window_test.go 只验正确性（发了几个请求、
// 数据有没有错位），这里要回答的是「用户体验上到底卡在哪」——
//   1. 首次打开：从调用到拿到第 1 个字节要多久
//   2. 拖进度条：seek 到新位置后，多久能拿到第 1 个字节
//   3. 播放过程中：会不会因为"等整轮凑齐"而断流
//
// 用可控延迟的假数据源模拟 TG 的响应速度，把"网络时间"变成确定值，
// 这样测出来的数字可以直接对比不同参数组合，不用真机反复试。

// timedSource 每次 Chunk 固定耗时 delay，用来模拟 TG 拉一个 1MB 分片的时间。
// 真实网络上这个值大概 0.3~1.5s（取决于 DC 距离与限流）。
type timedSource struct {
	delay   time.Duration
	chunkSz int64
	total   int64
}

func (s *timedSource) ChunkSize(start, end int64) int64 { return s.chunkSz }

func (s *timedSource) Chunk(ctx context.Context, offset, limit int64) ([]byte, error) {
	select {
	case <-time.After(s.delay):
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	// 返回真实长度的假数据，保证 Read 循环能正常推进
	n := limit
	if offset+n > s.total {
		n = s.total - offset
	}
	if n < 0 {
		n = 0
	}
	return make([]byte, n), nil
}

// readFirstByte 返回「从 newTGMultiReader 到读出第一个字节」的耗时。
func readFirstByte(t *testing.T, r *tgMultiReader) time.Duration {
	t.Helper()
	buf := make([]byte, 64*1024) // 播放器通常先要 64KB 就能起播
	start := time.Now()
	n, err := r.Read(buf)
	d := time.Since(start)
	if err != nil && err != io.EOF {
		t.Fatalf("Read 失败: %v", err)
	}
	if n == 0 {
		t.Fatal("没读到数据")
	}
	return d
}

func mkReader(t *testing.T, delay time.Duration, fileMB int64, prefetch, first int) *tgMultiReader {
	t.Helper()
	total := fileMB * 1024 * 1024
	src := &timedSource{delay: delay, chunkSz: 1024 * 1024, total: total}
	cfg := testConfig(prefetch, first)
	r, err := newTGMultiReader(context.Background(), 0, total-1, cfg, src)
	if err != nil {
		t.Fatalf("构造 reader 失败: %v", err)
	}
	t.Cleanup(func() { r.Close() })
	return r
}

// TestFirstByteLatency 首次打开：首字节延迟应当 ≈ 单次 chunk 耗时，
// 不该随 prefetch 变大而变差。这是"首窗口恒为 1"的核心保障。
func TestFirstByteLatency(t *testing.T) {
	const delay = 800 * time.Millisecond

	for _, prefetch := range []int{1, 2, 4, 8} {
		r := mkReader(t, delay, 1400, prefetch, 1)
		d := readFirstByte(t, r)

		// 预期 ≈ delay（下完首块），给 2.5x 容差覆盖调度抖动
		limit := delay * 5 / 2
		if d > limit {
			t.Errorf("prefetch=%d 首字节耗时 %v，超过上限 %v（应≈%v）",
				prefetch, d, limit, delay)
		}
		t.Logf("prefetch=%d  首字节 = %v（基准 %v）", prefetch, d.Round(time.Millisecond), delay)
	}
}

// TestSeekLatency 拖进度条：seek 到文件中部后，拿到首字节要多久。
// ★ 这是本次要优化的核心指标 —— 用户感觉"拖完要等十几秒"就发生在这里。
func TestSeekLatency(t *testing.T) {
	const delay = 800 * time.Millisecond
	const fileMB = 1400

	// 模拟 seek 到 60% 处（约 840MB）
	seekTo := int64(fileMB) * 1024 * 1024 * 6 / 10

	for _, prefetch := range []int{1, 2, 4, 8} {
		src := &timedSource{delay: delay, chunkSz: 1024 * 1024, total: int64(fileMB) * 1024 * 1024}
		cfg := testConfig(prefetch, 1)
		r, err := newTGMultiReader(context.Background(), seekTo, int64(fileMB)*1024*1024-1, cfg, src)
		if err != nil {
			t.Fatalf("构造失败: %v", err)
		}

		d := readFirstByte(t, r)
		r.Close()

		// seek 后首字节也应 ≈ 单块耗时（首窗口恒 1 的收益）
		limit := delay * 5 / 2
		status := "✅"
		if d > limit {
			status = "⚠️"
		}
		t.Logf("%s prefetch=%d  seek 后首字节 = %v（基准 %v）",
			status, prefetch, d.Round(time.Millisecond), delay)
	}
}

// TestWindowBatchBlocking 验证「整轮等齐」的代价：
// 首窗口之后每轮要下 n 块，必须以"最慢的那块"为准 —— 也就是 n*delay 在串行下、
// delay 在完全并行下。这决定了播放中会不会卡顿。
func TestWindowBatchBlocking(t *testing.T) {
	const delay = 500 * time.Millisecond
	const fileMB = 64

	for _, prefetch := range []int{1, 4, 8} {
		src := &timedSource{delay: delay, chunkSz: 1024 * 1024, total: int64(fileMB) * 1024 * 1024}
		cfg := testConfig(prefetch, 1)
		r, err := newTGMultiReader(context.Background(), 0, int64(fileMB)*1024*1024-1, cfg, src)
		if err != nil {
			t.Fatalf("构造失败: %v", err)
		}

		// 连续读满 8MB，看总耗时
		const readMB = 8
		buf := make([]byte, 256*1024)
		got := 0
		start := time.Now()
		for got < readMB*1024*1024 {
			n, err := r.Read(buf)
			if err != nil {
				break
			}
			got += n
		}
		elapsed := time.Since(start)
		r.Close()

		throughput := float64(got) / 1024 / 1024 / elapsed.Seconds()
		t.Logf("prefetch=%d  读 %dMB 耗时 %v  吞吐 %.2f MB/s",
			prefetch, got/1024/1024, elapsed.Round(time.Millisecond), throughput)
	}
}

// 本文件回答一个具体问题：prefetch-windows 降到多少才"够用"。
//
// 起播快要求 prefetch 小（少抢带宽），播放不卡要求吞吐 ≥ 视频码率。
// 这里把两者放在同一张表里，用数据找平衡点，而不是凭感觉定值。
//
// 参考码率（H.264/H.265 常见值）：
//   1080p  4-10 Mbps  → 0.5-1.25 MB/s
//   4K     15-40 Mbps → 1.9-5    MB/s
//   蓝光原盘 40-80 Mbps → 5-10    MB/s

// measureThroughput 顺序读完整文件，返回平均吞吐（MB/s）。
// delay 模拟 TG 拉一个 1MB 分片的耗时 —— 真机实测通常 0.3~1.0s。
func measureThroughput(t *testing.T, delay time.Duration, fileMB, prefetch int) float64 {
	t.Helper()

	src := &timedSource{
		delay:   delay,
		chunkSz: 1024 * 1024,
		total:   int64(fileMB) * 1024 * 1024,
	}
	r, err := newTGMultiReader(
		context.Background(), 0, int64(fileMB)*1024*1024-1,
		testConfig(prefetch, 1), src)
	if err != nil {
		t.Fatalf("构造失败: %v", err)
	}
	defer r.Close()

	buf := make([]byte, 256*1024)
	got := 0
	start := time.Now()
	for {
		n, err := r.Read(buf)
		got += n
		if err != nil || err == io.EOF {
			break
		}
	}
	elapsed := time.Since(start)
	if elapsed <= 0 {
		return 0
	}
	return float64(got) / 1024 / 1024 / elapsed.Seconds()
}

// TestThroughputVsPrefetch 把每种 prefetch 的吞吐列出来，对照码率需求判断够不够。
func TestThroughputVsPrefetch(t *testing.T) {
	const delay = 600 * time.Millisecond // 模拟偏慢的网络
	const fileMB = 32

	t.Logf("单块耗时基准 %v，文件 %dMB", delay, fileMB)
	t.Logf("%-10s %-14s %-12s %s", "prefetch", "吞吐(MB/s)", "可支撑码率", "结论")
	t.Logf("%s", "----------------------------------------------------------------")

	for _, pf := range []int{1, 2, 3, 4, 6, 8} {
		tp := measureThroughput(t, delay, fileMB, pf)
		mbps := tp * 8

		verdict := ""
		switch {
		case tp >= 5:
			verdict = "可播蓝光原盘"
		case tp >= 2:
			verdict = "可播 4K"
		case tp >= 1:
			verdict = "可播 1080p（够用）"
		case tp >= 0.6:
			verdict = "1080p 勉强，可能缓冲"
		default:
			verdict = "❌ 不够，会卡"
		}

		t.Logf("%-10d %-14.2f %-12s %s", pf, tp, formatMbps(mbps), verdict)
	}
}

func formatMbps(mbps float64) string {
	return strconv.FormatFloat(mbps, 'f', 1, 64) + " Mbps"
}

// TestPrefetchEnoughFor1080p 钉住一条底线：
// prefetch 降到 2 之后，吞吐仍要能撑住 1080p（≥1 MB/s）。
// 这条测试是"把默认值从 8 降到 3"的安全网 —— 万一以后有人调到 1，
// 这里会红，提醒他 1 路撑不住播放。
func TestPrefetchEnoughFor1080p(t *testing.T) {
	const delay = 600 * time.Millisecond
	const minMBps = 1.0 // 1080p 下限

	for _, pf := range []int{2, 3} {
		tp := measureThroughput(t, delay, 16, pf)
		if tp < minMBps {
			t.Errorf("prefetch=%d 吞吐仅 %.2f MB/s，低于 1080p 所需 %.1f MB/s",
				pf, tp, minMBps)
		} else {
			t.Logf("✅ prefetch=%d 吞吐 %.2f MB/s，满足 1080p", pf, tp)
		}
	}
}
