# TGPan —— 技术验证记录

> 记录项目落地前**实测**出来的关键事实，避免基于错误假设开发。

---

## 一、选型：Alist / OpenList / Teldrive 的 TG 支持情况

| 项目 | 版本 | Telegram 驱动 | 结论 |
|---|---|---|---|
| **Alist** | v3.41.0（实测） | ❌ 无（70 个驱动全查过） | **不能直连 TG** |
| **OpenList** | v4.2.6（实测） | ✅ 有 `Teldrive` 驱动（93 个驱动） | 可连接，但需另起 Teldrive 服务 |
| **Teldrive** | v1.8.3 | ✅ 自身就是完整 TG 网盘 | **最终采用** |

> 网上流传的「Alist 挂 TG 频道」是误传；真正能连 TG 的是 **Teldrive**。
> OpenList 只是把它做成了一个连接器（前端），本项目不需要，直接单镜像跑 Teldrive。

---

## 二、最终架构

```
┌──────────────────────────────────────────────────────────┐
│  单个 Docker 镜像 tgpan                                   │
│                                                           │
│  ┌──────────────────────────────┐   ┌──────────────────┐ │
│  │ Teldrive v1.8.3 (Go, 静态)   │◀──│ PostgreSQL 17    │ │
│  │  · 连 TG / 文件切片 / 302    │   │ + pgroonga 扩展  │ │
│  │  · Web UI (embed 进二进制)   │   │ :5432            │ │
│  │  · REST API + WebDAV         │   └──────────────────┘ │
│  │  · 【改造】频道扫描           │                        │
│  │ :8080                        │                        │
│  └──────────┬───────────────────┘                        │
└─────────────┼────────────────────────────────────────────┘
              │ MTProto
       Telegram 服务器
```

---

## 三、核心机制：Teldrive 是怎么存/取文件的

理解这一点，才能理解「频道扫描」为什么可行。

### 3.1 数据结构

```
teldrive.files 表（元数据账本）
┌──────────────────────────────────────────────────────────┐
│ id          text    文件唯一 ID（gen_random_uuid）        │
│ name        text    文件名                               │
│ type        text    file / folder                        │
│ size        bigint  原始大小（字节）                       │
│ channel_id  bigint  存在哪个 TG 频道（裸 ID，不带 -100）   │
│ parts       jsonb   [{"id": <消息ID>, "salt": "..."}]     │
│ encrypted   bool    是否加密                             │
│ parent_id   text    父文件夹 ID                           │
│ status      text    active / pending_deletion            │
└──────────────────────────────────────────────────────────┘
```

### 3.2 读取流程（播放时）

```
用户点播放
    ↓
查 files 表 → 拿 channel_id + parts[0].id
    ↓
tgc.GetMessages(ctx, api, ids, channel_id)   ← 注意 channel_id 是裸 ID
    ↓
TG 返回该消息的 document
    ↓
tgc.GetLocation() → document.AsInputDocumentFileLocation()
    ↓
upload.getFile 拉流（带 offset/limit → 支持拖进度条）
    ↓
返回给播放器
```

> **关键结论**：Teldrive 是「按账本取文件」。
> 频道里躺着的消息，只要没登记进 `files` 表，Teldrive 就完全不知道它存在。

### 3.3 写入流程（上传时）

```
用户上传 10GB 电影
    ↓
按 512KB 切片（uploader.WithPartSize(512*1024)）
    ↓
每片作为一条独立 TG 消息发进存储频道（带文件名标记）
    ↓
记录进 teldrive.uploads 表（part_id = 消息ID, part_no = 序号）
    ↓
全部传完 → 汇总写一行 teldrive.files（parts = 所有片的消息ID）
```

### 3.4 加密机制（本项目扫描时不使用）

Teldrive 可选加密（`tg.uploads.encryption-key`）：

