package main

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

const (
	maxDevicesPerGroup = 10
	maxMessageSize     = 256 * 1024 // 256 KB，剪贴板文本绰绰有余
	writeWait          = 15 * time.Second
	pongWait           = 90 * time.Second
	sendQueueSize      = 64
)

// clientConn is one online device connection.
type clientConn struct {
	deviceID string
	conn     *websocket.Conn
	send     chan []byte
}

// Hub tracks online connections per group and fans messages out.
type Hub struct {
	store *Store

	mu     sync.RWMutex
	groups map[string]map[string]*clientConn // groupID -> deviceID -> conn

	upgrader websocket.Upgrader
}

func NewHub(store *Store) *Hub {
	return &Hub{
		store:  store,
		groups: map[string]map[string]*clientConn{},
		upgrader: websocket.Upgrader{
			CheckOrigin: func(r *http.Request) bool { return true },
		},
	}
}

// HandleWebSocket authenticates and upgrades a device connection.
//
// Query params:
//
//	deviceId - stable client device ID
//	groupId  - HKDF(sync code, "windrop-group-v1"), base64url
//	joinKey  - HKDF(sync code, "windrop-join-v1"), base64url
//	name     - human readable device name (optional)
func (h *Hub) HandleWebSocket(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	deviceID := q.Get("deviceId")
	groupID := q.Get("groupId")
	joinKey := q.Get("joinKey")
	name := q.Get("name")

	if deviceID == "" || groupID == "" || joinKey == "" {
		http.Error(w, "missing deviceId/groupId/joinKey", http.StatusBadRequest)
		return
	}

	joinKeyHash := sha256.Sum256([]byte(joinKey))
	joinKeyHashHex := hex.EncodeToString(joinKeyHash[:])

	group := h.store.GetOrCreateGroup(groupID, joinKeyHashHex)
	if subtle.ConstantTimeCompare([]byte(group.JoinKeySHA256), []byte(joinKeyHashHex)) != 1 {
		log.Printf("[hub] auth failed: group=%s device=%s remote=%s", groupID, deviceID, r.RemoteAddr)
		http.Error(w, "invalid join key", http.StatusForbidden)
		return
	}
	if !h.store.AddDevice(groupID, deviceID, name) {
		http.Error(w, "group is full", http.StatusForbidden)
		return
	}

	conn, err := h.upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("[hub] upgrade failed: %v", err)
		return
	}

	cc := &clientConn{deviceID: deviceID, conn: conn, send: make(chan []byte, sendQueueSize)}

	h.mu.Lock()
	if h.groups[groupID] == nil {
		h.groups[groupID] = map[string]*clientConn{}
	}
	// 同一 deviceId 重复连接：踢掉旧连接（设备重连场景）
	if old, ok := h.groups[groupID][deviceID]; ok {
		close(old.send)
		old.conn.Close()
	}
	h.groups[groupID][deviceID] = cc
	online := len(h.groups[groupID])
	h.mu.Unlock()

	log.Printf("[hub] online: group=%s device=%s name=%q online=%d remote=%s",
		groupID, deviceID, name, online, r.RemoteAddr)

	go h.writePump(cc)
	h.readPump(groupID, cc)
}

func (h *Hub) readPump(groupID string, cc *clientConn) {
	defer func() {
		h.mu.Lock()
		// 仅当当前登记的还是这个连接时才移除（避免误删重连后的新连接）
		if cur, ok := h.groups[groupID][cc.deviceID]; ok && cur == cc {
			delete(h.groups[groupID], cc.deviceID)
			if len(h.groups[groupID]) == 0 {
				delete(h.groups, groupID)
			}
		}
		h.mu.Unlock()
		cc.conn.Close()
		log.Printf("[hub] offline: group=%s device=%s", groupID, cc.deviceID)
	}()

	cc.conn.SetReadLimit(maxMessageSize)
	_ = cc.conn.SetReadDeadline(time.Now().Add(pongWait))
	// gorilla 默认自动回复 ping；这里只要在收到 pong 时续期读超时
	cc.conn.SetPongHandler(func(string) error {
		return cc.conn.SetReadDeadline(time.Now().Add(pongWait))
	})

	for {
		msgType, payload, err := cc.conn.ReadMessage()
		if err != nil {
			if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseAbnormalClosure) {
				log.Printf("[hub] read error: group=%s device=%s err=%v", groupID, cc.deviceID, err)
			}
			return
		}
		if msgType != websocket.TextMessage && msgType != websocket.BinaryMessage {
			continue
		}
		h.fanOut(groupID, cc.deviceID, payload)
	}
}

// fanOut delivers a raw message to every other online device in the group.
// Slow consumers are dropped rather than blocking the group — clipboard is
// state, the receiver will catch up with requestClipboard on reconnect.
func (h *Hub) fanOut(groupID, senderID string, payload []byte) {
	h.mu.RLock()
	defer h.mu.RUnlock()

	for deviceID, cc := range h.groups[groupID] {
		if deviceID == senderID {
			continue
		}
		select {
		case cc.send <- payload:
		default:
			log.Printf("[hub] drop: group=%s target=%s queue full", groupID, deviceID)
		}
	}
}

func (h *Hub) writePump(cc *clientConn) {
	for payload := range cc.send {
		_ = cc.conn.SetWriteDeadline(time.Now().Add(writeWait))
		if err := cc.conn.WriteMessage(websocket.TextMessage, payload); err != nil {
			cc.conn.Close()
			return
		}
	}
}
