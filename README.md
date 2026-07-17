# Grok Gateway

本地 Grok Build 账号池网关：托管多个 Grok Build OAuth 账号，查询用量、自动刷新 Token，并在额度耗尽或冷却时自动切换账号。

提供 Windows 桌面控制台（WebView2）与无界面 `serve` 模式，**仅**转发 Grok Build 原生协议，不兼容 OpenAI / Anthropic / Grok Web 接口。

```text
Grok Build  ──>  127.0.0.1:8787/v1  ──>  账号池 / 故障迁移  ──>  本地代理  ──>  cli-chat-proxy.grok.com
桌面控制台  ──>  管理 API / OAuth / 用量 / 设置
```

## 功能

- 单文件 Windows GUI EXE，双击打开独立桌面窗口（WebView2）
- 控制台：概览、账号池、配置
- 界面内完成 Grok Build Device OAuth
- 多账号启用 / 停用 / 删除 / 首选账号
- 查询套餐、用量百分比、重置时间
- Access Token 到期前自动刷新
- 正常情况会话粘滞；`401` / `429` / 额度耗尽时自动换号
- JSON 与 SSE 流式响应透明转发
- 所有上游 xAI 请求固定走配置的本地出站代理
- 一键启动已接入网关的 Grok Build
- 强制监听 `127.0.0.1`，不暴露到局域网

## 准备条件

| 依赖 | 说明 |
|------|------|
| Windows 10/11 | 桌面 GUI 需要 WebView2（系统通常已预装） |
| [Grok Build](https://grok.com) | `grok --version` 有输出 |
| 本地 HTTP 代理 | 默认 `http://127.0.0.1:7890`（Clash / Mihomo 等 mixed-port） |
| 合法 Grok Build 账号 | 仅用于本人拥有的账号 |

> 出站代理为**硬性依赖**：OAuth、Token 刷新、Billing、推理请求都会走它。代理未启动时授权与上游调用会失败。

## 快速开始

### 1. 启动

双击：

```text
GrokGateway.exe
```

程序会在 EXE 同目录生成 `config.json`，监听 `http://127.0.0.1:8787`，并在内嵌 WebView2 窗口中打开管理界面（不启动外部浏览器或控制台窗口）。

### 2. 添加账号并启动 Grok Build

1. 打开「账号池」→「添加账号」
2. 在系统浏览器完成 xAI Device OAuth
3. 回到控制台，点击「刷新用量」
4. 点击右上角「启动 Grok Build」

启动按钮会在新终端中临时设置：

```text
GROK_CLI_CHAT_PROXY_BASE_URL=http://127.0.0.1:8787/v1
```

**不会**修改 `~/.grok/config.toml`。关闭该终端后，Grok Build 不再走本网关。

### 3. 手动接入 Grok Build（可选）

```powershell
$env:GROK_CLI_CHAT_PROXY_BASE_URL = "http://127.0.0.1:8787/v1"
grok
```

## 命令行

```powershell
.\GrokGateway.exe             # 桌面 GUI + 本地网关
.\GrokGateway.exe serve       # 仅网关，无界面
.\GrokGateway.exe login       # 终端 Device OAuth 添加账号
.\GrokGateway.exe accounts    # 列出账号
.\GrokGateway.exe version
.\GrokGateway.exe help
```

指定配置文件：

```powershell
.\GrokGateway.exe --config D:\private\grok-gateway.json
# 或
$env:GROK_GATEWAY_CONFIG = "D:\private\grok-gateway.json"
```

## 账号池行为

| 场景 | 行为 |
|------|------|
| 设为首选账号 | 清空进程内会话绑定，后续请求优先走该账号 |
| 有会话 ID | 稳定哈希到同一账号（粘滞） |
| 无会话 ID | 轮询可用账号 |
| 上游 `429` | 按 `Retry-After` 或默认冷却，换号重试 |
| 用量 100% | 冷却到上游返回的重置时间 |
| Token `401` | 强制刷新；失败则冷却并换号 |

手动「解除冷却」后账号重新加入可用池。

## 配置

界面可改：监听地址、上游地址、出站代理、默认冷却时间。

**网络相关配置保存后需重启 EXE 生效。**

默认 `config.json`：

```json
{
  "listen": "127.0.0.1:8787",
  "upstream_base_url": "https://cli-chat-proxy.grok.com/v1",
  "outbound_proxy": "http://127.0.0.1:7890",
  "cooldown": "5m",
  "accounts": []
}
```

参考模板：[`config.example.json`](config.example.json)。

## 安全

- `config.json` 含 access / refresh token，**禁止**上传、分享或提交 Git
- 管理 API 与推理网关仅绑定本机回环地址
- 管理 API **不**返回 OAuth Token
- 面向本人合法账号；请勿用于规避服务条款或对外提供服务

若本地 `dist/config.json` 或任意备份曾包含真实凭据，开源前请确认已排除，并视情况在 xAI 侧轮换 Token。

## 构建

需要 Go 1.25+。图标生成可选（Python 3 + Pillow）。

```powershell
go test ./...
go vet ./...
.\scripts\build.ps1 -Version 0.3.0
```

产物：

```text
dist\GrokGateway.exe
```

架构与路由说明见 [docs/DEVELOPMENT.md](docs/DEVELOPMENT.md)。

## 项目结构

```text
cmd/grok-gateway/     入口、桌面宿主、Windows 资源
internal/account/     OAuth、账号池、用量
internal/admin/       管理 API 与内嵌控制台
internal/config/      配置加载与持久化
internal/proxy/       Grok Build 原生 HTTP/SSE 代理
scripts/              Windows 构建与图标
docs/                 开发文档
```

## 第三方致谢

OAuth 常量与请求行为参考 [chenyme/grok2api](https://github.com/chenyme/grok2api)（MIT）。桌面宿主使用 [Wails](https://github.com/wailsapp/wails)（MIT）。详见 [THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md)。

## License

MIT — 见 [LICENSE](LICENSE)。
