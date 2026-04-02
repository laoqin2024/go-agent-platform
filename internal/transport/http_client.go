package transport

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"

	"log/slog"
)

type HttpClient struct {
	endpoint string
	client   *http.Client
	logger   *slog.Logger
}

type HttpClientOption func(*HttpClient)

// WithLogger sets logger for internal warnings (optional).
func WithLogger(l *slog.Logger) HttpClientOption {
	return func(h *HttpClient) {
		h.logger = l
	}
}

// NewHttpClient creates an HTTPS client with optional mTLS.
//
// If insecureSkipVerify is true, TLS server certificate verification is skipped (demo only).
func NewHttpClient(
	endpoint string,
	caPath string,
	clientCertPath string,
	clientKeyPath string,
	insecureSkipVerify bool,
	opts ...HttpClientOption,
) (*HttpClient, error) {
	if endpoint == "" {
		return nil, errors.New("transport: empty endpoint")
	}

	var logger *slog.Logger
	h := &HttpClient{
		endpoint: endpoint,
		logger:   logger,
	}
	for _, opt := range opts {
		opt(h)
	}

	tlsConfig := &tls.Config{
		MinVersion:         tls.VersionTLS12,
		InsecureSkipVerify: insecureSkipVerify, // demo only
	}

	// Load CA root (if provided) to verify server.
	if caPath != "" {
		caPEM, err := os.ReadFile(caPath)
		if err != nil {
			if !insecureSkipVerify {
				return nil, fmt.Errorf("transport: read CA cert: %w", err)
			}
			if h.logger != nil {
				h.logger.Warn("transport: failed to load CA cert; continuing insecurely", "caPath", caPath, "err", err)
			}
		} else {
			pool := x509.NewCertPool()
			if ok := pool.AppendCertsFromPEM(caPEM); !ok {
				if !insecureSkipVerify {
					return nil, fmt.Errorf("transport: invalid CA cert PEM in %s", caPath)
				}
				if h.logger != nil {
					h.logger.Warn("transport: invalid CA cert PEM; continuing insecurely", "caPath", caPath)
				}
			} else {
				tlsConfig.RootCAs = pool
			}
		}
	}

	// Load client certificate for mTLS.
	if clientCertPath != "" && clientKeyPath != "" {
		cert, err := tls.LoadX509KeyPair(clientCertPath, clientKeyPath)
		if err != nil {
			if !insecureSkipVerify {
				return nil, fmt.Errorf("transport: load client cert/key: %w", err)
			}
			if h.logger != nil {
				h.logger.Warn("transport: failed to load client cert/key; continuing without mTLS", "certPath", clientCertPath, "keyPath", clientKeyPath, "err", err)
			}
		} else {
			tlsConfig.Certificates = []tls.Certificate{cert}
		}
	}

	httpTr := &http.Transport{
		TLSClientConfig: tlsConfig,
	}

	h.client = &http.Client{
		Transport: httpTr,
		Timeout:   20 * time.Second, // extra safety
	}
	return h, nil
}

// PostJSON sends a JSON request body via POST and returns the status and response body.
func (h *HttpClient) PostJSON(ctx context.Context, reqBody []byte) (int, []byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, h.endpoint, bytes.NewReader(reqBody))
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := h.client.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()

	b, err := io.ReadAll(resp.Body)
	if err != nil {
		return resp.StatusCode, nil, err
	}
	return resp.StatusCode, b, nil
}

