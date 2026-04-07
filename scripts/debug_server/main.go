package main

import (
	"compress/gzip"
	"crypto/sha256"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
)

// @title           2000台规模设备监控平台 API
// @version         1.0
// @description     高并发 Agent 采集与实时监控数据上报系统
// @contact.name    开发团队
// @host            localhost:8080
// @BasePath        /api/v1

// Incoming structures from Agent (Swagger models).
type ApiBatchRequest struct {
	Items []ApiBatchItem `json:"items"`
}

// ApiBatchItem is a Swagger-friendly view of an incoming batch item.
// Note: Payload is the real transport field; typed fields below are only for Swagger model discovery.
type ApiBatchItem struct {
	DataType string         `json:"dataType"`
	Payload  map[string]any `json:"payload,omitempty"`

	// Typed models for documentation only (optional in real payloads):
	SecuritySnapshot *ApiSecuritySnapshot `json:"security_snapshot,omitempty"`
}

// Runtime ingest structures (keep payload raw for forward compatibility).
type batchRequest struct {
	Items []batchItem `json:"items"`
}
type batchItem struct {
	DataType string          `json:"dataType"`
	Payload  json.RawMessage `json:"payload"`
}

// jsonRawMessage is a tiny alias to avoid importing encoding/json in this file for type only.
type jsonRawMessage []byte

// ---- Doc-only models for Swagger (mirror of internal collector types) ----
type ApiListeningPort struct {
	Protocol   string `json:"protocol,omitempty"`
	Address    string `json:"address,omitempty"`
	Port       uint32 `json:"port,omitempty"`
	Scope      string `json:"scope,omitempty"`        // public | local | other
	IsHighRisk bool   `json:"is_high_risk,omitempty"` // high-risk on public
}
type ApiSecuritySnapshot struct {
	Connections []map[string]any   `json:"connections,omitempty"` // compact placeholder
	Startup     []map[string]any   `json:"startup,omitempty"`
	Hotfixes    []map[string]any   `json:"hotfixes,omitempty"`
	Listening   []ApiListeningPort `json:"listening,omitempty"`
	CollectedAt string             `json:"collected_at,omitempty"`
}

// Dashboard state pushed to front-end.
type dashboardState struct {
	HostMetrics        json.RawMessage `json:"host_metrics,omitempty"`
	HardwareDetails    json.RawMessage `json:"hardware_details,omitempty"`
	SoftwareInventory  json.RawMessage `json:"software_inventory,omitempty"`
	ProcessSnapshot    json.RawMessage `json:"process_snapshot,omitempty"`
	SecuritySnapshot   json.RawMessage `json:"security_snapshot,omitempty"`
	ServiceSnapshot    json.RawMessage `json:"service_snapshot,omitempty"`
	NetworkConnections json.RawMessage `json:"network_connections,omitempty"`
	RawBatch           batchRequest    `json:"raw_batch"`
}

var (
	upgrader = websocket.Upgrader{
		ReadBufferSize:  1024,
		WriteBufferSize: 1024,
		CheckOrigin: func(r *http.Request) bool {
			return true
		},
	}
	// Explicit upgrader for global WS to guarantee permissive CORS for WS handshake
	globalUpgrader = websocket.Upgrader{
		ReadBufferSize:  1024,
		WriteBufferSize: 1024,
		CheckOrigin: func(r *http.Request) bool {
			return true // allow all origins for debug server
		},
	}

	stateMu sync.Mutex
	state   = dashboardState{}

	clientsMu sync.Mutex
	clients   = map[*websocket.Conn]struct{}{}

	// Minimal per-device isolation (debug-only, not Redis):
	lastHostHashByDevice = map[string]string{}
	fingerprintOwner     = map[string]string{} // fingerprint -> deviceID

	// Global status broadcasting
	globalClientsMu sync.Mutex
	globalClients   = map[*websocket.Conn]struct{}{}
	lastSeenMu      sync.Mutex
	lastSeenAt      = map[string]int64{} // deviceID -> unix seconds
)

