//go:build !windows

package service

import (
	"context"
	"log/slog"
	"time"

	"github.com/qinyilin/go-agent/internal/models"
)

type USBEventHandler func(e models.USBEvent)

type USBPolicy struct {
	AllowedInstanceIDs []string `json:"allowed_instance_ids"`
	AllowedDevices     []string `json:"allowed_devices"`
}

// StartUSBMonitor is a no-op on non-Windows platforms.
func StartUSBMonitor(ctx context.Context, logger *slog.Logger, pollInterval time.Duration, onEvent func(models.USBEvent)) error {
	return nil
}

// SetUSBStorageEnabled is a no-op on non-Windows platforms.
func SetUSBStorageEnabled(enabled bool) error {
	return nil
}

func SetUSBPolicy(p USBPolicy) {}

func GetUSBPolicy() USBPolicy { return USBPolicy{} }

func AddAllowedInstanceID(id string) {}

func IsUSBInstanceAllowed(id string) bool { return true }

func DisconnectUSBInstanceID(instanceID string) error { return nil }

func ConfigureUSBPolicyFetcher(apiURL, deviceID string) {}
