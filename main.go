// Package main implements the WinDrop cloud relay.
//
// The relay is a dumb, near-stateless WebSocket fan-out server:
//   - Clients connect with wss://host/ws?deviceId=&groupId=&joinKey=&name=
//   - groupId / joinKey are derived client-side from the human sync code
//     (HKDF), the relay never sees the code nor any plaintext clipboard.
//   - Messages are end-to-end encrypted envelopes; the relay only fans them
//     out to every other online connection in the same group.
//   - Offline devices simply miss messages (clipboard is state, not a queue;
//     clients re-sync with requestClipboard after reconnect).
package main

import (
	"log"
	"net/http"
	"os"
	"time"
)

func main() {
	listenAddr := envOr("LISTEN_ADDR", ":8790")
	storePath := envOr("STORE_PATH", "./relay-store.json")

	store, err := NewStore(storePath)
	if err != nil {
		log.Fatalf("load store: %v", err)
	}

	hub := NewHub(store)

	mux := http.NewServeMux()
	mux.HandleFunc("/ws", hub.HandleWebSocket)
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})

	server := &http.Server{
		Addr:              listenAddr,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}

	log.Printf("[relay] listening on %s, store=%s", listenAddr, storePath)
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatalf("serve: %v", err)
	}
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
