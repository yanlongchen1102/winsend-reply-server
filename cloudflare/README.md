# 部署到 Cloudflare Workers（免服务器方案）

这是 relay 的 Cloudflare 版实现，与 Go 版**协议完全一致**（同一套 groupId/joinKey/信封格式），
客户端无需任何改动，只需把 relay 地址指向 Worker 域名。

- 架构：Worker 入口 + 每个配对组一个 Durable Object（`GroupRelay`）
- 使用 WebSocket Hibernation API：无消息时 DO 休眠，**不持续计时长费用**
- 剪贴板为 KB 级文本流量，个人/小团队使用基本零成本（以 Cloudflare 官网额度政策为准）

## 部署步骤（约 10 分钟）

```bash
# 1. 安装 wrangler（Cloudflare 官方 CLI）
npm install -g wrangler

# 2. 登录 Cloudflare 账号（会打开浏览器授权）
wrangler login

# 3. 部署
cd cloudflare
wrangler deploy
```

部署成功后 wrangler 会输出 Worker 地址，形如：

```
https://windrop-relay.<你的账号子域>.workers.dev
```

## 验证

```bash
# 健康检查
curl https://windrop-relay.<你的账号子域>.workers.dev/healthz   # -> ok

# 端到端冒烟（双模拟设备互发加密剪贴板）
cd ../../airclip
go run ./tools/cloudsmoke wss://windrop-relay.<你的账号子域>.workers.dev/ws
# 期望输出：SMOKE TEST PASSED
```

## 配置客户端

把两个客户端的默认 relay 地址改为 `wss://windrop-relay.<子域>.workers.dev/ws`：

- **PC**：`airclip/main.go` 的 `DefaultRelayAddr`（或 config.json 的 `cloudRelay` 字段，免编译）
- **iOS**：`windrop-ios/WinDrop/Managers/RelayManager.swift` 的 `defaultRelayAddr`

## 可选：绑定自定义域名（推荐）

`workers.dev` 子域在部分网络环境下可能被干扰，建议绑定自有域名：

1. 域名 DNS 托管到 Cloudflare（任意免费域名即可，只用于 relay）
2. 控制台 → Workers & Pages → `windrop-relay` → Settings → Domains & Routes → Add → Custom Domain
   - 例如 `relay.example.com`
3. 证书由 Cloudflare 自动签发，无需任何配置
4. 客户端 relay 地址改为 `wss://relay.example.com/ws`

## 常用运维命令

```bash
wrangler tail          # 实时查看 Worker 日志（连接/鉴权失败都在这里）
wrangler deployments   # 查看部署历史
wrangler rollback      # 回滚到上一版本
```

## 与 Go 自建版对比

| | Cloudflare 版（本目录） | Go 自建版（仓库根目录） |
|---|---|---|
| 服务器 | 无需 | 需要 VPS |
| 成本 | 免费额度内≈0 | VPS 月费 |
| 运维 | 几乎为零 | systemd + Caddy |
| 代码 | JS，~150 行 | Go，~300 行 |
| 可控性 | 依赖 Cloudflare | 完全自控 |

两者协议一致，可随时互换：改客户端 relay 地址即可切换，同步码/加密不受影响。
