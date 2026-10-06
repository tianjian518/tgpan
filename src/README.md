# 源码改造说明

本目录存放本项目对 **Teldrive**（基于 v1.8.3，已升级到 v2.6.x 上游接口）的源码改造，
供需要自行编译的人参考。

> ⚠️ 普通用户**不需要**看这里 —— 仓库 `vendor/` 里已含编译好的双架构二进制，
> `docker build` 直接用，无需编译。

**当前改造版本：`2.7.0`**

---

## 改造了什么

在 Teldrive 上叠加了三块能力：

1. **频道扫描** —— 把 TG 频道里已有的视频消息，登记成 Teldrive 网盘文件（不复制、不占空间）。
2. **已关注频道自动列出** —— 扫描页直接列出你关注的全部频道，**不用自己去找频道 ID**。
3. **播放流畅度与写入性能调优** —— 滑动窗口预取、数据库连接级参数。

下面按文件说明。

### 一、频道扫描 / 自动追更

| 文件 | 改动 |
|---|---|
| `channelscan.go` | **新增**。扫描核心逻辑（增量游标 + FLOOD_WAIT 自动等待）+ HTTP 入口 |
| `channelscan_http.go` | **新增**。`/scan/channel`、`/scan/channels`、`/scan/status` 等接口 |
| `scan_scheduler.go` | **新增**。自动扫描调度器（2 分钟一轮、指数退避、串行执行） |
| `episode.go` / `episode_test.go` | **新增**。剧集识别（「第05集 / S01E05 / EP05」）与归档 |
| `series_http.go` | **新增**。剧集相关接口 |
| `api.go` | 在 `extendedMiddleware.ServeHTTP` 里前置拦截 `/scan/*`、`/series/*` 等路径 |
| `internal_auth.go`（对照 `internal/auth/auth.go`） | 新增导出函数 `WithUser(ctx, claims)` |

### 二、已关注频道（v2.7.0 新增，借鉴 `2517973339/tgdrive` 的设计）

> **核心体验**：扫描页不再让你手填频道 ID，而是把**你关注的频道直接列出来**，
> 挑一个点「扫描」即可。列表由后台定时器自动刷新。

| 文件 | 改动 |
|---|---|
| `dialogs.go` | **新增**。`MessagesGetDialogs` 分页拉取 + 落库 + 定时调度器（30 分钟一轮） |
| `dialogs_test.go` | **新增**。7 个单测，钉住翻页游标与 Peer 类型映射 |
| `models_webdav.go` | 新增 `TGDialog` 模型（表 `tg_dialogs`） |
| `20261005090000_tg_dialogs.sql` | **新增迁移**。建 `teldrive.tg_dialogs` 表 + 索引 + 外键 |
| `api.go` | 注册 `GET /scan/dialogs`；`StartScanSchedulerFor` 里顺带启动 `StartDialogScheduler` |

**实现要点（踩过的坑都在这）**：

- `tg.DialogClass` 是接口，有两个实现：`*tg.Dialog`（有 `TopMessage`）和
  `*tg.DialogFolder`（聊天文件夹，**没有 `TopMessage`**）。断言前必须区分，
  否则一旦某页页尾恰好是 `DialogFolder`，**分页会中断，后面所有频道都收不到**。
- `MessagesGetDialogs` 的 `offsetPeer` 要**无条件**跟着最后一条 dialog 更新，
  三种 Peer（`PeerChannel` / `PeerChat` / `PeerUser`）都要覆盖，
  各自构造对应的 `InputPeer`（Channel / User 需要 AccessHash，从返回的
  `Chats` / `Users` 数组里捞）。
- `offsetDate` 语义是**消息日期**，不是消息 ID。传 0 表示不限日期，
  纯按 `(peer, id)` 定位，语义更干净。

### 三、播放流畅度：滑动窗口预取（v2.7.0 新增）

| 文件 | 改动 |
|---|---|
| `internal_reader_tg_reader.go`（`internal/reader/tg_reader.go`） | `tgMultiReader` 改为**滑动窗口**取块 |
| `internal_reader_reader.go` | 清掉不再使用的 `concurrency` 字段 |
| `internal_reader_window_test.go` | **新增**。6 个单测（数据正确性） |
| `internal_reader_timing_test.go` | **新增**。5 个时序单测（首字节 / seek / 吞吐曲线） |
| `config.go` | 新增 `stream.prefetch-windows`（默认 **3**）、`stream.first-window-chunks` |

