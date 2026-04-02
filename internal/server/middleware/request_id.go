package middleware

import (
	"crypto/rand"
	"encoding/hex"

	"github.com/gin-gonic/gin"
)

// RequestID attaches a per-request ID for easier log correlation.
func RequestID() gin.HandlerFunc {
	return func(c *gin.Context) {
		id := c.GetHeader("X-Request-Id")
		if id == "" {
			// 16 bytes => 32 hex chars; good enough for correlation IDs.
			var b [16]byte
			if _, err := rand.Read(b[:]); err != nil {
				id = "req_unknown"
			} else {
				id = hex.EncodeToString(b[:])
			}
		}
		c.Set("request_id", id)
		c.Writer.Header().Set("X-Request-Id", id)
		c.Next()
	}
}
