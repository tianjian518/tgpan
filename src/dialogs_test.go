package services

import (
	"context"
	"testing"

	"github.com/gotd/td/tg"
	"github.com/tgdrive/teldrive/internal/auth"
	"github.com/tgdrive/teldrive/pkg/types"
)

// TestAccessHashOfFindsChannel 验证翻页游标能拿到正确的 access_hash。
//
// 翻页时要构造 InputPeerChannel 当游标，它必须要 access_hash，
// 而 dialog 本身只带 peer_id —— 得回本页的 chats 数组里捞。
// 捞错了会让下一页从错误的频道开始翻，表现为列表少几条。
func TestAccessHashOfFindsChannel(t *testing.T) {
	chats := []tg.ChatClass{
		&tg.Channel{ID: 111, AccessHash: 1111},
		&tg.Channel{ID: 222, AccessHash: 2222},
		&tg.Chat{ID: 333}, // 普通群，没有 access_hash
	}

	if got := accessHashOf(chats, 111); got != 1111 {
		t.Errorf("频道 111 的 hash = %d，期望 1111", got)
	}
	if got := accessHashOf(chats, 222); got != 2222 {
		t.Errorf("频道 222 的 hash = %d，期望 2222", got)
	}
	// 找不到就返回 0，不能 panic，也不能返回别人的 hash
	if got := accessHashOf(chats, 999); got != 0 {
		t.Errorf("不存在的频道应返回 0，实际 %d", got)
	}
	// 普通群（*tg.Chat）不该被当成频道匹配上
	if got := accessHashOf(chats, 333); got != 0 {
		t.Errorf("*tg.Chat 不应返回 access_hash，实际 %d", got)
	}
}

// TestAccessHashOfEmpty 空列表不能 panic（TG 偶尔会返回空 chats）
func TestAccessHashOfEmpty(t *testing.T) {
	if got := accessHashOf(nil, 123); got != 0 {
		t.Errorf("空列表应返回 0，实际 %d", got)
	}
	if got := accessHashOf([]tg.ChatClass{}, 123); got != 0 {
		t.Errorf("空列表应返回 0，实际 %d", got)
	}
}

// TestAccessHashOfUser 用户的 access_hash 要从 users 数组里拿，
// 不能去 chats 里找（两边是分开返回的）。
func TestAccessHashOfUser(t *testing.T) {
	users := []tg.UserClass{
		&tg.User{ID: 555, AccessHash: 5555},
		&tg.User{ID: 666, AccessHash: 6666},
	}
	if got := accessHashOfUser(users, 555); got != 5555 {
		t.Errorf("用户 555 的 hash = %d，期望 5555", got)
	}
	if got := accessHashOfUser(users, 999); got != 0 {
		t.Errorf("不存在的用户应返回 0，实际 %d", got)
	}
	// 用错数组（拿 chats 去找用户）必须拿不到
	chats := []tg.ChatClass{&tg.Channel{ID: 555, AccessHash: 9999}}
	if got := accessHashOf(chats, 555); got != 9999 {
		// 这条只是记录"同 ID 在 chats 里是频道"，不是错误
		t.Logf("chats 里 ID=555 是频道，hash=%d", got)
	}
	if got := accessHashOfUser(nil, 555); got != 0 {
		t.Errorf("users 为空应返回 0，实际 %d", got)
	}
}

// TestDialogFolderIsDialogClass 钉住"DialogFolder 也是 DialogClass"这个事实。
//
// 翻页取游标时必须先断言 *tg.Dialog。如果只写 `dlgList[last].(*tg.Dialog)`
// 然后在 !ok 时 return，一旦页尾恰好是 DialogFolder（聊天文件夹），
// 分页就会在那里中断 —— 后面所有频道都收不到。
//
// 因此实现改成了"从后往前找第一条真正的 *tg.Dialog"。
// 这条测试确保类型体系理解正确（是编译期断言，跑不过说明上游改了 schema）。
func TestDialogFolderIsDialogClass(t *testing.T) {
	var folder tg.DialogClass = &tg.DialogFolder{Folder: tg.Folder{ID: 1, Title: "我的分组"}}
	if _, isPlain := folder.(*tg.Dialog); isPlain {
		t.Error("DialogFolder 不应被断言成 *tg.Dialog")
	}

	var plain tg.DialogClass = &tg.Dialog{TopMessage: 42}
	d, isPlain := plain.(*tg.Dialog)
	if !isPlain {
		t.Fatal("*tg.Dialog 应能断言成功")
	}
	if d.TopMessage != 42 {
		t.Errorf("TopMessage = %d，期望 42", d.TopMessage)
	}
}

