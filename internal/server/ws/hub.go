package ws

import (
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

type WSClient struct {
	Conn    *websocket.Conn
	writeMu sync.Mutex // Gorilla websocket: concurrent writes must be avoided.

	closeOnce sync.Once
}

func (c *WSClient) WriteMessage(messageType int, payload []byte) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	return c.Conn.WriteMessage(messageType, payload)
}

// Ping sends websocket Ping control frame with writeMu protection.
func (c *WSClient) Ping() error {
	deadline := time.Now().Add(10 * time.Second)
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	// Ping control frames should be short; use a small payload.
	return c.Conn.WriteControl(websocket.PingMessage, []byte("ping"), deadline)
}

func (c *WSClient) Close() error {
	var err error
	c.closeOnce.Do(func() {
		// Closing is concurrency-safe for gorilla websocket; we still avoid racing with writes via writeMu.
		c.writeMu.Lock()
		defer c.writeMu.Unlock()
		if c.Conn != nil {
			err = c.Conn.Close()
		}
	})
	return err
}

type deviceHub struct {
	deviceID string
	mu       sync.Mutex
	clients  map[*WSClient]struct{}
}

func newDeviceHub(deviceID string) *deviceHub {
	return &deviceHub{
		deviceID: deviceID,
		clients:  make(map[*WSClient]struct{}),
	}
}

func (h *deviceHub) add(c *WSClient) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.clients[c] = struct{}{}
}

func (h *deviceHub) remove(c *WSClient) {
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.clients, c)
}

func (h *deviceHub) broadcastTextMessage(messageType int, payload []byte) (sent int) {
	// Snapshot clients to avoid holding the lock while writing.
	h.mu.Lock()
	clients := make([]*WSClient, 0, len(h.clients))
	for c := range h.clients {
		clients = append(clients, c)
	}
	h.mu.Unlock()

	for _, c := range clients {
		err := c.WriteMessage(messageType, payload)
		if err != nil {
			h.remove(c)
			_ = c.Close()
			continue
		}
		sent++
	}
	return sent
}

// Hub holds all device hubs.
// sync.Map keeps operations fast for large device counts (e.g. 2000).
type Hub struct {
	hubs sync.Map // map[string]*deviceHub
}

func NewHub() *Hub {
	return &Hub{}
}

func (h *Hub) getOrCreate(deviceID string) *deviceHub {
	if deviceID == "" {
		return nil
	}
	if v, ok := h.hubs.Load(deviceID); ok {
		return v.(*deviceHub)
	}
	dh := newDeviceHub(deviceID)
	actual, _ := h.hubs.LoadOrStore(deviceID, dh)
	return actual.(*deviceHub)
}

func (h *Hub) AddWSClient(deviceID string, c *WSClient) {
	dh := h.getOrCreate(deviceID)
	if dh == nil {
		return
	}
	dh.add(c)
}

func (h *Hub) RemoveClient(deviceID string, c *WSClient) {
	if deviceID == "" {
		return
	}
	if v, ok := h.hubs.Load(deviceID); ok {
		v.(*deviceHub).remove(c)
	}
}

// BroadcastToDevice pushes payload to all WS subscribers of that device.
func (h *Hub) BroadcastToDevice(deviceID string, payload []byte) (int, error) {
	dh := h.getOrCreate(deviceID)
	if dh == nil {
		return 0, nil
	}
	n := dh.broadcastTextMessage(websocket.TextMessage, payload)
	return n, nil
}
