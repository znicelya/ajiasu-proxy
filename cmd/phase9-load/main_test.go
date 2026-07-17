package main

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"
)

func TestLoadHarnessUsesBoundedConcurrentConnect(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	go func() {
		for {
			connection, acceptErr := listener.Accept()
			if acceptErr != nil {
				return
			}
			go func() {
				defer connection.Close()
				line, _ := bufio.NewReader(connection).ReadString('\n')
				if strings.HasPrefix(line, "CONNECT example.test:443 ") {
					_, _ = fmt.Fprint(connection, "HTTP/1.1 200 Connection Established\r\n\r\n")
				}
			}()
		}
	}()
	result := run(context.Background(), listener.Addr().String(), "example.test:443", 32, time.Millisecond, time.Second)
	if result.Succeeded != 32 || result.Failed != 0 || result.HeapMiB <= 0 {
		t.Fatalf("unexpected load result: %+v", result)
	}
}
