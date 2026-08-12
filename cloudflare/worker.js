// WinDrop Cloud Relay — Cloudflare Workers + Durable Objects 版
//
// 与 Go 版（windrop-relay/hub.go）协议完全一致：
//   wss://<worker>/ws?deviceId=&groupId=&joinKey=&name=
//   - groupId/joinKey 由客户端从同步码 HKDF 派生，relay 只存 joinKey 的 SHA-256
//   - 消息为端到端加密信封，本服务只按组 fan-out，不解析、不存储消息
//   - 离线即丢弃：剪贴板是状态，客户端重连后 requestClipboard 追平
//
// 架构：每个 groupId 映射到一个 Durable Object（GroupRelay），组内连接由它持有。
// 使用 WebSocket Hibernation API：无消息时 DO 休眠，不计时长费用。

const MAX_DEVICES_PER_GROUP = 10;
const MAX_MESSAGE_SIZE = 256 * 1024; // 256KB

export default {
  async fetch(request, env) {
    const url = new URL(request.url);

    if (url.pathname === "/healthz") {
      return new Response("ok", { status: 200 });
    }

    if (url.pathname === "/ws") {
      const groupId = url.searchParams.get("groupId");
      if (!groupId) {
        return new Response("missing groupId", { status: 400 });
      }
      // 每个配对组一个 Durable Object 实例
      const id = env.GROUPS.idFromName(groupId);
      const stub = env.GROUPS.get(id);
      return stub.fetch(request);
    }

    return new Response("not found", { status: 404 });
  },
};

export class GroupRelay {
  constructor(ctx, env) {
    this.ctx = ctx;
    this.env = env;
  }

  async fetch(request) {
    const url = new URL(request.url);
    const deviceId = url.searchParams.get("deviceId");
    const joinKey = url.searchParams.get("joinKey");
    const name = url.searchParams.get("name") || "";

    if (!deviceId || !joinKey) {
      return new Response("missing deviceId/joinKey", { status: 400 });
    }

    // ---- 鉴权：joinKey 的 SHA-256 与组内记录比对（relay 从不接触同步码明文） ----
    const joinKeyHash = await sha256Hex(joinKey);

    let group = await this.ctx.storage.get("group");
    if (!group) {
      // 第一个连接的设备创建组
      group = { joinKeyHash, devices: {}, createdAt: new Date().toISOString() };
    }
    if (group.joinKeyHash !== joinKeyHash) {
      console.log(`auth failed: device=${deviceId}`);
      return new Response("invalid join key", { status: 403 });
    }
    if (!group.devices[deviceId]) {
      if (Object.keys(group.devices).length >= MAX_DEVICES_PER_GROUP) {
        return new Response("group is full", { status: 403 });
      }
      group.devices[deviceId] = { name, joinedAt: new Date().toISOString() };
    } else if (name) {
      group.devices[deviceId].name = name;
    }
    await this.ctx.storage.put("group", group);

    // ---- WebSocket 升级 ----
    if (request.headers.get("Upgrade") !== "websocket") {
      return new Response("expected websocket", { status: 426 });
    }

    const pair = new WebSocketPair();
    const server = pair[1];

    // 同一 deviceId 重复连接：踢掉旧连接（设备重连场景）。
    // tag 按 deviceId 标记，Hibernation 休眠唤醒后仍可检索。
    for (const old of this.ctx.getWebSockets(deviceId)) {
      try { old.close(1000, "replaced by new connection"); } catch (_) {}
    }

    // Hibernation API：无消息时 DO 可休眠，WebSocket 由运行时保持
    this.ctx.acceptWebSocket(server, [deviceId]);
    server.serializeAttachment({ deviceId });

    console.log(`online: device=${deviceId} name=${name} online=${this.ctx.getWebSockets().length}`);

    return new Response(null, { status: 101, webSocket: pair[0] });
  }

  // 收到消息：fan-out 给组内除发送者外的所有在线连接
  async webSocketMessage(ws, message) {
    if (typeof message !== "string" || message.length > MAX_MESSAGE_SIZE) {
      return;
    }
    const sender = ws.deserializeAttachment();
    const senderId = sender && sender.deviceId;

    for (const peer of this.ctx.getWebSockets()) {
      const meta = peer.deserializeAttachment();
      if (meta && meta.deviceId === senderId) continue;
      try {
        peer.send(message);
      } catch (err) {
        // 连接失效由运行时回收，忽略单点发送失败
        console.log(`fan-out send failed: ${err}`);
      }
    }
  }

  async webSocketClose(ws, code, reason) {
    const meta = ws.deserializeAttachment();
    console.log(`offline: device=${meta && meta.deviceId} code=${code}`);
    try { ws.close(code, reason); } catch (_) {}
  }

  async webSocketError(ws, error) {
    console.log(`ws error: ${error}`);
    try { ws.close(1011, "error"); } catch (_) {}
  }
}

async function sha256Hex(text) {
  const digest = await crypto.subtle.digest("SHA-256", new TextEncoder().encode(text));
  return Array.from(new Uint8Array(digest))
    .map((b) => b.toString(16).padStart(2, "0"))
    .join("");
}