func main() {
	addr := flag.String("addr", "0.0.0.0:8080", "listen address, e.g. 0.0.0.0:8080")
	flag.Parse()

	r := gin.Default()

	// POST /ingest from Agent.
	r.POST("/ingest", handleIngest)

	// WebSocket for front-end.
	r.GET("/ws", handleWS)
	// Global status WebSocket channel
	r.GET("/ws-global", handleWSGlobal)
	// Also expose a simpler alias to avoid any proxy cache/path issues
	r.GET("/global_status", handleWSGlobal)

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

// ReportMetrics 接收 Agent 上报的批量指标数据
// @Summary      上报设备指标
// @Description  接收经过 Gzip 压缩的 Batch 指标包，包含进程、服务、网络连接与安全快照（当在公开地址上暴露高危端口 22/3389/445/3306 时，listening 项将返回 is_high_risk=true）
// @Accept       json
// @Produce      json
// @Param        Content-Encoding  header  string  true  "必须为 gzip"
// @Param        payload           body    ApiBatchRequest  true  "指标数据包（包含 security_snapshot.listening 的 is_high_risk 标志）"
// @Success      200  {object}  map[string]string "{"status":"ok"}"
// @Failure      400  {object}  map[string]string "{"error":"invalid JSON"}"
// @Failure      413  {object}  map[string]string "{"error":"payload too large"}"
// @Router       /ingest [post]
func handleIngest(c *gin.Context) {
	var br batchRequest
	// Support Content-Encoding: gzip
	var dec *json.Decoder
	if strings.Contains(strings.ToLower(c.Request.Header.Get("Content-Encoding")), "gzip") {
		gr, err := gzip.NewReader(c.Request.Body)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid gzip body"})
			return
		}
		defer gr.Close()
		dec = json.NewDecoder(gr)
		// Drain rest
	} else {
		dec = json.NewDecoder(c.Request.Body)
	}
	if err := dec.Decode(&br); err != nil {
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
			// Debug JSON sampling + per-device isolation checks.
			deviceID, osName, fprint, firstIFName, firstIFTx, firstIFRx := extractHostBrief(it.Payload)
			sum := sha256.Sum256(it.Payload)
			hash := fmt.Sprintf("%x", sum[:])
			if deviceID != "" {
				last := lastHostHashByDevice[deviceID]
				if last == hash {
					log.Printf("[DEBUG_MD5] Device=%s no_change=true hash=%s", deviceID, hash)
				} else {
					lastHostHashByDevice[deviceID] = hash
				}
				// Update last-seen and broadcast online=true
				markSeenAndBroadcast(deviceID, true)
			}
			// Fingerprint ownership warning.
			if fprint != "" && deviceID != "" {
				if owner, ok := fingerprintOwner[fprint]; ok && owner != deviceID {
					log.Printf("[ALERT] fingerprint collision: fp=%s deviceA=%s deviceB=%s", fprint, owner, deviceID)
				} else if !ok {
					fingerprintOwner[fprint] = deviceID
				}
			}
			log.Printf("Report Received id=%s os=%s firstIF=%s tx=%d rx=%d", deviceID, osName, firstIFName, firstIFTx, firstIFRx)
			// Optional: print network interfaces brief for sampling
			if deviceID != "" {
				if nb := extractNetDebug(it.Payload); nb != "" {
					log.Printf("[DEBUG_JSON] Device: %s, Network: %s", deviceID, nb)
				}
			}
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

// Helpers to extract minimal fields for logging/validation without strict schema coupling.
type hostIfaceBrief struct {
	Name      string `json:"name"`
	BytesSent uint64 `json:"bytes_sent"`
	BytesRecv uint64 `json:"bytes_recv"`
}
type hostBrief struct {
	DeviceID          string           `json:"device_id"`
	Fingerprint       string           `json:"fingerprint"`
	OS                string           `json:"os"`
	NetworkInterfaces []hostIfaceBrief `json:"network_interfaces"`
}

func extractHostBrief(raw json.RawMessage) (deviceID, osName, fp, firstIF string, tx, rx uint64) {
	var hb hostBrief
	if err := json.Unmarshal(raw, &hb); err == nil {
		deviceID = hb.DeviceID
		osName = hb.OS
		fp = hb.Fingerprint
		if len(hb.NetworkInterfaces) > 0 {
			firstIF = hb.NetworkInterfaces[0].Name
			tx = hb.NetworkInterfaces[0].BytesSent
			rx = hb.NetworkInterfaces[0].BytesRecv
		}
		return
	}
	// fallback: try flexible map-based extraction
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return
	}
	if v, ok := m["device_id"].(string); ok {
		deviceID = v
	}
	if v, ok := m["fingerprint"].(string); ok {
		fp = v
	}
	if v, ok := m["os"].(string); ok {
		osName = v
	}
	if arr, ok := m["network_interfaces"].([]any); ok && len(arr) > 0 {
		if mm, ok := arr[0].(map[string]any); ok {
			if n, ok := mm["name"].(string); ok {
				firstIF = n
			}
			if bs, ok := mm["bytes_sent"].(float64); ok {
				tx = uint64(bs)
			}
			if br, ok := mm["bytes_recv"].(float64); ok {
				rx = uint64(br)
			}
		}
	}
	return
}

func extractNetDebug(raw json.RawMessage) string {
	var s struct {
		NetworkInterfaces []hostIfaceBrief `json:"network_interfaces"`
		DeviceID          string           `json:"device_id"`
	}
	if err := json.Unmarshal(raw, &s); err != nil {
		return ""
	}
	if len(s.NetworkInterfaces) == 0 {
		return "[]"
	}
	// Print a compact brief only
	type brief struct {
		N string `json:"n"`
		T uint64 `json:"tx"`
		R uint64 `json:"rx"`
	}
	out := make([]brief, 0, len(s.NetworkInterfaces))
	for _, it := range s.NetworkInterfaces {
		out = append(out, brief{N: it.Name, T: it.BytesSent, R: it.BytesRecv})
	}
	b, _ := json.Marshal(out)
	return string(b)
}

// ----- Global status channel -----
func handleWSGlobal(c *gin.Context) {
	conn, err := globalUpgrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		log.Printf("upgrade ws-global failed: %v", err)
		return
	}
	globalClientsMu.Lock()
	globalClients[conn] = struct{}{}
	globalClientsMu.Unlock()

	// Send a minimal hello
	_ = conn.WriteJSON(gin.H{"type": "hello", "channel": "global_status"})
	// Immediately send a snapshot of known device statuses
	snap := currentDeviceSnapshot()
	_ = conn.WriteJSON(gin.H{"type": "device_snapshot", "devices": snap})

	go func() {
		defer func() {
			globalClientsMu.Lock()
			delete(globalClients, conn)
			globalClientsMu.Unlock()
			conn.Close()
		}()
		for {
			// keep alive; ignore incoming
			if _, _, err := conn.ReadMessage(); err != nil {
				break
			}
		}
	}()
}

