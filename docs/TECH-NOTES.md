# TGPan —— 技术验证记录

> 这份文档记录项目落地前**实测**出来的关键事实，避免基于错误假设开发。

## 一、核心发现：Alist / OpenList 的 TG 支持情况

| 项目 | 版本 | Telegram 驱动 | 结论 |
|---|---|---|---|
| **Alist** | v3.41.0（实测） | ❌ 无（70 个驱动全查过） | **不能直连 TG** |
| **OpenList** | v4.2.6（实测） | ✅ **有 `Teldrive` 驱动**（93 个驱动） | **可以，方案成立** |

> 网上流传的"Alist 挂 TG 频道"是误传；真正能连 TG 的是 **Teldrive**，OpenList 把它做成了原生驱动。

## 二、最终架构（实测确认）

```
┌──────────────────────────────────────────────────────┐
│  单个 Docker 镜像 tgpan                            │
│                                                       │
│  ┌─────────────┐   ┌──────────────┐   ┌───────────┐  │
│  │ Teldrive    │◀──│ OpenList      │◀──│ 前端       │  │
│  │ (Go)        │原生│ (Teldrive     │   │ 百度网盘风 │  │
│  │ 连TG/切片   │驱动│  驱动)        │   │ +设置页    │  │
│  │ 302 直链    │   │ WebDAV/文件列表│   │           │  │
│  └──────┬──────┘   └──────────────┘   └───────────┘  │
│         │                                             │
│  ┌──────▼──────┐                                      │
│  │ PostgreSQL  │  ← 必须带 pgroonga 扩展              │
│  │ (定制品)     │                                      │
│  └─────────────┘                                      │
└──────────┬───────────────────────────────────────────┘
           │ MTProto
     Telegram 服务器
```

## 三、Teldrive 真实技术参数（读源码确认）

### 3.1 版本与来源
- 仓库：`github.com/tgdrive/teldrive`，**3095 star**，Go 语言，未归档
- 实测版本：**v1.8.3**
- 官方镜像：`ghcr.io/tgdrive/teldrive`
- 官方数据库镜像：**`ghcr.io/tgdrive/postgres:17-alpine`**（定制版，含 pgroonga）

### 3.2 API 结构（源码 `pkg/services/api.go` + 各 service 确认）

API 统一挂在 **`/api/`** 前缀下，认证用 **JWT Cookie**。

**认证（`pkg/services/auth.go`）**
| 操作 | 说明 |
|---|---|
| `AuthLogin` | 登录，返回 JWT |
| `AuthLogout` | 登出 |
| `AuthSession` | 查询会话 |
| `AuthWs` | **WebSocket 登录**（用 TG session 走 WS 拿 token） |

**文件（`pkg/services/file.go`）**
| 操作 | 说明 |
|---|---|
| `FilesList` | 列目录 |
| `FilesCreate` | 新建 |
| `FilesMkdir` | 建文件夹 |
| `FilesMove` | 移动 |
| `FilesCopy` | 复制 |
| `FilesDelete` | 删除 |
| `FilesGetById` | 按 ID 取文件 |
| `FilesUpdate` | 重命名/更新 |
| `FilesCategoryStats` | 分类统计（图片/视频/文档…） |
| **`FilesStream`** | **文件流式读取 —— 播放就靠它** |
| `FilesCreateShare` | 创建分享 |
| `SharesStream` | 分享文件流 |

**上传（`pkg/services/upload.go`）**
| 操作 | 说明 |
|---|---|
| `UploadsUpload` | 上传（分片） |
| `UploadsPartsById` | 分片上传 |
| `UploadsStats` | 上传统计 |
| `UploadsDelete` | 取消上传 |

**用户/频道（`pkg/services/user.go`）**
| 操作 | 说明 |
|---|---|
| `UsersListChannels` | **列出频道 —— 前端"频道文件夹"用这个** |
| `UsersCreateChannel` | 创建频道 |
| `UsersUpdateChannel` | 更新频道 |
| `UsersDeleteChannel` | 删除频道 |
| `UsersSyncChannels` | 同步频道 |
| `UsersListSessions` | 列出登录会话 |
| `UsersStats` | 用户统计（容量等） |
| `UsersAddBots` / `UsersRemoveBots` | Bot 管理 |
| `UsersProfileImage` | 头像 |

### 3.3 配置文件（`config.sample.toml` 实测）

关键字段：
```toml
[db]
data-source = 'postgres://user:pass@host:5432/db?sslmode=disable'

[jwt]
secret = '随机字符串'
session-time = '30d'

[server]
port = 8080

[tg]
app-id = <从 my.telegram.org 申请>
app-hash = '<32位hash>'
auto-channel-create = true        # 自动建频道
channel-limit = 500000            # 频道容量上限
rate-limit = true                 # 限流保护（防封）
pool-size = 8

[tg.session]
type = 'postgres'                 # session 存数据库，重启不丢
key = 'session'

[tg.stream]
buffers = 8
concurrency = 1

[tg.uploads]
# 上传并发等
```

### 3.4 数据库硬依赖

⚠️ **Teldrive 强依赖 PostgreSQL 的 `pgroonga` 全文搜索扩展**：
```
CREATE EXTENSION IF NOT EXISTS pgroonga;   -- 迁移脚本 20240711163538_search.sql
```
普通 `postgres:16-alpine` **不带此扩展**，迁移会失败。**必须用 `ghcr.io/tgdrive/postgres:17-alpine`**。

## 四、302 支持（解决"卡"的关键）

OpenList 的 Teldrive 驱动字段（实测）：
- `webdav_policy` 默认值 **`302_redirect`** ✅
- `use_share_link` — 创建分享链接以支持 302
- `chunk_size` 默认 10 MiB — 大文件自动分片，突破 TG 2GB 单文件限制
- `upload_concurrency` 默认 4

> 意味着：**播放器可以直连 TG 拿数据，不经过你的服务器转发**。这解决了之前方案里"数据必须过服务器带宽"的死结。

## 五、必须遵守的 Telegram 限制（官方 README 警告）

> "You will be banned instantly if you misuse telegram API."
> "Your files will be removed from telegram servers if you try to abuse the service."

- **禁止滥用**：大量并发请求、疯狂刷频道会**立即封号**
- **禁止数据囤积**（data hoarding）：违规会导致**频道被清空**
- 必须遵守 TG API 频率限制

**因此：`rate-limit = true` 必须保持开启。建议用小号。**

## 六、端口规划

| 端口 | 服务 |
|---|---|
| 8080 | 前端 UI（百度网盘界面） |
| 8085 | Teldrive API（内部） |
| 5244 | OpenList（内部，WebDAV 对外） |
| 5432 | PostgreSQL（内部） |

对外只暴露：**8080（UI）** 和 **5244（WebDAV）**。
