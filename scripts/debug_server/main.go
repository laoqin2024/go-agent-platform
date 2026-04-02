package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"strings"
	"sync"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
)

// Incoming structures from Agent.
type batchRequest struct {
	Items []batchItem `json:"items"`
}

type batchItem struct {
	DataType string          `json:"dataType"`
	Payload  json.RawMessage `json:"payload"`
}

// jsonRawMessage is a tiny alias to avoid importing encoding/json in this file for type only.
type jsonRawMessage []byte

// Dashboard state pushed to front-end.
type dashboardState struct {
	HostMetrics       json.RawMessage `json:"host_metrics,omitempty"`
	HardwareDetails   json.RawMessage `json:"hardware_details,omitempty"`
	SoftwareInventory json.RawMessage `json:"software_inventory,omitempty"`
	ProcessSnapshot   json.RawMessage `json:"process_snapshot,omitempty"`
	SecuritySnapshot  json.RawMessage `json:"security_snapshot,omitempty"`
	ServiceSnapshot   json.RawMessage `json:"service_snapshot,omitempty"`
	NetworkConnections json.RawMessage `json:"network_connections,omitempty"`
	RawBatch          batchRequest    `json:"raw_batch"`
}

var (
	upgrader = websocket.Upgrader{
		ReadBufferSize:  1024,
		WriteBufferSize: 1024,
		CheckOrigin: func(r *http.Request) bool {
			return true
		},
	}

	stateMu sync.Mutex
	state   = dashboardState{}

	clientsMu sync.Mutex
	clients   = map[*websocket.Conn]struct{}{}
)

func main() {
	addr := flag.String("addr", "0.0.0.0:8080", "listen address, e.g. 0.0.0.0:8080")
	flag.Parse()

	r := gin.Default()

	// POST /ingest from Agent.
	r.POST("/ingest", handleIngest)

	// WebSocket for front-end.
	r.GET("/ws", handleWS)

	// Serve single-page UI.
	r.StaticFile("/", "./index.html")

	log.Printf("Debug server listening on http://%s", *addr)
	log.Printf("Open dashboard in browser: %s", fmt.Sprintf("http://<LAN-IP>:%s", portOnly(*addr)))
	if err := r.Run(*addr); err != nil {
		log.Fatalf("server exited: %v", err)
	}
}

func portOnly(addr string) string {
	// addr may be "host:port" or ":port"
	for i := len(addr) - 1; i >= 0; i-- {
		if addr[i] == ':' {
			return addr[i+1:]
		}
	}
	return addr
}

func handleIngest(c *gin.Context) {
	var br batchRequest
	if err := c.ShouldBindJSON(&br); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	// Update in-memory state.
	stateMu.Lock()
	defer stateMu.Unlock()

	var nHost, nHw, nProc, nSw, nSec, nSvc, nConn int
	for _, it := range br.Items {
		dt := strings.ToLower(strings.TrimSpace(it.DataType))
		switch dt {
		case "host_metrics":
			state.HostMetrics = it.Payload
			nHost++
		case "hardware_details":
			state.HardwareDetails = it.Payload
			nHw++
		case "process_snapshot":
			state.ProcessSnapshot = it.Payload
			nProc++
		case "software_inventory":
			state.SoftwareInventory = it.Payload
			nSw++
		case "security_snapshot":
			state.SecuritySnapshot = it.Payload
			nSec++
		case "service_snapshot":
			state.ServiceSnapshot = it.Payload
			nSvc++
		case "network_connections":
			state.NetworkConnections = it.Payload
			nConn++
		}
	}
	state.RawBatch = br
	log.Printf("ingest: items=%d host=%d hw=%d proc=%d svc=%d conn=%d sw=%d sec=%d", len(br.Items), nHost, nHw, nProc, nSvc, nConn, nSw, nSec)

	// Broadcast to all WebSocket clients.
	broadcastStateLocked()

	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}

func handleWS(c *gin.Context) {
	conn, err := upgrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		log.Printf("upgrade ws failed: %v", err)
		return
	}

	clientsMu.Lock()
	clients[conn] = struct{}{}
	clientsMu.Unlock()

	// 连接建立时立即推送一次当前状态。
	stateMu.Lock()
	sendStateToConnLocked(conn)
	stateMu.Unlock()

	go func() {
		defer func() {
			clientsMu.Lock()
			delete(clients, conn)
			clientsMu.Unlock()
			conn.Close()
		}()

		for {
			// 只读取以保持连接存活；实际不处理任何来自前端的消息。
			if _, _, err := conn.ReadMessage(); err != nil {
				break
			}
		}
	}()
}

func broadcastStateLocked() {
	clientsMu.Lock()
	defer clientsMu.Unlock()

	for conn := range clients {
		sendStateToConnLocked(conn)
	}
}

func sendStateToConnLocked(conn *websocket.Conn) {
	if err := conn.WriteJSON(state); err != nil {
		log.Printf("write ws failed: %v", err)
	}
}