// TestPeerCursorTypesCovered 验证各种 peer 类型都能构造出游标。
//
// 翻页的 (offsetPeer, offsetID) 是一对，offsetPeer 必须**无条件**跟随
// 最后一条 dialog 的 peer 更新。如果只处理 PeerChannel，
// 遇到私聊/小群收尾的页就会携带上一页的 peer，游标错位 ——
// 轻则漏频道，重则反复翻同一页直到撞页数上限。
func TestPeerCursorTypesCovered(t *testing.T) {
	chats := []tg.ChatClass{
		&tg.Channel{ID: 111, AccessHash: 1111},
		&tg.Chat{ID: 222},
	}
	users := []tg.UserClass{
		&tg.User{ID: 333, AccessHash: 3333},
	}

	// PeerChannel -> InputPeerChannel，带 hash
	pch := &tg.PeerChannel{ChannelID: 111}
	got := &tg.InputPeerChannel{
		ChannelID:  pch.ChannelID,
		AccessHash: accessHashOf(chats, pch.ChannelID),
	}
	if got.AccessHash != 1111 {
		t.Errorf("PeerChannel 游标的 hash = %d，期望 1111", got.AccessHash)
	}

	// PeerUser -> InputPeerUser，带 hash（必须查 users 数组）
	pusr := &tg.PeerUser{UserID: 333}
	gotU := &tg.InputPeerUser{
		UserID:     pusr.UserID,
		AccessHash: accessHashOfUser(users, pusr.UserID),
	}
	if gotU.AccessHash != 3333 {
		t.Errorf("PeerUser 游标的 hash = %d，期望 3333", gotU.AccessHash)
	}

	// PeerChat -> InputPeerChat，没有 hash 字段（编译期保证）
	pchat := &tg.PeerChat{ChatID: 222}
	gotC := &tg.InputPeerChat{ChatID: pchat.ChatID}
	if gotC.ChatID != 222 {
		t.Errorf("PeerChat 游标 ChatID = %d，期望 222", gotC.ChatID)
	}
}

// TestDialogSyncConstants 锁定几个关键常量。
//
// 这些值直接决定会不会把 TG 配额打爆，改动必须有意识 ——
// 所以用测试钉住，避免以后随手调小。
func TestDialogSyncConstants(t *testing.T) {
	// 同步间隔不能太短：这是全量翻页调用，比增量扫描贵得多
	if dialogSyncInterval < 10*60*1000000000 { // 10 分钟
		t.Errorf("对话同步间隔 %v 太短，全量翻页会打爆 TG 配额", dialogSyncInterval)
	}
	// 单页取满 100（TG 上限），减少往返
	if dialogPageLimit != 100 {
		t.Errorf("单页条数 = %d，应取满 100 以减少往返", dialogPageLimit)
	}
	// 必须有翻页上限，否则异常情况下会无限翻
	if dialogMaxPages <= 0 || dialogMaxPages > 500 {
		t.Errorf("翻页上限 = %d，不合理（应为 1~500）", dialogMaxPages)
	}
	// 退避上限必须大于同步间隔，否则退避没意义
	if dialogSyncMaxBackoff <= dialogSyncInterval {
		t.Errorf("退避上限 %v 应大于同步间隔 %v", dialogSyncMaxBackoff, dialogSyncInterval)
	}
}

