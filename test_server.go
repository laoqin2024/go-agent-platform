package main

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
)

func main() {
	var (
		addr       = flag.String("addr", ":8443", "listen address, e.g. :8443")
		caPath     = flag.String("ca", "./certs/ca.pem", "CA root certificate (PEM) used to verify client cert")
		serverCert = flag.String("server-cert", "./certs/server.pem", "server certificate (PEM)")
		serverKey  = flag.String("server-key", "./certs/server-key.pem", "server private key (PEM)")
		path       = flag.String("path", "/ingest", "HTTP ingest path")
	)
	flag.Parse()

	// 1) 加载 CA，用于校验客户端证书（mTLS）
	caPEM, err := os.ReadFile(*caPath)
	if err != nil {
		log.Fatalf("read CA cert failed: %v", err)
	}
	clientCAPool := x509.NewCertPool()
	if !clientCAPool.AppendCertsFromPEM(caPEM) {
		log.Fatalf("append CA cert failed")
	}

	// 2) 加载服务端证书
	cert, err := tls.LoadX509KeyPair(*serverCert, *serverKey)
	if err != nil {
		log.Fatalf("load server cert/key failed: %v", err)
	}

	tlsCfg := &tls.Config{
		Certificates: []tls.Certificate{cert},
		ClientCAs:    clientCAPool,
		// 要求客户端必须提供证书（双向 TLS）
		// ClientAuth: tls.RequireAndVerifyClientCert,
		ClientAuth: tls.NoClientCert, // 仅测试浏览器时使用
		MinVersion: tls.VersionTLS12,
	}

	mux := http.NewServeMux()
	mux.HandleFunc(*path, handleIngest)

	srv := &http.Server{
		Addr:      *addr,
		Handler:   mux,
		TLSConfig: tlsCfg,
	}

	log.Printf("Test mTLS server listening on https://127.0.0.1%v%s", *addr, *path)
	if err := srv.ListenAndServeTLS("", ""); err != nil {
		log.Fatalf("server exited: %v", err)
	}
}

func handleIngest(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "only POST allowed", http.StatusMethodNotAllowed)
		return
	}
	defer r.Body.Close()

	// Optional: log client cert subject (useful for handshake debugging).
	if r.TLS != nil && len(r.TLS.PeerCertificates) > 0 {
		log.Printf("mTLS client cert: subject=%s", r.TLS.PeerCertificates[0].Subject.String())
	}

	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "read body failed", http.StatusBadRequest)
		return
	}

	// 尝试格式化为 JSON 打印
	var js any
	if err := json.Unmarshal(body, &js); err != nil {
		log.Printf("=== Received (non-JSON or invalid JSON) ===\n%s\n", string(body))
	} else {
		pretty, _ := json.MarshalIndent(js, "", "  ")
		log.Printf("=== Received JSON ===\n%s\n", string(pretty))
	}

	// 简单返回 200，表示已成功接收
	w.WriteHeader(http.StatusOK)
	fmt.Fprintln(w, `{"status":"ok"}`)
}
