package main

import (
	"bytes"
	"encoding/binary"
	"io"
	"net"
	"testing"
	"time"
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

func TestFakeRunnerHTTPConnectAndSOCKSPaths(t *testing.T) {
	for _, test := range []struct {
		name, payload string
		protocol      byte
		want          string
	}{
		{name: "http", protocol: 1, payload: "GET / HTTP/1.1\r\nHost: target.test\r\n\r\n", want: "ajiasu-fake-runner"},
		{name: "connect", protocol: 2, payload: "connect-canary", want: "connect-canary"},
		{name: "socks5", protocol: 3, payload: "socks-canary", want: "socks-canary"},
	} {
		t.Run(test.name, func(t *testing.T) {
			client, server := net.Pipe()
			defer client.Close()
			go handle(server)
			_ = client.SetDeadline(time.Now().Add(5 * time.Second))
			host := "target.test"
			frame := append([]byte("AJR1"), test.protocol, 2, 0, byte(len(host)), 0, 80)
			frame = append(frame, host...)
			frame = append(frame, test.payload...)
			if _, err := client.Write(frame); err != nil {
				t.Fatal(err)
			}
			buffer := make([]byte, 4096)
			length, err := client.Read(buffer)
			if err != nil && err != io.EOF {
				t.Fatal(err)
			}
			if !bytes.Contains(buffer[:length], []byte(test.want)) {
				t.Fatalf("response=%q", buffer[:length])
			}
		})
	}
}
