package websocket

import (
	"bufio"
	"crypto/sha1"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"sync"
)

const guid = "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"

// Upgrader upgrades an HTTP connection to a websocket connection.
// This is a minimal stub compatible with the usage in scripts/debug_server.
type Upgrader struct {
	ReadBufferSize  int
	WriteBufferSize int
	CheckOrigin     func(r *http.Request) bool
}

// Conn is a minimal websocket connection.
type Conn struct {
	c      net.Conn
	rwMu   sync.Mutex
	br     *bufio.Reader
	bw     *bufio.Writer
	closed bool
}

// Upgrade performs the websocket handshake and returns a websocket connection.
func (u *Upgrader) Upgrade(w http.ResponseWriter, r *http.Request, _ http.Header) (*Conn, error) {
	if r.Method != http.MethodGet {
		return nil, errors.New("websocket: invalid method")
	}

	if u.CheckOrigin != nil && !u.CheckOrigin(r) {
		return nil, errors.New("websocket: origin rejected")
	}

	if r.Header.Get("Upgrade") != "websocket" {
		// Browsers use "websocket" but allow case-insensitive values.
		// For simplicity, we only check minimal.
		// Still attempt handshake.
	}

	key := r.Header.Get("Sec-WebSocket-Key")
	if key == "" {
		return nil, errors.New("websocket: missing Sec-WebSocket-Key")
	}

	hij, ok := w.(http.Hijacker)
	if !ok {
		return nil, errors.New("websocket: response does not support hijacking")
	}
	conn, buf, err := hij.Hijack()
	if err != nil {
		return nil, err
	}

	accept := acceptKey(key)
	handshake := fmt.Sprintf(
		"HTTP/1.1 101 Switching Protocols\r\n"+
			"Upgrade: websocket\r\n"+
			"Connection: Upgrade\r\n"+
			"Sec-WebSocket-Accept: %s\r\n\r\n",
		accept,
	)
	if _, err := buf.WriteString(handshake); err != nil {
		_ = conn.Close()
		return nil, err
	}
	if err := buf.Flush(); err != nil {
		_ = conn.Close()
		return nil, err
	}

	c := &Conn{
		c:  conn,
		br: bufio.NewReader(conn),
		bw: bufio.NewWriter(conn),
	}
	return c, nil
}

func acceptKey(key string) string {
	sum := sha1.Sum([]byte(key + guid))
	return base64.StdEncoding.EncodeToString(sum[:])
}

// WriteJSON marshals v into JSON and sends it as a text frame.
func (c *Conn) WriteJSON(v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return c.writeText(b)
}

// ReadMessage reads one websocket message.
// For ping frames, it responds with pong and continues reading.
func (c *Conn) ReadMessage() (messageType int, p []byte, err error) {
	for {
		opcode, payload, err := c.readFrame()
		if err != nil {
			return 0, nil, err
		}
		switch opcode {
		case 0x1: // text
			return int(opcode), payload, nil
		case 0x2: // binary
			return int(opcode), payload, nil
		case 0x8: // close
			return int(opcode), payload, io.EOF
		case 0x9: // ping
			_ = c.writeControl(0xA, payload) // pong
			continue
		case 0xA: // pong
			continue
		default:
			// unsupported opcode; skip
			continue
		}
	}
}

func (c *Conn) Close() error {
	c.rwMu.Lock()
	defer c.rwMu.Unlock()
	if c.closed {
		return nil
	}
	c.closed = true
	return c.c.Close()
}

func (c *Conn) writeText(payload []byte) error {
	return c.writeFrame(0x1, payload)
}

func (c *Conn) writeControl(opcode int, payload []byte) error {
	// FIN=1, opcode=control
	// Control frames must not be fragmented and length <= 125; we will truncate if needed.
	if len(payload) > 125 {
		payload = payload[:125]
	}

	c.rwMu.Lock()
	defer c.rwMu.Unlock()

	b := make([]byte, 0, 2+len(payload))
	b = append(b, 0x80|byte(opcode))
	if len(payload) <= 125 {
		b = append(b, byte(len(payload))) // no mask for server frames
	}
	b = append(b, payload...)
	if _, err := c.bw.Write(b); err != nil {
		return err
	}
	return c.bw.Flush()
}

func (c *Conn) writeFrame(opcode int, payload []byte) error {
	c.rwMu.Lock()
	defer c.rwMu.Unlock()

	payloadLen := len(payload)
	var header []byte

	first := byte(0x80 | byte(opcode)) // FIN=1
	header = append(header, first)

	if payloadLen < 126 {
		header = append(header, byte(payloadLen)) // mask=0
	} else if payloadLen <= 0xFFFF {
		header = append(header, 126)
		header = append(header, byte(payloadLen>>8), byte(payloadLen))
	} else {
		// keep it simple: cap to 64-bit sizes
		header = append(header, 127)
		for i := 7; i >= 0; i-- {
			header = append(header, byte(uint64(payloadLen)>>uint(8*i)))
		}
	}

	if _, err := c.bw.Write(header); err != nil {
		return err
	}
	if payloadLen > 0 {
		if _, err := c.bw.Write(payload); err != nil {
			return err
		}
	}
	return c.bw.Flush()
}

func (c *Conn) readFrame() (opcode byte, payload []byte, err error) {
	// Read first two bytes
	var hdr [2]byte
	if _, err := io.ReadFull(c.br, hdr[:]); err != nil {
		return 0, nil, err
	}

	b1 := hdr[0]
	b2 := hdr[1]

	_ = b1 & 0x80 // FIN, ignore fragmentation for now
	opcode = b1 & 0x0f

	masked := (b2 & 0x80) != 0
	plen := int(b2 & 0x7f)

	switch plen {
	case 126:
		var ext [2]byte
		if _, err := io.ReadFull(c.br, ext[:]); err != nil {
			return 0, nil, err
		}
		plen = int(ext[0])<<8 | int(ext[1])
	case 127:
		var ext [8]byte
		if _, err := io.ReadFull(c.br, ext[:]); err != nil {
			return 0, nil, err
		}
		// Cap to avoid huge allocations.
		n := uint64(0)
		for i := 0; i < 8; i++ {
			n = (n << 8) | uint64(ext[i])
		}
		if n > 16*1024*1024 {
			return 0, nil, errors.New("websocket: payload too large")
		}
		plen = int(n)
	}

	var maskKey [4]byte
	if masked {
		if _, err := io.ReadFull(c.br, maskKey[:]); err != nil {
			return 0, nil, err
		}
	}

	payload = make([]byte, plen)
	if plen > 0 {
		if _, err := io.ReadFull(c.br, payload); err != nil {
			return 0, nil, err
		}
	}

	if masked && plen > 0 {
		for i := 0; i < plen; i++ {
			payload[i] ^= maskKey[i%4]
		}
	}
	return opcode, payload, nil
}
