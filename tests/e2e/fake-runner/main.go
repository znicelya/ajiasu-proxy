package main

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"time"
)

const (
	defaultSocket = "/run/ajiasu-relay/runner.sock"
	maxHostBytes  = 4096
)

type metadata struct {
	protocol   byte
	dnsMode    byte
	targetHost string
	targetPort uint16
}

func main() {
	socket := os.Getenv("AJIASU_RUNNER_RELAY_SOCKET")
	if socket == "" {
		socket = defaultSocket
	}
	if len(os.Args) == 2 && os.Args[1] == "health" {
		info, err := os.Stat(socket)
		if err != nil || info.Mode()&os.ModeSocket == 0 {
			os.Exit(1)
		}
		return
	}
	if len(os.Args) != 1 {
		fmt.Fprintln(os.Stderr, "usage: fake-runner [health]")
		os.Exit(2)
	}
	if err := serve(socket); err != nil {
		fmt.Fprintln(os.Stderr, "fake runner unavailable")
		os.Exit(1)
	}
}

func serve(socket string) error {
	if !filepath.IsAbs(socket) || filepath.Ext(socket) != ".sock" {
		return errors.New("invalid socket")
	}
	if err := os.MkdirAll(filepath.Dir(socket), 0o700); err != nil {
		return err
	}
	if err := os.Remove(socket); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	listener, err := net.Listen("unix", socket)
	if err != nil {
		return err
	}
	defer listener.Close()
	defer os.Remove(socket)
	if err := os.Chmod(socket, 0o600); err != nil {
		return err
	}
	for {
		connection, err := listener.Accept()
		if err != nil {
			return err
		}
		go handle(connection)
	}
}

func handle(connection net.Conn) {
	defer connection.Close()
	_ = connection.SetDeadline(time.Now().Add(2 * time.Minute))
	meta, err := readMetadata(connection)
	if err != nil || meta.targetHost == "" || meta.targetPort == 0 || meta.dnsMode < 1 || meta.dnsMode > 2 {
		return
	}
	switch meta.protocol {
	case 1:
		request := make([]byte, 64*1024)
		if _, err := connection.Read(request); err != nil {
			return
		}
		_, _ = io.WriteString(connection, "HTTP/1.1 200 OK\r\nContent-Type: text/plain\r\nContent-Length: 18\r\nConnection: close\r\n\r\najiasu-fake-runner")
	case 2, 3:
		buffer := make([]byte, 64*1024)
		for {
			length, err := connection.Read(buffer)
			if length > 0 {
				if _, writeErr := connection.Write(buffer[:length]); writeErr != nil {
					return
				}
			}
			if err != nil {
				return
			}
		}
	}
}

func readMetadata(reader io.Reader) (metadata, error) {
	var header [10]byte
	if _, err := io.ReadFull(reader, header[:]); err != nil {
		return metadata{}, err
	}
	if string(header[:4]) != "AJR1" || header[4] < 1 || header[4] > 3 {
		return metadata{}, errors.New("invalid metadata")
	}
	hostLength := int(binary.BigEndian.Uint16(header[6:8]))
	if hostLength < 1 || hostLength > maxHostBytes {
		return metadata{}, errors.New("invalid host")
	}
	host := make([]byte, hostLength)
	if _, err := io.ReadFull(reader, host); err != nil {
		return metadata{}, err
	}
	return metadata{
		protocol: header[4], dnsMode: header[5], targetHost: string(host),
		targetPort: binary.BigEndian.Uint16(header[8:10]),
	}, nil
}
