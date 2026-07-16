package main

import (
	"bytes"
	"encoding/binary"
	"testing"
)

func TestReadMetadata(t *testing.T) {
	frame := append([]byte("AJR1"), 2, 2, 0, 11, 1, 187)
	frame = append(frame, []byte("example.com")...)
	metadata, err := readMetadata(bytes.NewReader(frame))
	if err != nil || metadata.protocol != 2 || metadata.dnsMode != 2 || metadata.targetHost != "example.com" || metadata.targetPort != 443 {
		t.Fatalf("metadata=%#v error=%v", metadata, err)
	}
	frame[6] = 0x20
	binary.BigEndian.PutUint16(frame[8:10], 0)
	if _, err := readMetadata(bytes.NewReader(frame)); err == nil {
		t.Fatal("invalid metadata was accepted")
	}
}
