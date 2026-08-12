# WinDrop Cloud Relay

WinDrop / WinSend 的云端剪贴板中转服务。一个近无状态的 Go 单二进制 WebSocket fan-out 服务器。

## 设计要点

- **哑管道**：relay 不解析消息体。客户端之间传输的是 AES-256-GCM 端到端加密信封，密钥由同步码在客户端侧派生，relay 永远接触不到明文和密钥。
- **分组模型**：一个"同步码"对应一个配对组（groupId）。组内设备互收消息。
- **无离线队列**：剪贴板是状态不是消息。设备重连后主动发 `requestClipboard` 追平，错过的历史不补偿。
- **鉴权**：`groupId` + `joinKey` 均由同步码经 HKDF 派生；relay 只存 joinKey 的 SHA-256。知道同步码才能入组。

## 协议

连接：

```
wss://<host>/ws?deviceId=<稳定设备ID>&groupId=<HKDF派生>&joinKey=<HKDF派生>&name=<设备名>
```

- 组不存在 → 自动创建（第一个连接的设备即为创建者）
- 组已存在 → joinKey 的 SHA-256 必须匹配，否则 403
- 每组最多 10 台设备；同一 deviceId 重复连接会踢掉旧连接
- 收到任何文本帧 → 原样转发给组内其他在线连接；目标队列满则丢弃
- 心跳走 WebSocket 控制帧 ping/pong（gorilla 自动回 ping），90 秒无 pong 判定死亡

密钥派生（客户端侧，两端必须一致）：

```
code     = 规范化后的同步码（大写、去空格和连字符）
groupId  = base64url(HKDF-SHA256(ikm=code, salt="windrop-group-v1", info="", L=32))
joinKey  = base64url(HKDF-SHA256(ikm=code, salt="windrop-join-v1", info="", L=32))
encKey   = PBKDF2-HMAC-SHA256(password=code, salt="windrop-enc-v1:" + groupId, iter=600000, L=32)
```

信封格式（relay 不解析，仅客户端关心）：

```json
{
  "type": "secure",
  "deviceId": "<发送方设备ID>",
  "msgId": "<UUID>",
  "ts": 1700000000,
  "nonce": "<base64, 12字节>",
  "data": "<base64, AES-256-GCM(innerJSON)>"
}
```

innerJSON 目前只承载两种消息：`clipboard`（含 text）和 `requestClipboard`。

## 本地运行

```bash
go build -o windrop-relay .
LISTEN_ADDR=:8790 STORE_PATH=./relay-store.json ./windrop-relay
```

## 部署（Ubuntu/Debian VPS）

前置：一个域名，A 记录指向 VPS（例如 `relay.example.com`）。

```bash
# 1. 编译（本机交叉编译或 VPS 上编译）
GOOS=linux GOARCH=amd64 go build -ldflags="-s -w" -o windrop-relay .

# 2. 上传到 VPS 并安装
sudo useradd -r -s /usr/sbin/nologin windrop || true
sudo mkdir -p /opt/windrop-relay /var/lib/windrop-relay
sudo chown windrop:windrop /var/lib/windrop-relay
scp windrop-relay user@vps:/tmp/
sudo mv /tmp/windrop-relay /opt/windrop-relay/windrop-relay
sudo chmod +x /opt/windrop-relay/windrop-relay

# 3. systemd
sudo cp deploy/windrop-relay.service /etc/systemd/system/
sudo systemctl daemon-reload
sudo systemctl enable --now windrop-relay
sudo systemctl status windrop-relay

# 4. Caddy（自动 HTTPS）
sudo apt install -y caddy   # 参考 https://caddyserver.com/docs/install
sudo cp deploy/Caddyfile /etc/caddy/Caddyfile   # 先改成你的域名
sudo systemctl reload caddy

# 5. 防火墙：只开 80/443（relay 监听 127.0.0.1，不直接暴露）
sudo ufw allow 80,443/tcp
```

验证：

```bash
curl https://relay.example.com/healthz   # -> ok
journalctl -u windrop-relay -f           # 查看日志
```

## 客户端配置

- **PC (airclip)**：托盘菜单 → 云端同步 → 生成同步码并启用。默认 relay 地址在 `main.go` 的 `DefaultRelayAddr`，也可用 `config.json` 的 `cloudRelay` 覆盖。
- **iOS (windrop-ios)**：首页 → 云端同步卡片 → 输入 PC 上显示的同步码。默认地址在 `RelayManager.swift` 的 `defaultRelayAddr`。

## 安全边界与运维

- relay 被拖库：攻击者只能得到 groupId（不可逆派生值）、joinKey 哈希、设备 ID/名称，**拿不到同步码、密钥和任何剪贴板内容**。
- 撤销设备 / 换码：目前最简方式是两端都换一个新同步码重新配对（旧 groupId 自然废弃）。
- relay 挂了：客户端指数退避自动重连（1s → 30s），无数据丢失概念。
- 流量：纯文本剪贴板为 KB 级，1M 带宽入门 VPS 足够。
