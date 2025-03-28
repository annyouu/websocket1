package main

import (
	"log"
	"net/http"
	"time"

	"github.com/gorilla/websocket"
)

// WebSocketの設定
var upgrader = websocket.Upgrader{
	ReadBufferSize: 1024,
	WriteBufferSize: 1024,
	// クロスオリジンを許可する(本番では制限する)
	CheckOrigin: func(r *http.Request) bool {
		return true
	},
}

// 各接続ユーザーを表す
type Client struct {
	hub *Hub
	conn *websocket.Conn
	//　送信用チャネル
	send chan []byte
}

// Hubは全クライアントの接続を管理し、ブロードキャストを行う
type Hub struct {
	// 接続中のクライアント
	clients map[*Client]bool

	// クライアントからのメッセージを受け取るチャネル
	broadcast chan []byte

	// 新規接続登録用チャネル
	register chan *Client

	// 切断登録用チャネル
	unregister chan *Client
}

// コンストラクタでHubの初期化を行う
func newHub() *Hub {
	return &Hub{
		clients: make(map[*Client]bool),
		broadcast: make(chan []byte),
		register: make(chan *Client),
		unregister: make(chan *Client),
	}
}

// hubに対する操作
func (h *Hub) run() {
	for {
		select {
		case client := <-h.register:
			h.clients[client] = true
			log.Println("新しいクライアントが作成されました")
		case client := <-h.unregister:
			if _, ok := h.clients[client]; ok {
				delete(h.clients, client)
				close(client.send)
				log.Println("クライアントが切断されました")
			}
		case message := <-h.broadcast:
			// 全てクライアントにメッセージを送信
			for client := range h.clients {
				select {
				case client.send <- message:
				default:
					// 送信バッファ(client.send)がいっぱいの場合はクライアントを閉じる
					close(client.send)
					delete(h.clients, client)
				}
			}
		}
	}
}

// クライアントからのメッセージ受信を処理する
func (c *Client) readPump() {
	defer func() {
		c.hub.unregister <- c
		c.conn.Close()
	}()
	// 読み込みの制限とタイムアウト設定
	c.conn.SetReadLimit(512)
	c.conn.SetReadDeadline(time.Now().Add(60 * time.Second))
	c.conn.SetPongHandler(func(string) error {
		c.conn.SetReadDeadline(time.Now().Add(60 * time.Second))
		return nil
	})
	for {
		// メッセージ受信(テキストメッセージ)
		_, message, err := c.conn.ReadMessage()
		if err != nil {
			if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseAbnormalClosure) {
				log.Printf("readPump エラー: %v", err)
			}
			break
		}
		// 受信したメッセージをhubのbroadcastに送る
		c.hub.broadcast <- message
	}
}

// クライアントへのメッセージ送信を処理する
func (c *Client) writePump() {
	ticker := time.NewTicker(54 * time.Second)
	defer func() {
		ticker.Stop()
		c.conn.Close()
	}()
	for {
		select {
		case message, ok := <-c.send:
			// 書き込みタイムアウト設定
			c.conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if !ok {
				// hubがチャネルをクローズした場合
				c.conn.WriteMessage(websocket.CloseMessage, []byte{})
				return
			}
			// 書き込み用のwriterを取得
			w, err := c.conn.NextWriter(websocket.TextMessage)
			if err != nil {
				return
			}
			w.Write(message)

			// バッファ内のメッセージもまとめて送信
			n := len(c.send)
			for i := 0; i < n; i++ {
				w.Write([]byte("\n"))
				w.Write(<-c.send)
			}

			if err := w.Close(); err != nil {
				return
			}
		case <-ticker.C:
			// 定期的にpingを送信して接続を維持
			c.conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if err := c.conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		}
	}
}

// HTTPリクエストをWebSocket接続にアップグレードし、新しいクライアントを登録する
func serveWs(hub *Hub, w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Println("upgradeエラー:", err)
		return
	}
	client := &Client{
		hub: hub,
		conn: conn,
		send: make(chan []byte, 256),
	}
	client.hub.register <- client

	// 読み書きをゴルーチンで処理
	go client.readPump()
	go client.writePump()

}


func main() {
	hub := newHub()
	go hub.run()

	http.HandleFunc("/ws", func(w http.ResponseWriter, r *http.Request) {
		serveWs(hub, w, r)
	})

	add := ":8080"
	log.Println("WebSocket server started on", add)
	if err := http.ListenAndServe(add, nil); err != nil {
		log.Fatal("ListenAndServe error:", err)
	}
}