- 文件头：魔数 `TELDRIVE\x00\x00`（8 字节）+ 24 字节 nonce
- 数据按 64KB 一块，`secretbox` 密封，每块头 16 字节
- 密钥：`scrypt(password, salt)` 派生（N=16384, r=8, p=1）

**扫描导入的文件一律 `encrypted = false`**，原因：

1. 频道里现成的视频是**原始文件**，没有 Teldrive 的加密头
2. 若标记为加密，解密器校验魔数会失败（`ErrorEncryptedBadMagic`）
3. 不加密时 `parts` 只需 `id`，Teldrive 直接读原始 document 流

---

## 四、本项目做的改造：频道扫描

### 4.1 需求

把 TG 频道里**已有的**视频（包括别人发的），直接变成网盘里能播的文件。
Teldrive 原生不支持 —— 它只认自己上传时登记的文件。

### 4.2 实现

新增 `pkg/services/channelscan.go`：

```
1. 用登录的 TG 账号解析频道（tgc.GetChannelById）
2. 分页翻历史消息（messages.getHistory，每页 100 条，用 OffsetID 翻页）
3. 逐条判断：
   - MessageMediaDocument + DocumentAttributeVideo → 是视频
   - 大小 >= MinSize（默认 1MB，过滤封面小图）
4. 组装 files 记录：
   - channel_id = 频道裸 ID
   - parts = [{"id": <消息ID>}]
   - encrypted = false
   - size = document.Size
5. 建文件夹（用频道标题命名），文件挂进去
6. 批量写入（CreateInBatches，每批 200）
```

### 4.3 去重

扫描前先查该频道下已登记的所有 `parts[].id`，已存在则跳过。
**重复扫描同一频道不会重复导入。**

### 4.4 限流保护

- 每页之间 sleep 300ms
- 遇到 `FLOOD_WAIT` 自动等待（<=120s 自动重试，超过则报错）
- 默认 limit 2000 条消息
- 沿用 Teldrive 的 `rate-limit = true`

---

## 五、API

### 自定义接口

```
POST /api/scan/channel
Cookie: teldrive=<jwt>

{
  "channelId": -1001234567890,   // 必填，支持 -100 前缀或裸 ID
  "folderName": "我的电影",       // 选填，默认用频道标题
  "parentId": "",                // 选填，挂到哪个目录
  "limit": 2000,                 // 选填，最多扫多少条消息
  "minSize": 1048576             // 选填，最小文件字节数
}
```

响应：

```json
{
  "channelId": 1234567890,
  "channelName": "某某电影频道",
  "folderId": "uuid...",
  "folderName": "某某电影频道",
  "scanned": 1523,
  "imported": 87,
  "skipped": 1436,
  "totalSize": 214748364800,
  "message": "扫描了 1523 条消息，新导入 87 个视频，共 200.00 GB"
}
```

### ⚠️ 路由注册的坑（实测踩到）

`cmd/run.go` 里是：

```go
mux.Mount("/api/", http.StripPrefix("/api", extendedSrv))
```

**`/api` 前缀被 StripPrefix 剥掉了**，所以进到 `extendedMiddleware.ServeHTTP` 时
`r.URL.Path` 是 `/scan/channel`，**不是** `/api/scan/channel`。

第一次按 `/api/scan/channel` 判断 → 直接 404。修正为两种都兼容。

> 不走 ogen 路由表的原因：改 openapi 规范要重新跑 ogen 生成代码，
> 而在 `extendedMiddleware` 里前置拦截更轻量（`AuthWs`、`FilesStream` 也是这么做的）。

---

## 六、前端改造

> **本节已随 v2.6.3 重写。** 早期方案是「保留官方 UI + 注入补丁 JS」
> （`ui/tgpan-skin.js` / `ui/tgpan-scan.js` / `ui/tgpan-login.js`），
> 该方案下的选择器会污染原版界面，且官方 UI 升级即失效。
> **现已整体换成自制前端**，那三个补丁脚本已删除。

当前结构 —— 三个文件，都是第一方代码：

