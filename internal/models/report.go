package models

// USBEvent is one USB audit event for agent report pipeline.
type USBEvent struct {
	DeviceID   string `json:"device_id"`   // host fingerprint/device id
	Action     string `json:"action"`      // insert/remove
	USBID      string `json:"usb_id"`      // hardware id
	VolumeName string `json:"volume_name"` // volume label / drive letter
	Timestamp  int64  `json:"timestamp"`   // unix seconds
}

