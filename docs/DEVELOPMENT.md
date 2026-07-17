# Grok Gateway 开发文档

## 1. 定位与边界

Grok Gateway 是单机 Grok Build OAuth 账号托管网关：

```text
桌面 WebView2 ──> 内嵌 AssetServer ──> OAuth / 用量 / 设置
Grok Build  ──> 127.0.0.1:8787/v1
                              │
                        账号池与故障迁移
                              │
                    HTTP Proxy 127.0.0.1:7890
                              │
             cli-chat-proxy.grok.com/v1
```

不在范围内：OpenAI Chat Completions 转换、Anthropic Messages 转换、Grok Web/Console、远程多租户、数据库、Redis、图片或视频代理。

## 2. 目录结构

```text
cmd/grok-gateway/main.go          程序入口与本地服务器启动
cmd/grok-gateway/desktop.go       Wails/WebView2 桌面窗口生命周期
cmd/grok-gateway/fatal_windows.go GUI 启动错误对话框
internal/config/config.go         JSON 配置、账号和用量持久化
internal/account/oauth.go         Device OAuth 与 Token 刷新
internal/account/pool.go          动态账号池、首选账号和会话路由
internal/account/usage.go         Billing 用量获取和解析
internal/proxy/proxy.go           Grok Build 原生 HTTP/SSE 代理 + 请求观测钩子
internal/observe/store.go         请求日志、Token 日聚合、模型缓存
internal/account/models.go        每账号 /models 拉取
internal/admin/handler.go         本地控制台管理 API
internal/admin/static/index.html  控制台结构
internal/admin/static/style.css   控制台视觉样式
internal/admin/static/app.js      控制台交互
scripts/build.ps1                 Windows 发布构建
scripts/generate_icon.py          Windows 图标生成
```

静态资源通过 `go:embed` 编译进 EXE。桌面宿主使用 Wails v2.13 和系统 WebView2 Runtime；无需启动外部浏览器，也不携带 Chromium。

## 3. 桌面宿主

默认启动路径使用 `wails.Run` 创建 1280×820 的独立 Windows GUI 窗口。Wails AssetServer 将窗口内的请求直接交给现有 `admin.Handler`，因此前端仍复用相同管理 API；同时另行监听 `127.0.0.1:8787`，供外部 Grok Build 调用 `/v1/*`。

窗口生命周期规则：

- 关闭窗口时优雅关闭本地 HTTP 网关。
- 本地监听失败时显示原生错误对话框并退出。
- `SingleInstanceLock` 防止重复实例；再次启动会恢复已有窗口。
- OAuth 链接通过 Wails `BrowserOpenURL` 打开系统浏览器，仅授权页面离开应用。
- 构建使用 `-H windowsgui`，运行时不产生控制台窗口。
- Windows 依赖系统 WebView2 Runtime，Windows 10/11 通常已预装。

## 4. HTTP 路由

### 管理界面

```text
GET /                       控制台
GET /assets/*               内嵌 CSS/JavaScript
GET /healthz                健康状态
```

### 管理 API

```text
GET  /api/state
POST /api/oauth/start
POST /api/oauth/poll
POST /api/accounts/preferred
POST /api/accounts/enabled
POST /api/accounts/delete
POST /api/accounts/usage
POST /api/accounts/usage-all
POST /api/accounts/models
POST /api/accounts/models-all
POST /api/accounts/api-key
POST /api/accounts/cooldown/clear
GET  /api/stats
GET  /api/stats/export
GET  /api/logs
POST /api/logs/clear
PUT  /api/settings
POST /api/settings/gateway-key
POST /api/grok/launch
```

管理 API 不序列化 access token 或 refresh token。

### Grok Build

所有 `/v1/*` 路径按原协议透传，包括但不限于：

```text
POST   /v1/responses
POST   /v1/responses/compact
GET    /v1/responses/{id}
DELETE /v1/responses/{id}
GET    /v1/models
GET    /v1/billing
```