func broadcastGlobal(msg any) {
	// Log the outgoing message (best-effort JSON)
	if b, err := json.Marshal(msg); err == nil {
		log.Printf("[WS-GLOBAL] Broadcasting: %s", string(b))
	}
	globalClientsMu.Lock()
	defer globalClientsMu.Unlock()
	for conn := range globalClients {
		_ = conn.WriteJSON(msg)
	}
}

func markSeenAndBroadcast(deviceID string, online bool) {
	now := time.Now().Unix()
	lastSeenMu.Lock()
	lastSeenAt[deviceID] = now
	lastSeenMu.Unlock()
	broadcastGlobal(gin.H{
		"type":      "device_update",
		"device_id": deviceID,
		"online":    online,
		"ts":        now,
	})
}

func init() {
	// Start a background ticker to mark devices offline if stale.
	go func() {
		t := time.NewTicker(30 * time.Second)
		defer t.Stop()
		for range t.C {
			now := time.Now().Unix()
			const offlineAfter = int64(60) // seconds
			lastSeenMu.Lock()
			for dev, ts := range lastSeenAt {
				if ts > 0 && now-ts > offlineAfter {
					// Only broadcast once per transition: set to 0 to avoid spamming.
					lastSeenAt[dev] = 0
					broadcastGlobal(gin.H{
						"type":      "device_update",
						"device_id": dev,
						"online":    false,
						"ts":        now,
					})
				}
			}
			lastSeenMu.Unlock()
		}
	}()
}

type deviceBrief struct {
	DeviceID  string `json:"device_id"`
	Online    bool   `json:"online"`
	UpdatedAt int64  `json:"updated_at"`
}

func currentDeviceSnapshot() []deviceBrief {
	now := time.Now().Unix()
	const offlineAfter = int64(60)
	lastSeenMu.Lock()
	defer lastSeenMu.Unlock()
	out := make([]deviceBrief, 0, len(lastSeenAt))
	for dev, ts := range lastSeenAt {
		if dev == "" {
			continue
		}
		online := ts > 0 && now-ts <= offlineAfter
		updated := ts
		if !online && ts == 0 {
			updated = 0
		}
		out = append(out, deviceBrief{
			DeviceID:  dev,
			Online:    online,
			UpdatedAt: updated,
		})
	}
	return out
}
