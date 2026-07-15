package scheduler

import (
	"bufio"
	"context"
	"errors"
	"io"
	"net"
	"strconv"
	"strings"
	"testing"

	"github.com/google/uuid"
)

func TestRedisClientFailsClosedWithoutBackend(t *testing.T) {
	client, err := NewRedisClient(RedisOptions{Address: "127.0.0.1:1", Password: "test-only"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	manager, err := NewLeaseManager(client, testLeaseConfig(), uuid.New())
	if err != nil {
		t.Fatal(err)
	}
	_, err = manager.Acquire(context.Background(), []ResourceKey{{Kind: "endpoint", TenantID: uuid.New(), ResourceID: uuid.New()}})
	if !errors.Is(err, ErrCoordinationDown) {
		t.Fatalf("Acquire() error=%v", err)
	}
}

func TestRedisClientEvaluatesScriptsAfterRecovery(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	serverDone := make(chan error, 1)
	go func() {
		connection, acceptErr := listener.Accept()
		if acceptErr != nil {
			serverDone <- acceptErr
			return
		}
		defer connection.Close()
		reader := bufio.NewReader(connection)
		auth, readErr := readRedisCommand(reader)
		if readErr != nil || len(auth) != 2 || auth[0] != "AUTH" || auth[1] != "test-only" {
			serverDone <- errors.New("invalid AUTH command")
			return
		}
		if _, writeErr := connection.Write([]byte("+OK\r\n")); writeErr != nil {
			serverDone <- writeErr
			return
		}
		eval, readErr := readRedisCommand(reader)
		if readErr != nil || len(eval) < 4 || eval[0] != "EVAL" || eval[2] != "1" {
			serverDone <- errors.New("invalid EVAL command")
			return
		}
		_, writeErr := connection.Write([]byte("*2\r\n:1\r\n:9\r\n"))
		serverDone <- writeErr
	}()
	client, err := NewRedisClient(RedisOptions{Address: listener.Addr().String(), Password: "test-only"})
	if err != nil {
		t.Fatal(err)
	}
	result, err := client.Eval(t.Context(), "return {1,9}", []string{"lease"}, "owner")
	if err != nil {
		t.Fatal(err)
	}
	if len(result) != 2 || result[0] != 1 || result[1] != 9 {
		t.Fatalf("result=%v", result)
	}
	if err := <-serverDone; err != nil {
		t.Fatal(err)
	}
}

func readRedisCommand(reader *bufio.Reader) ([]string, error) {
	header, err := reader.ReadString('\n')
	if err != nil || !strings.HasPrefix(header, "*") {
		return nil, errors.New("invalid command header")
	}
	count, err := strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(header, "*")))
	if err != nil || count < 1 {
		return nil, errors.New("invalid command length")
	}
	values := make([]string, count)
	for index := range values {
		lengthLine, readErr := reader.ReadString('\n')
		if readErr != nil || !strings.HasPrefix(lengthLine, "$") {
			return nil, errors.New("invalid bulk header")
		}
		length, parseErr := strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(lengthLine, "$")))
		if parseErr != nil || length < 0 {
			return nil, errors.New("invalid bulk length")
		}
		payload := make([]byte, length+2)
		if _, readErr := io.ReadFull(reader, payload); readErr != nil {
			return nil, readErr
		}
		values[index] = string(payload[:length])
	}
	return values, nil
}