**为什么是"滑动窗口"而不是"加大并发"**：

同一部 3984MB 的 mp4，实测只改并发窗口数：

| 并发窗口 | 首字节 | 吞吐 |
|---|---|---|
| **1 路** | **0.97s** | 0.71 MB/s |
| 4 路 | 13.34s | 1.19 MB/s |
| 8 路 | 26.45s | 0.60 MB/s |

真相是：**并发拉高会把「首字节」拖垮**（起播慢十几倍），而吞吐并没有变好，
8 路甚至比串行还慢（TG 对单账号限流 + 连接争抢）。

所以正确做法是 **首窗口恒 1 个 chunk，第二轮之后才放大到 `prefetch-windows`**：

```go
func (r *tgMultiReader) windowConcurrency() int {
    if !r.firstWindowDone { return 1 }              // 首窗口：只发 1 个请求，保证秒起播
    if r.prefetchWindows < 1 { return 1 }
    if left := r.totalParts - r.currentPart; left < r.prefetchWindows {
        if left < 1 { return 1 }
        return left
    }
    return r.prefetchWindows                        // 后续窗口：放大到 prefetch
}
```

**验收单测**（`internal_reader/window_test.go`，全 PASS）：

| 单测 | 钉住什么 |
|---|---|
| `TestFirstWindowIsSingleRequest` | 首窗口必须只发 1 个请求 |
| `TestLaterWindowsWiden` | 后续窗口放大到 prefetch |
| `TestDataIntegrityAcrossWindows` | 跨窗口数据不错位（从中间偏移开始读） |
| `TestSinglePartCuts` | 单分片左右裁切 |
| `TestDegeneratePrefetch` | `prefetch=0/-1/-100` 不死循环 |
| `TestDegenerateFirstWindow` | `FirstWindowChunks=0/-5` 兜底为 1 |

> 数据完整性用 `patternChunkSource` 校验：每个字节 = 绝对偏移 % 251，
> 任何错位都会立刻暴露，而不是"看起来能播"。

**时序单测**（`internal_reader/timing_test.go`，全 PASS）：

用 `timedSource` 给每个 `Chunk()` 注入固定延迟（模拟 TG 单块 RTT），
测的是"延迟已经存在的前提下，并发策略怎么选最优"。

| 单测 | 钉住什么 |
|---|---|
| `TestFirstByteLatency` | 首字节 = 1 个 chunk 的 RTT，**与 prefetch 无关** |
| `TestSeekLatency` | seek 之后首字节同样 = 1 个 RTT，prefetch 帮不上忙 |
| `TestWindowBatchBlocking` | 一个窗口内 N 路并发只花 1 个 RTT（不是 N 个） |
| `TestThroughputVsPrefetch` | 吞吐随 prefetch 增长但**边际递减** |
| `TestPrefetchEnoughFor1080p` | prefetch=3 的吞吐已足够 1080p 码率 |

> **这两个数字决定了所有"加速"的天花板**：延迟由**单块 RTT** 决定，不由
> prefetch 决定；prefetch 只能提吞吐。所以"拉进度条要等十几秒"如果实测首字节
> 只有 0.8 秒，那多出来的时间**一定在网络链路，改代码无效**。

实测（delay = 600ms，单块 512KB）：

| prefetch | 吞吐 |
|---|---|
| 1 | 1.66 MB/s |
| 2 | 3.13 MB/s |
| 3 | 4.44 MB/s |
| 4 | 5.92 MB/s |
| 6 | 7.61 MB/s |
| 8 | 10.65 MB/s |

**所以默认 `prefetch-windows` 定 3**：4.44 MB/s 已经远超 1080p 蓝光原盘码率
（～1.7 MB/s），再往上拉吞吐，代价是并发请求数线性上涨 —— 而 TG 对单账号
有速率限制，并发拉高到 8 路以上会触发限流，实测吞吐反而回落、首字节也被拖慢。
**3 是"够用 + 留余量给限流"的平衡点。**