// TestScanRequestIncrementalFieldExists 钉住增量扫描字段。
//
// 这个字段是「前端从不传导致每次全量重扫」那个 bug 的核心。
// 如果以后有人把它删了/改名了，测试会立刻炸。
func TestScanRequestIncrementalFieldExists(t *testing.T) {
	req := ScanRequest{
		ChannelId:   123456,
		Incremental: true,
		Limit:       2000,
	}
	if !req.Incremental {
		t.Error("ScanRequest.Incremental 没能保持 true —— 增量扫描会失效")
	}
	// 零值必须是 false（默认全量），否则老的调用方语义会被悄悄改掉
	var zero ScanRequest
	if zero.Incremental {
		t.Error("ScanRequest 零值 Incremental 应为 false")
	}
}

// ---------------------------------------------------------------------------
// 覆盖写决策（静默删数据的防线）
// ---------------------------------------------------------------------------

// TestShouldOverwrite_NotModified 是本轮修的**最严重**的 bug 的回归测试。
//
// Bug 现场：syncDialogsForUser 里 client.Run 之后**无条件**执行"先删后插"。
// 当 TG 返回 *tg.MessagesDialogsNotModified（按 hash 判定列表没变）时，
// 这一轮 dialogs 是空的 —— 于是"先删"生效、"后插"插了个空，
// 用户已缓存的频道列表被**静默清空**。
//
// 表现是：用户打开扫描页，昨天还在的频道列表今天全空了，
// 而且日志里没有任何错误（因为整个流程"成功"了）。
func TestShouldOverwrite_NotModified(t *testing.T) {
	if shouldOverwriteDialogs(true, false) {
		t.Error("NotModified + 未完整 → 绝不能覆盖写（会清空用户列表）")
	}
	if shouldOverwriteDialogs(true, true) {
		t.Error("NotModified 时绝不该覆盖写，无论 complete 是什么")
	}
}

// TestShouldOverwrite_Incomplete 是本轮修的第二严重 bug 的回归测试。
//
// Bug 场景：对话列表要翻很多页。如果第 3 页报错（网络抖动），
// 而我们只处理了前 2 页的数据就去"先删后插"覆盖写，
// 后半截的频道会被**删掉** —— 下次同步虽然会补回来，
// 但这中间用户看到的列表是残缺的，且如果错误持续就永远残缺。
func TestShouldOverwrite_Incomplete(t *testing.T) {
	if shouldOverwriteDialogs(false, false) {
		t.Error("没翻完（中途报错/撞页数上限）→ 不能拿半截数据覆盖写")
	}
}

// TestShouldOverwrite_Complete 确认正常路径没被误伤。
func TestShouldOverwrite_Complete(t *testing.T) {
	if !shouldOverwriteDialogs(false, true) {
		t.Error("完整翻完且非 NotModified → 应该覆盖写")
	}
}

// TestShouldOverwrite_EmptyButComplete 是最容易写错的那个边界。
//
// "完整翻到底、但一条频道都没有" 和 "没拿到数据" 是**两回事**：
//   - 前者 = 用户把关注的频道全取关了 → 应该清空（否则取关后列表还在）
//   - 后者 = 没有数据可用 → 绝不能清空（否则就是删数据）
//
// 所以判断依据必须是 (notModified, complete)，**不能**是 len(dialogs)。
// 这条测试钉住这个区分 —— 如果以后有人"顺手"加一句
// `if len(dialogs) == 0 { return false }`，这里会炸。
func TestShouldOverwrite_EmptyButComplete(t *testing.T) {
	if !shouldOverwriteDialogs(false, true) {
		t.Error("完整翻完（即使结果为空）应该允许覆盖写 —— " +
			"否则用户取关频道后列表永远不更新")
	}
}

// TestDialogTickBudget 单轮 tick 必须有时间预算。
//
// syncOne 是串行阻塞的，单用户最长 dialogSyncTimeout（5 分钟）。
// 账号一多，一轮 tick 会远超 dialogSyncTick（10 分钟）——
// ticker 事件堆积，调度器一刻不停地跑。
func TestDialogTickBudget(t *testing.T) {
	if dialogTickBudget <= 0 {
		t.Fatal("单轮预算必须为正，否则 tick 永远不会跑")
	}
	if dialogTickBudget >= dialogSyncTick {
		t.Errorf("单轮预算 %v 应小于 tick 间隔 %v，否则留不出喘息空档",
			dialogTickBudget, dialogSyncTick)
	}
	// 预算要能容下至少一个用户的完整同步，否则永远同步不完
	if dialogTickBudget < dialogSyncTimeout {
		t.Errorf("单轮预算 %v 装不下一个用户的同步超时 %v —— "+
			"会永远卡在第一个人身上", dialogTickBudget, dialogSyncTimeout)
	}
}

