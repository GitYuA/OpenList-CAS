package casmeta

import (
	"encoding/base64"
	"testing"
)

func TestDecodeSupportsPlainJSONPayload(t *testing.T) {
	payload := []byte(`{"name":"demo.mp4","size":123,"md5":"abc123","sliceMd5":"abc123","create_time":"1700000000"}`)

	info, err := Decode(payload)
	if err != nil {
		t.Fatalf("Decode plain JSON payload failed: %v", err)
	}
	if info.Name != "demo.mp4" {
		t.Fatalf("unexpected name: %s", info.Name)
	}
	if info.Size != 123 {
		t.Fatalf("unexpected size: %d", info.Size)
	}
	if info.MD5 != "abc123" {
		t.Fatalf("unexpected md5: %s", info.MD5)
	}
}

func TestDecodeSupportsBase64EncodedPayload(t *testing.T) {
	encoded := base64.StdEncoding.EncodeToString([]byte(`{"name":"demo.mp4","size":123,"md5":"abc123","sliceMd5":"abc123","create_time":"1700000000"}`))

	info, err := Decode([]byte(encoded))
	if err != nil {
		t.Fatalf("Decode base64 payload failed: %v", err)
	}
	if info.Name != "demo.mp4" {
		t.Fatalf("unexpected name: %s", info.Name)
	}
}