### 四、数据库连接级调优（v2.7.0 新增）
| 文件 | 改动 |
|---|---|
| `internal_database_tuning.go` | **新增**。构建调优参数并**写进 DSN** |
| `internal_database_tuning_test.go` | **新增**。8 个单测 |
| `internal_database_database.go` | DSN 拼接后调 `applyTuning`，`gorm.Open` 后调 `reportTuning` |
| `config.go` | 新增 `db.tune`、`db.tune-work-mem-mb` |

**关键点（容易踩）**：`SET` 是**会话级**的，只对执行它的那条连接生效 ——
池子里其它连接还是默认值。所以参数必须**塞进 DSN**，让每条新连接自动带上。

```toml
[db]
tune = true
tune-work-mem-mb = 16
```

| 参数 | 值 | 为什么 |
|---|---|---|
| `synchronous_commit` | `off` | 批量导入不用等 WAL fsync；崩溃最多丢最后几秒，且重扫幂等 |
| `work_mem` | `16MB` | 目录排序 / 分类聚合不落盘做外部排序 |
| `jit` | `off` | 短查询上 JIT 编译开销大于收益 |

启动后会**回读 `SHOW` 确认真的生效**（写错参数名 Postgres 不报错，只会静默不生效）。
日志长这样：

```
INFO [APP] db.tuning.applied  settings=[synchronous_commit=off work_mem=16MB jit=off] tune=true
INFO [APP] db.tuning.verify   synchronous_commit=off work_mem=16MB jit=off
```

### 五、WebDAV / 闸门 / 界面

| 文件 | 改动 |
|---|---|
| `webdav.go` / `webdav_test.go` | WebDAV 服务端；挂载探测头修复 |
| `gate.go` / `gate_http.go` / `gate_test.go` / `gate_wire.go` | 访问闸门（管理密码 + 内网免密） |
| `services_auth.go` / `internal_auth.go` / `gate_wire.go` | 认证与配对通道 |
| `run.go` | 路由装配、调度器启动 |
| `ui/app.js` / `ui/app.css` | **新增**。本项目自己的前端皮肤与交互 |
| `ui/index.html` / `ui/tgpan-gate.js` | 注入前端脚本 |

---

## 本目录（`src/`）的命名约定

这里是**扁平快照**，文件名用下划线拼接表示它在 Teldrive 仓库里的目录层级：

| 这里的名字 | 对应上游路径 |
|---|---|
| `channelscan.go` | `pkg/services/channelscan.go` |
| `dialogs.go` | `pkg/services/dialogs.go` |
| `file.go` / `authlog.go` / `diag.go` / `logincheck.go` | `pkg/services/` 下同名文件 |
| `models_webdav.go` | `pkg/models/webdav.go` |
| `internal_auth.go` | `internal/auth/auth.go` |
| `internal_reader_tg_reader.go` | `internal/reader/tg_reader.go` |
| `internal_database_tuning.go` | `internal/database/tuning.go` |
| `tgc.go` | `internal/tgc/tgc.go` |
| `20261005090000_tg_dialogs.sql` | `internal/database/migrations/20261005090000_tg_dialogs.sql` |

> **这里的每一个文件都是真实源码**，按上表映射回上游路径即可直接编译。
> 目录里**不含**任何仅供阅读的中间产物 —— 早先版本曾把 `file.go`、`authlog.go`
> 等文件错命名为 `*.go.txt`，导致使用者误以为它们不用替换、或拿到的是旧版。
> 已全部更正。

---

## 如何自行编译