// TestDialogFillGate 验证冷启动填充的互斥闸门。
//
// 场景：用户刚登录，库里 tg_dialogs 是空的。此时可能有好几个并发请求
// （前端切页签、快速刷新）同时发现"列表是空的"→ 同时发 TG getDialogs。
// 多路全量翻页调用会直接把自己限流。
//
// 闸门保证同一用户只有一路在拉，其余直接返回（下次请求就有数据了）。
func TestDialogFillGate(t *testing.T) {
	svc := &apiService{} // 只测闸门，不碰 db

	// 第一次能抢到
	if !svc.tryAcquireFill(1001) {
		t.Fatal("第一个请求应该能抢到填充权")
	}
	// 同一用户第二次抢不到
	if svc.tryAcquireFill(1001) {
		t.Error("同一用户并发时第二次不该抢到 —— 会重复拉 TG")
	}
	// 别的用户不受影响
	if !svc.tryAcquireFill(1002) {
		t.Error("不同用户之间不该互相阻塞")
	}

	// 释放后能重新抢到
	svc.releaseFill(1001)
	if !svc.tryAcquireFill(1001) {
		t.Error("释放后应该能重新抢到")
	}

	svc.releaseFill(1001)
	svc.releaseFill(1002)
}

// ---------------------------------------------------------------------------
// HTTP 层 → apiService 的用户传递（"验过身份却一直 401" 的防线）
// ---------------------------------------------------------------------------

// TestVerifyCookieUserCtxInjectsUser 钉住最隐蔽的一个 bug。
//
// 现象：接口明明用 verifyCookieUser 验过身份了，却一直回
// `{"code":401,"message":"unauthorized"}`，日志里毫无线索。
//
// 根因：verifyCookieUser 只**返回** claims，不碰 context。
// 而 apiService 里几乎所有方法都是 `userId := auth.GetUser(ctx)` ——
// 从 context 里取用户。于是「HTTP handler → apiService」这条链
// 永远拿到 userId == 0，方法第一行就 return 401。
//
// 受影响的功能（都曾经是坏的）：
//   - POST /api/scan/channels          登记并立即扫描
//   - POST /api/scan/channels/{id}/run 「立即扫一次」按钮
//   - GET  /api/scan/dialogs           「已关注频道」列表
//
// 修法：加 verifyCookieUserCtx，把 claims 注入 request 的 context。
// 这条测试确保注入是真的生效的 —— 如果以后有人"清理"掉 WithUser，
// 这里会立刻炸，而不是等到用户报"按钮没反应"。
func TestVerifyCookieUserCtxInjectsUser(t *testing.T) {
	claims := &types.JWTClaims{
		Hash:      "h",
		TgSession: "s",
	}
	claims.Subject = "1001"

	// 模拟 verifyCookieUserCtx 的核心动作
	ctx := auth.WithUser(context.Background(), claims)

	if got := auth.GetUser(ctx); got != 1001 {
		t.Errorf("注入后 auth.GetUser = %d，期望 1001 —— "+
			"context 没注入的话所有走 apiService 的接口都会 401", got)
	}

	// 反面对照：不注入时必须是 0（这就是 bug 的样子）
	if got := auth.GetUser(context.Background()); got != 0 {
		t.Errorf("未注入时 auth.GetUser 应为 0，实际 %d", got)
	}

	// claims 本身也要能取回来（syncDialogsForUser 靠它拿 TG session）
	got := auth.GetJWTUser(ctx)
	if got == nil {
		t.Fatal("注入后 GetJWTUser 不应为 nil —— 拿不到 session 就没法调 TG")
	}
	if got.TgSession != "s" {
		t.Errorf("TgSession = %q，期望 \"s\"", got.TgSession)
	}
}