## 5. 账号池

`Pool.Reload` 从配置重新加载账号，同时保留未删除账号的运行时冷却和并发状态。OAuth 完成、启停账号和删除账号后均会调用 Reload，无需重启。

选择顺序：

1. 仍然可用的已有会话绑定。
2. 可用的首选账号。
3. 使用会话键 FNV-1a 哈希选择。
4. 没有会话键时轮询。

会话键优先级：

1. `x-grok-session-id`
2. `x-grok-conv-id`
3. `prompt_cache_key`
4. `previous_response_id`

切换首选账号时清空进程内会话映射，使后续请求立即迁移。

## 6. 自动故障迁移

每个下游请求最多尝试当前账号池数量次：

- `401`：对同一账号强制刷新 Token 并重试。
- Token 刷新失败：冷却账号并选择其他账号。
- `429`：读取 `Retry-After`，冷却账号，移除会话绑定并选择其他账号。
- 全部账号不可用：返回本地 `429`。
- 普通网络错误和 `5xx`：不盲目跨账号重放。
- 一旦开始向下游写入响应：不再重试。

因此账号正常时保留会话一致性，账号额度耗尽时自动漂移。

## 7. 用量同步

UI 中的 `/usage` 数据通过以下上游接口获取：

```text
GET {upstream}/billing?format=credits
```

解析兼容字段：

- `monthlyLimit`、`used`
- `onDemandCap`、`onDemandUsed`
- `prepaidBalance`
- `creditUsagePercent`
- `currentPeriod.start/end`
- `billingPeriodStart/End`
- subscription/plan 名称

用量达到 100% 时优先冷却到 `currentPeriod.end`，其次 `billingPeriodEnd`，最后使用默认冷却时间。

## 8. OAuth

Device OAuth：

```text
POST https://auth.x.ai/oauth2/device/code
POST https://auth.x.ai/oauth2/token
```

UI 创建短期 OAuth 会话并按上游间隔轮询。授权完成后只将凭据写入 `config.json`，API 只返回账号名称。

每个 RuntimeAccount 有独立互斥锁，防止并发刷新同一 refresh token。

## 9. 出站网络

程序显式创建 `http.Transport`：

```go
transport.Proxy = http.ProxyURL(proxyURL)
```

Device OAuth、Token 刷新、Billing 和 Grok Build 推理均使用同一个 Transport。本地浏览器与 Grok Build 到网关的请求不经过出站代理。

## 10. UI

控制台不依赖 CDN 或外部字体，避免代理未启动时页面无法渲染。界面使用响应式 CSS，桌面为固定侧栏，窄屏自动折叠。

前端每 10 秒刷新 `/api/state`，但不会自动触发上游 Billing 请求；Billing 只由用户主动刷新。

## 11. 测试与构建

```powershell
gofmt -w cmd internal
go test ./...
go vet ./...
.\scripts\build.ps1 -Version 0.3.0
```

测试覆盖：

- 配置安全校验与持久化
- OAuth Token 解析和刷新
- 固定出站代理
- 会话粘滞和冷却漂移
- `429` 自动账号故障迁移
- Billing 用量解析
- 管理页面加载
- 管理 API 不泄露 Token
- 配置 API 保存

`go test -race` 在 Windows 上需要安装 GCC；无 GCC 环境可执行普通测试和 `go vet`。

发布构建使用：

```text
-tags production
-ldflags "-H windowsgui -s -w"
CGO_ENABLED=0
```

`cmd/grok-gateway/rsrc_windows_amd64.syso` 将 `build/windows/icon.ico` 嵌入 EXE。修改图标时运行：

```powershell
python .\scripts\generate_icon.py
go run github.com/akavel/rsrc@v0.10.2 -ico .\build\windows\icon.ico -o .\cmd\grok-gateway\rsrc_windows_amd64.syso
```