```bash
# 1. 克隆 Teldrive 源码
git clone https://github.com/tgdrive/teldrive.git
cd teldrive
git checkout <与本文档匹配的 tag>      # 本项目基线为 v1.8.3，后续已适配上游 v2.6.x

# 2. 生成 API 代码（ogen）
curl -sL -o openapi.json https://raw.githubusercontent.com/tgdrive/teldrive-docs/main/openapi/openapi.json
go run github.com/ogen-go/ogen/cmd/ogen --clean --package api --target internal/api openapi.json

# 3. 铺前端到 ui/dist
#    ★ 本项目前端是**自制的**（ui/ 下四个文件），不依赖官方 UI 包。
#      不要再去下载 tgdrive/teldrive-ui —— 那是官方界面，会被下面的自制前端覆盖，
#      白下十几 MB。
mkdir -p ui/dist
cp <本项目>/ui/index.html      ui/dist/index.html
cp <本项目>/ui/app.css         ui/dist/app.css
cp <本项目>/ui/app.js          ui/dist/app.js
cp <本项目>/ui/tgpan-gate.js   ui/dist/tgpan-gate.js
cp <本项目>/ui/robots.txt      ui/dist/robots.txt
cp -r <本项目>/ui/fonts        ui/dist/fonts
cp -r <本项目>/ui/images       ui/dist/images

# 4. 把 src/ 的文件按上表还原到上游目录（全部都要替换，没有例外）
S=<本项目>/src
cp $S/api.go                      pkg/services/api.go
cp $S/services_auth.go            pkg/services/auth.go
cp $S/authlog.go                  pkg/services/authlog.go
cp $S/channelscan.go              pkg/services/channelscan.go
cp $S/channelscan_http.go         pkg/services/channelscan_http.go
cp $S/diag.go                     pkg/services/diag.go
cp $S/dialogs.go                  pkg/services/dialogs.go
cp $S/episode.go                  pkg/services/episode.go
cp $S/file.go                     pkg/services/file.go
cp $S/gate.go                     pkg/services/gate.go
cp $S/gate_http.go                pkg/services/gate_http.go
cp $S/gate_wire.go                pkg/services/gate_wire.go
cp $S/logincheck.go               pkg/services/logincheck.go
cp $S/scan_scheduler.go           pkg/services/scan_scheduler.go
cp $S/series_http.go              pkg/services/series_http.go
cp $S/webdav.go                   pkg/services/webdav.go
cp $S/models_webdav.go            pkg/models/webdav.go
cp $S/internal_auth.go            internal/auth/auth.go
cp $S/config.go                   internal/config/config.go
cp $S/tgc.go                      internal/tgc/tgc.go
cp $S/run.go                      cmd/run.go
cp $S/internal_database_database.go      internal/database/database.go
cp $S/internal_database_tuning.go        internal/database/tuning.go
cp $S/internal_reader_reader.go          internal/reader/reader.go
cp $S/internal_reader_tg_reader.go       internal/reader/tg_reader.go
for m in 20261004080000_webdav_credentials 20261004080100_channel_scans \
         20261005090000_tg_dialogs; do
  cp $S/$m.sql internal/database/migrations/$m.sql
done
# 测试文件（可选，跑单测用）
cp $S/gate_test.go                pkg/services/gate_test.go
cp $S/webdav_test.go              pkg/services/webdav_test.go
cp $S/dialogs_test.go             pkg/services/dialogs_test.go
cp $S/episode_test.go             pkg/services/episode_test.go
cp $S/security_test.go            pkg/services/security_test.go
cp $S/internal_database_tuning_test.go   internal/database/tuning_test.go
cp $S/internal_reader_window_test.go     internal/reader/window_test.go
cp $S/internal_reader_timing_test.go     internal/reader/timing_test.go

# 5. 静态编译（关键：CGO_ENABLED=0）
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags "-s -w" -o teldrive-amd64 .
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath -ldflags "-s -w" -o teldrive-arm64 .

# 6. 跑一遍本项目新增的单测（不需要数据库）
go test ./internal/reader/... ./internal/database/... ./pkg/services/...

# 7. 打包进本项目
mkdir -p pa pb && cp teldrive-amd64 pa/teldrive && cp teldrive-arm64 pb/teldrive
(cd pa && tar -czf ../vendor/teldrive-amd64.tar.gz teldrive)
(cd pb && tar -czf ../vendor/teldrive-arm64.tar.gz teldrive)
```

---

## 三个关键坑

### 1. 必须 `CGO_ENABLED=0`

运行镜像是 Alpine（musl libc）。动态链接 glibc 编译的二进制会报：

```
exec /usr/local/bin/teldrive: no such file or directory
```

验证：`ldd ./teldrive` 要显示 `not a dynamic executable`。

### 2. 路由前缀被剥掉（v1.1.0 修复）

`cmd/run.go`：

```go
mux.Mount("/api/", http.StripPrefix("/api", extendedSrv))
```

所以进到 `extendedMiddleware` 时 `r.URL.Path` 是 `/scan/channel`，**不是** `/api/scan/channel`。

**但这个前缀问题还有第二个副作用（v1.1.0 才修）**：

ogen 的路由表来自 `openapi.json`，其中：