| 文件 | 职责 |
|---|---|
| `ui/index.html` | 骨架，按顺序引 `app.css` / `tgpan-gate.js` / `app.js` |
| `ui/app.css` | 全部样式 |
| `ui/app.js` | 主界面：我的网盘 + 系统设置（六个面板） |
| `ui/tgpan-gate.js` | 闸门：设置管理密码 / 登录 / 遮挡层（必须最先跑） |

设置页的六个面板：`drive`（我的网盘）、`scan`（频道扫描）、`auto`（自动扫描）、
`webdav`、`series`（剧集归档）、`about`。

> ⚠️ UI 是 **embed 进 Go 二进制的**（`//go:embed all:dist`），
> 改了前端后必须**重新编译二进制**，光重打镜像没用。
> 源码侧对应 `ui/dist/`（编译副本里），`ui/ui.go` 负责 embed。

---

## 七、构建要点（踩过的坑）

### 7.1 必须静态编译（否则容器起不来）

```bash
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags "-s -w" -o teldrive .
```

**原因**：运行镜像是 Alpine（musl libc）。若动态链接 glibc 编译，
容器里会报：

```
exec /usr/local/bin/teldrive: no such file or directory
```

（文件明明存在 —— 实际是找不到动态链接器 `/lib64/ld-linux-x86-64.so.2`）

验证：`ldd ./teldrive` 应输出 `not a dynamic executable`。

### 7.2 编译依赖的生成物

| 生成物 | 来源 | 命令 |
|---|---|---|
| `internal/api/` | ogen 从 openapi 规范生成 | `go run github.com/ogen-go/ogen/cmd/ogen --clean --package api --target internal/api openapi.json` |

两者都**已固化进本项目**（`vendor/` 里的二进制是编译好的完整产物），
正常构建**不需要联网**。

> 抓取时若 GitHub 直连慢，可用 `https://ghfast.top/` 前缀代理。

### 7.3 多架构

| 架构 | 用途 |
|---|---|
| `amd64` | x86 服务器、飞牛 OS（x86 版） |
| `arm64` | **甲骨文 ARM 免费机**、树莓派 |

### 7.4 pgroonga 硬依赖

Teldrive 的搜索用了 pgroonga 的 `&@~` 操作符和 `teldrive.clean_name()` 函数。
普通 postgres 镜像会报 `CREATE EXTENSION "pgroonga" is not available`。

解法：基础镜像用 `groonga/pgroonga:latest-alpine-17`。

### 7.5 登录卡死：AuthWs 的设计缺陷（v1.2.0 修复）

**这是本项目排查耗时最久的一个问题，记录完整过程以免重蹈覆辙。**

#### 现象

用户手机号登录后，界面永远停在 `Please Wait...`，验证码收不到；
后端日志几乎空白。大号小号都一样，偶发第一次能成功。

#### 排查过程中的两次误判

**误判一：以为是 TG 风控/限流**

用户反驳："我的甲骨文刚刚连接 TG，怎么就风控、限流、拉黑了？完全说不过去。"
—— 判断成立，此路排除。

**误判二：以为是 StripPrefix 导致路由失配（v1.1.0 的错误改动）**

观察 `cmd/run.go`：

```go
mux.Mount("/api/", http.StripPrefix("/api", extendedSrv))
```

openapi 里 `servers.url = "{url}/api"`，于是推测 ogen 路由表注册的是
`/api/auth/ws`，而进中间件时 `r.URL.Path` 已被剥成 `/auth/ws`，
`FindRoute` 必然失配。

**这个推测是错的。** ogen 内部会自行处理 basePath。
按此推测"修复"（查路由前补 `/api` 前缀）后，实测发现：

```
新版：HTTP/1.1 101 Switching Protocols   有 Accept 头 = False   ← 握手残缺！
旧版：HTTP/1.1 101 Switching Protocols   有 Accept 头 = True    ← 正常
```

