//go:build wsclient

package main

import (
	"bufio"
	"crypto/rand"
	"crypto/sha1"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/url"
	"strings"
	"time"
)

const guid = "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"

func main() {
	target := "ws://127.0.0.1:8080/ws"
	u, err := url.Parse(target)
	if err != nil {
		panic(err)
	}

	host := u.Hostname()
	port := u.Port()
	if port == "" {
		port = "80"
	}
	path := u.EscapedPath()
	if path == "" {
		path = "/"
	}

	conn, err := net.DialTimeout("tcp", net.JoinHostPort(host, port), 3*time.Second)
	if err != nil {
		panic(err)
	}
	defer conn.Close()

	key := randomWebSocketKey()
	req := fmt.Sprintf("GET %s HTTP/1.1\r\nHost: %s\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Key: %s\r\nSec-WebSocket-Version: 13\r\n\r\n",
		path, u.Host, key)
	if _, err := io.WriteString(conn, req); err != nil {
		panic(err)
	}

	// Read handshake response.
	br := bufio.NewReader(conn)
	resp, err := br.ReadString('\n')
	if err != nil {
		panic(err)
	}
	_ = resp
	// Drain headers until blank line.
	for {
		line, err := br.ReadString('\n')
		if err != nil {
			panic(err)
		}
		line = strings.TrimSpace(line)
		if line == "" {
			break
		}
		if strings.Contains(strings.ToLower(line), "sec-websocket-accept") {
			// ignore
		}
	}

	// Read one websocket text frame.
	opcode, payload, err := readFrame(br)
	if err != nil {
		panic(err)
	}
	if opcode != 0x1 {
		fmt.Println("unexpected opcode:", opcode)
		return
	}

	// Try pretty print JSON.
	var js any
	if json.Unmarshal(payload, &js) == nil {
		b, _ := json.MarshalIndent(js, "", "  ")
		fmt.Println(string(b))
		return
	}
	fmt.Println(string(payload))
}

func randomWebSocketKey() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return base64.StdEncoding.EncodeToString(b[:])
}

func readFrame(br *bufio.Reader) (opcode byte, payload []byte, err error) {
	var hdr [2]byte
	if _, err := io.ReadFull(br, hdr[:]); err != nil {
		return 0, nil, err
	}
	b1 := hdr[0]
	b2 := hdr[1]
	opcode = b1 & 0x0f
	masked := (b2 & 0x80) != 0
	plen := int(b2 & 0x7f)

	switch plen {
	case 126:
		var ext [2]byte
		if _, err := io.ReadFull(br, ext[:]); err != nil {
			return 0, nil, err
		}
		plen = int(ext[0])<<8 | int(ext[1])
	case 127:
		var ext [8]byte
		if _, err := io.ReadFull(br, ext[:]); err != nil {
			return 0, nil, err
		}
		// cap
		var n uint64
		for i := 0; i < 8; i++ {
			n = (n << 8) | uint64(ext[i])
		}
		plen = int(n)
	}

	var maskKey [4]byte
	if masked {
		if _, err := io.ReadFull(br, maskKey[:]); err != nil {
			return 0, nil, err
		}
	}
	payload = make([]byte, plen)
	if plen > 0 {
		if _, err := io.ReadFull(br, payload); err != nil {
			return 0, nil, err
		}
	}
	if masked {
		for i := 0; i < plen; i++ {
			payload[i] ^= maskKey[i%4]
		}
	}
	return opcode, payload, nil
}

var _ = sha1.Sum // silence unused warning if build tags change