```json
"servers": [{ "url": "{url}/api" }]
```

**路由表里注册的路径是 `/api/auth/ws` 这种带前缀的**。而 `FindRoute(r.Method, r.URL.Path)`
拿到的是被剥过的 `/auth/ws` → **永远匹配失败** → 请求落到 `m.next.ServeHTTP()`（ogen
默认实现，普通 HTTP handler，**不处理 WebSocket 升级**）。

表现：登录页点发送验证码后一直 `Please Wait...`，后端日志无任何记录；
偶发第一次能成功（旧连接残留），之后就全部失效。

修复：查路由前把 `/api` 补回去：

```go
lookupPath := r.URL.Path
if !strings.HasPrefix(lookupPath, "/api/") && lookupPath != "/api" {
    lookupPath = "/api" + lookupPath
}
route, ok := m.next.FindRoute(r.Method, lookupPath)
```

### 3. UI 是 embed 进二进制的

`ui/ui.go` 里 `//go:embed all:dist`。

**改了 `ui/dist/` 必须重新编译二进制**，只重打 Docker 镜像没用。

### 4. 中间件里写路由判断的**顺序**坑（v2.7.0 踩到）

`ServeHTTP` 里做前缀拦截时，`/scan/dialogs` 必须放在 `/scan/channels/` 前缀判断
**之前**，否则会被 `HasPrefix("/scan/channels/")` 吞掉（`parts[0]` 变成 `"dialogs"`）。

另外注意：**不带 `/api` 前缀的路径压根不进中间件**（只 `Mount("/api/")`），
会被 SPA 兜底吞掉并返回 `200` + HTML。这是**既有设计**，不是 bug ——
排查时别被 `curl /scan/dialogs` 返回 HTML 骗了，要用 `/api/scan/dialogs`。

---

## 新增接口

```
POST /api/scan/channel
Cookie: access_token=<jwt>        ← 注意 cookie 名是 access_token

{
  "channelId": 1234567890,        // 支持 -100 前缀或裸 ID
  "folderName": "我的电影",        // 选填
  "incremental": true,            // ★ 强烈建议 true：只拉上次之后的新消息
  "limit": 2000,                  // 选填
  "minSize": 1048576              // 选填
}
```

```
GET /api/scan/dialogs
Cookie: access_token=<jwt>

?groups=1     # 连群组一起列（默认只列广播频道）
?refresh=1    # 同步拉一次 TG 再返回（会慢几秒；前端只做手动刷新用）

→ { "dialogs": [{ "channelId", "title", "username", "isChannel",
                  "scanned", "imported", "fileCount" }],
    "total": 12, "includeGroups": false, "syncIntervalSec": 1800 }
```

> `rescan`（「立即扫一次」按钮）走的是 `RescanChannel`：
> 只对**已登记过**的频道做**增量**扫描。如果复用 `FilesScanChannel` 且不带
> `incremental`，每次点按钮都会把整个频道从头翻一遍。

细节见 [`../docs/TECH-NOTES.md`](../docs/TECH-NOTES.md)。

---

## 本轮（2.7.0）修掉的坑

全是自己审查代码时挖出来的，都补了回归测试。记在这里免得后人重踩。

### 1. 空列表覆盖写 = 静默删用户数据（最严重）

`syncDialogsForUser` 在 `client.Run` 之后**无条件**执行「先删后插」。但
`MessagesGetDialogs` 有个返回类型叫 `MessagesDialogsNotModified` —— TG 按 hash
判定「列表没变」时**一条都不返回**。于是 dialogs 是空的 → 先删生效、后插插了个空
→ **用户缓存的频道列表被清空**，而且整个流程"成功"，日志里没有任何错误。

同一个原因还有第二种：**分页没翻完**就报错（网络抖动、撞上 `dialogMaxPages` 上限）。
此时 dialogs 只有前半截，覆盖写等于把后半截频道删掉。

修法：用 `shouldOverwriteDialogs(notModified, complete)` 做闸门，
**只有「完整翻完且不是 NotModified」才允许覆盖写**。

> 关键区分：`notModified`（没拿到数据）**不能**清空，
> 而「完整翻完但确实为空」（用户全取关了）**应该**清空。
> 判断依据必须是这两个标记，**不能**是 `len(dialogs) == 0`。

