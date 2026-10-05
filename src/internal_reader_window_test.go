package reader

import (
	"context"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/tgdrive/teldrive/internal/config"
)

// fakeChunkSource 记录每次 Chunk 调用的 offset，用来验证"首窗口只有 1 个请求"。
type fakeChunkSource struct {
	mu      sync.Mutex
	calls   []int64
	size    int64 // 单个 chunk 的字节数
	chunkSz int64
	delay   time.Duration
}

func (f *fakeChunkSource) ChunkSize(start, end int64) int64 { return f.chunkSz }

func (f *fakeChunkSource) Chunk(ctx context.Context, offset int64, limit int64) ([]byte, error) {
	if f.delay > 0 {
		select {
		case <-time.After(f.delay):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	f.mu.Lock()
	f.calls = append(f.calls, offset)
	f.mu.Unlock()
	return make([]byte, limit), nil
}

func (f *fakeChunkSource) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

func testConfig(prefetch, first int) *config.TGConfig {
	cfg := &config.TGConfig{}
	cfg.Stream.Concurrency = 1
	cfg.Stream.Buffers = 8
	cfg.Stream.ChunkTimeout = 5 * time.Second
	cfg.Stream.PrefetchWindows = prefetch
	cfg.Stream.FirstWindowChunks = first
	return cfg
}

// 首窗口必须只发 1 个请求 —— 这是整个改动的地基。
func TestFirstWindowIsSingleRequest(t *testing.T) {
	const chunk = 512 * 1024
	src := &fakeChunkSource{chunkSz: chunk, delay: 20 * time.Millisecond}
	// 总共请求 4 个 chunk，首窗口 1 + 后续 8 路足够覆盖
	r, err := newTGMultiReader(context.Background(), 0, chunk*4-1, testConfig(8, 1), src)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()

	// 一开跑，goroutine 立刻会发第一轮。等一小会儿看窗口宽度。
	deadline := time.Now().Add(2 * time.Second)
	for src.callCount() == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	// 首轮请求发出后，立刻采样：此时不应超过 1 个
	// （后续轮会因为 delay 还没返回而被挡住，所以这个采样是可靠的）
	first := src.callCount()
	if first != 1 {
		t.Fatalf("首窗口请求数 = %d，期望 1（首字节优先）", first)
	}
}

// 后续窗口应该放大到 prefetch 指定的宽度。
func TestLaterWindowsWiden(t *testing.T) {
	const chunk = 512 * 1024
	const parts = 40 // 放大后一轮能发 8 个
	src := &fakeChunkSource{chunkSz: chunk, delay: time.Millisecond}
	r, err := newTGMultiReader(context.Background(), 0, chunk*parts-1, testConfig(8, 1), src)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()

	// 读完全部数据
	n, err := io.Copy(io.Discard, r)
	if err != nil {
		t.Fatalf("读取失败: %v", err)
	}
	if want := int64(chunk * parts); n != want {
		t.Fatalf("读到 %d 字节，期望 %d", n, want)
	}
	if got := src.callCount(); got != parts {
		t.Fatalf("chunk 请求数 = %d，期望 %d（每个分片只拉一次）", got, parts)
	}
}

// 数据完整性：读出来的字节必须和"分片按顺序拼接"一致，不能错位。
func TestDataIntegrityAcrossWindows(t *testing.T) {
	const chunk = 4096
	const parts = 7
	src := &patternChunkSource{chunkSize: chunk}

	// 从中间偏移开始读，模拟 Range 请求（播放器拖进度条）
	start := int64(1000)
	end := int64(chunk*parts - 500)
	r, err := newTGMultiReader(context.Background(), start, end, testConfig(4, 1), src)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()

	got, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("读取失败: %v", err)
	}
	if want := int(end - start + 1); len(got) != want {
		t.Fatalf("读到 %d 字节，期望 %d", len(got), want)
	}
	for i, b := range got {
		pos := start + int64(i)
		want := byte(pos % 251)
		if b != want {
			t.Fatalf("第 %d 字节 = %d，期望 %d（读出来的数据错位了）", i, b, want)
		}
	}
}

// patternChunkSource 返回"每个字节 = 绝对偏移 % 251"的流，
// 这样任何位置错位都会被立刻发现。
type patternChunkSource struct {
	chunkSize int64
}

func (p *patternChunkSource) ChunkSize(start, end int64) int64 { return p.chunkSize }

func (p *patternChunkSource) Chunk(ctx context.Context, offset int64, limit int64) ([]byte, error) {
	buf := make([]byte, limit)
	for i := range buf {
		buf[i] = byte((offset + int64(i)) % 251)
	}
	return buf, nil
}

// 单分片（整个 range 落在一个 chunk 里）时，左右裁切必须正确。
func TestSinglePartCuts(t *testing.T) {
	const chunk = 1024
	src := &patternChunkSource{chunkSize: chunk}
	start, end := int64(10), int64(99)
	r, err := newTGMultiReader(context.Background(), start, end, testConfig(8, 1), src)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()

	got, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != int(end-start+1) {
		t.Fatalf("长度 %d，期望 %d", len(got), end-start+1)
	}
	for i, b := range got {
		if want := byte((start + int64(i)) % 251); b != want {
			t.Fatalf("偏移 %d 数据错位: got %d want %d", i, b, want)
		}
	}
}

// prefetch 配 0 / 负数时不能死循环，必须能读完。
func TestDegeneratePrefetch(t *testing.T) {
	const chunk = 512
	const parts = 5
	for _, pf := range []int{0, -1, -100} {
		src := &fakeChunkSource{chunkSz: chunk}
		r, err := newTGMultiReader(context.Background(), 0, chunk*parts-1, testConfig(pf, 1), src)
		if err != nil {
			t.Fatal(err)
		}
		n, err := io.Copy(io.Discard, r)
		r.Close()
		if err != nil {
			t.Fatalf("prefetch=%d 读取失败: %v", pf, err)
		}
		if want := int64(chunk * parts); n != want {
			t.Fatalf("prefetch=%d 读到 %d，期望 %d", pf, n, want)
		}
	}
}

// FirstWindowChunks 配 0 / 负数时应兜底为 1，而不是 0（0 会死循环）。
func TestDegenerateFirstWindow(t *testing.T) {
	const chunk = 512
	const parts = 4
	for _, fw := range []int{0, -5} {
		src := &fakeChunkSource{chunkSz: chunk}
		cfg := testConfig(4, fw)
		r, err := newTGMultiReader(context.Background(), 0, chunk*parts-1, cfg, src)
		if err != nil {
			t.Fatal(err)
		}
		done := make(chan int64, 1)
		go func() {
			n, _ := io.Copy(io.Discard, r)
			done <- n
		}()
		select {
		case n := <-done:
			r.Close()
			if want := int64(chunk * parts); n != want {
				t.Fatalf("firstWindow=%d 读到 %d，期望 %d", fw, n, want)
			}
		case <-time.After(5 * time.Second):
			r.Close()
			t.Fatalf("firstWindow=%d 卡死了（死循环？）", fw)
		}
	}
}
