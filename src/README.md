# 源码改造说明

本目录存放本项目对 **Teldrive v1.8.3** 的源码改造，供需要自行编译的人参考。

> ⚠️ 普通用户**不需要**看这里 —— 仓库 `vendor/` 里已含编译好的双架构二进制，
> `docker build` 直接用，无需编译。

---

## 改造了什么

在 Teldrive 上新增了「**频道扫描**」功能：
把 TG 频道里已有的视频消息，登记成 Teldrive 网盘文件（不复制、不占空间）。

### 涉及的文件

| 文件 | 改动 |
|---|---|
| `pkg/services/channelscan.go` | **新增**。扫描核心逻辑 + HTTP 入口 |
| `pkg/services/api.go` | 在 `extendedMiddleware.ServeHTTP` 里前置拦截 `/scan/channel` |
| `internal/auth/auth.go` | 新增导出函数 `WithUser(ctx, claims)` |
| `ui/tgpan-scan.js` | **新增**。前端悬浮按钮 + 弹窗 |
| `ui/index.html` | 加一行 `<script src="/tgpan-scan.js" defer>` |

本目录下：

- `channelscan.go` —— 直接复制到 `pkg/services/`
- `api.go` —— 供对照，看 `ServeHTTP` 开头那段路由拦截
- `auth.go` —— 供对照，看末尾的 `WithUser` 函数

---

## 如何自行编译

```bash
# 1. 克隆 Teldrive 源码
git clone https://github.com/tgdrive/teldrive.git
cd teldrive
git checkout d400a2d      # v1.8.3

# 2. 生成 API 代码（ogen）
curl -sL -o openapi.json https://raw.githubusercontent.com/tgdrive/teldrive-docs/main/openapi/openapi.json
go run github.com/ogen-go/ogen/cmd/ogen --clean --package api --target internal/api openapi.json

# 3. 解压前端到 ui/dist
curl -sL -o ui.zip "https://ghfast.top/https://github.com/tgdrive/teldrive-ui/releases/download/latest/teldrive-ui.zip"
mkdir -p ui/dist && unzip -q ui.zip -d ui/dist

# 4. 应用本项目的改造
cp <本项目>/src/channelscan.go pkg/services/channelscan.go
#   …按 api.go / auth.go 对照手动打补丁
cp <本项目>/ui/tgpan-scan.js ui/dist/tgpan-scan.js
#   …在 ui/dist/index.html 里加 <script src="/tgpan-scan.js" defer>

# 5. 静态编译（关键：CGO_ENABLED=0）
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags "-s -w" -o teldrive-amd64 .
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath -ldflags "-s -w" -o teldrive-arm64 .

# 6. 打包进本项目
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

---

## 新增接口

```
POST /api/scan/channel
Cookie: access_token=<jwt>        ← 注意 cookie 名是 access_token

{
  "channelId": 1234567890,        // 支持 -100 前缀或裸 ID
  "folderName": "我的电影",        // 选填
  "limit": 2000,                  // 选填
  "minSize": 1048576              // 选填
}
```

细节见 [`../docs/TECH-NOTES.md`](../docs/TECH-NOTES.md)。