回归测试：`TestShouldOverwrite_NotModified` / `_Incomplete` / `_Complete` / `_EmptyButComplete`。

### 2. HTTP handler 验了身份，接口却一直 401

`verifyCookieUser` 只**返回** claims，**不碰 context**。而 `apiService` 里几乎
所有方法都是 `userId := auth.GetUser(ctx)` —— 从 context 取用户。于是
「HTTP handler → apiService」这条链路永远拿到 `userId == 0`，方法第一行就 401。

受影响的功能（都曾是坏的）：
- `POST /api/scan/channels` —— 登记并立即扫描
- `POST /api/scan/channels/{id}/run` —— 「立即扫一次」按钮
- `GET /api/scan/dialogs` —— 「已关注频道」列表

表现极难排查：**接口明明验过身份了，却一直回 401，日志毫无线索**。

修法：新增 `verifyCookieUserCtx`，把 claims 注入 `r.WithContext(...)`。
走 `apiService` 的 handler 必须用它；只用 claims 的（自己 `ParseInt` 拿 userId）
继续用老的，保持既有调用点零风险。

回归测试：`TestVerifyCookieUserCtxInjectsUser`。

### 3. 设置页各面板「永远停在加载中，且一个请求都不发」

前端 `render()` 的顺序是：

```js
var main = el('main', { class: 'tp-main' });
if (state.tab === 'drive') renderDrive(main);
else renderSettings(main);        // ← 渲染时整棵树还没进 document
app.appendChild(main);            // ← 最后才挂载
```

而各面板的加载函数用 `document.getElementById('xxx')` **自己找容器** ——
执行到那里时 `document` 里根本没有这些元素，`getElementById` 必然返回 `null`，
函数第一行就 `if (!list) return;`。

**症状特别误导人**：页面停在「加载中…」，网络面板**一个请求都没有**，
控制台**一条错误都没有**。因为压根没发请求、也没抛异常。

受影响：扫描频道 / 自动扫描 / WebDAV 挂载 / 剧集归档 / 关于与诊断 —— **五个面板全中**。

修法：容器**直接用参数传进去**（`paneScan(host)` 里已经有元素引用了），
用 `(box || document).querySelector('#xxx')` 相对查找，不再依赖 `document`。

回归测试：端到端脚本第 9 节，逐个点击所有页签，断言
「发出了对应请求」+「没卡在加载态」。

### 4. 「刷新」按钮只是重读了缓存

前端点「刷新」调 `loadScanChannelList()`，**没带 `refresh=1`** —— 只是把后端
缓存表又读了一遍。用户以为去 TG 同步过了，其实没有。

修法：刷新按钮走 `loadScanChannelList(true, box)` → `GET /scan/dialogs?refresh=1`，
后端才真的去 TG 拉一次。刷新期间按钮禁用 + 文案变「正在同步…」，防狂点。

### 5. 调度器单轮没有时间预算

`syncOne` 是**串行阻塞**的，单用户最长可跑 `dialogSyncTimeout`（5 分钟）。
账号一多，一轮 tick 会远超 `dialogSyncTick`（10 分钟）→ ticker 事件堆积、
调度器一刻不停地跑、日志刷屏。

修法：加 `dialogTickBudget = 8 * time.Minute`，跑到预算就把剩下的用户留给下一轮。

回归测试：`TestDialogTickBudget`。

### 6. 冷启动时列表是空的

用户刚登录，`tg_dialogs` 是空的，而调度器要等 `60s 启动延迟 + 下一个 10 分钟 tick`
才跑 —— 用户打开扫描页看到空列表，会以为功能坏了。

修法：读路径上加 `fillDialogsIfEmpty` —— **只在列表为空时**顺手同步一次。
带按用户的互斥闸门（`tryAcquireFill` / `releaseFill`），防止并发请求同时拉 TG。
失败**不报错**（这是体验优化，不是功能路径）。

回归测试：`TestDialogFillGate`。

### 7. 闸门数据文件默认在 `/data`，和配置目录不一致

`gate.DataFile` 默认 `/data/tgpan-gate.json`，但本项目运行时配置在 `/config`。
两者不一致时，闸门密码会**静默丢失**（每次都当新部署，重新引导设密码）。

**部署时记得在配置里显式指定**：

```toml
[gate]
data-file = '/config/tgpan-gate.json'
```