**补前缀虽然让 `FindRoute` 命中了，但破坏了 ogen 内部的参数解析，
导致 101 响应缺少 `Sec-WebSocket-Accept` 头（假升级，浏览器会拒绝）。**

→ v1.2.0 已回滚该改动。

#### 真正的原因

看 `pkg/services/auth.go` 的 `AuthWs`：

```go
err = tgClient.Run(ctx, func(ctx context.Context) error {
    for {
        message := &types.SocketMessage{}
        err := conn.ReadJSON(message)   // ★ 消息读取在这里
        ...
    }
})
```

`tgClient.Run()` **会先做 MTProto 握手连接 Telegram 服务器，成功后才进入回调**。

所以当服务器连不上 Telegram 时：
1. `Run()` 阻塞在网络握手，**不进入回调**
2. `conn.ReadJSON()` 永远不会被调用
3. 前端发来的 `sendcode` 消息烂在 WebSocket 缓冲区
4. **既不处理，也不报错** —— 界面只能无限转圈
5. 后端日志空白（因为代码根本没执行到有日志的地方）

这解释得通所有现象：与账号无关、偶发成功（那次恰好连上了）、日志空白。

#### 修复（v1.2.0）

把「读消息」和「连 TG」解耦，并主动上报状态：

```go
var ready atomic.Bool

// 1. 读消息循环独立成 goroutine，不受 TG 连接状态影响
go func() {
    for {
        message := &types.SocketMessage{}
        if err := conn.ReadJSON(message); err != nil { ... }
        // TG 未就绪时立刻反馈，别让前端干等
        if !ready.Load() && message.AuthType != "" {
            _ = conn.WriteJSON(map[string]any{
                "type":    "error",
                "message": "尚未连接到 Telegram，请稍候重试（服务器正在建立连接）",
            })
            continue
        }
        switch message.AuthType { ... }
    }
}()

// 2. TG 连接在后台进行，成功/失败都明确推送
go func() {
    err := tgClient.Run(ctx, func(ctx context.Context) error {
        ready.Store(true)
        _ = conn.WriteJSON(map[string]any{
            "type": "status", "message": "已连接到 Telegram"})
        <-ctx.Done()
        return nil
    })
    if err != nil {
        _ = conn.WriteJSON(map[string]any{
            "type": "error", "message": "连接 Telegram 失败：" + err.Error()})
    }
}()
```

**实测验证**（本地容器，模拟 TG 连不上）：

```
→ 已发送 sendcode
← 反馈1: {"message":"尚未连接到 Telegram，请稍候重试（服务器正在建立连接）","type":"error"}
```

此前为**无限静默**，现在**立即返回明确原因**。

#### 经验

> 写异步/网络代码时，**任何可能长时间阻塞的操作，都必须有超时或状态反馈**。
> Teldrive 这个 `Run()` 包裹消息循环的写法，本质上把「连接可用性」
> 和「消息处理」耦合成串行，一旦连接慢或失败，用户侧完全无法感知。
>
> 另外：**改动前先跑一次对照测试**。如果 v1.1.0 发布前就做过
> "新旧版握手对比"，那个错误的补前缀改动当场就会被发现。

---

## 八、风险提示

### TG 官方限制

Telegram 官方文档明确警告：

> 不得滥用 API 大量上传/囤积文件，否则可能导致**账号被封**、**频道内容被清空**。

本项目已做限流保护，但仍建议：

- ✅ 用**小号**
- ✅ 扫描间隔别太短
- ❌ 不要一次扫几万条消息
- ❌ 不要用脚本疯狂刷

### 版权

扫描的是**别人发布的内容**。私有频道自用没问题，**不要公开分享、不要二次分发**。

---

## 九、参考

- [Teldrive](https://github.com/tgdrive/teldrive) —— 核心引擎（Go）
- [gotd/td](https://github.com/gotd/td) —— Go 版 Telegram MTProto 客户端库
- [pgroonga](https://pgroonga.github.io/) —— PostgreSQL 全文搜索扩展
