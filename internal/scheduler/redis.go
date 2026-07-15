package scheduler

import (
	"bufio"
	"context"
	"crypto/tls"
	"errors"
	"io"
	"net"
	"strconv"
	"strings"
	"time"
)

type RedisOptions struct {
	Address  string
	Username string
	Password string
	Database int
	TLS      bool
}

// RedisClient is a deliberately narrow RESP client for scheduler Lua scripts.
// It opens one bounded connection per operation, so Redis recovery does not
// depend on stale pooled connections and Close has no background work to stop.
type RedisClient struct {
	options RedisOptions
}

func NewRedisClient(options RedisOptions) (*RedisClient, error) {
	if strings.TrimSpace(options.Address) == "" || strings.TrimSpace(options.Password) == "" || options.Database < 0 {
		return nil, ErrLeaseInvalid
	}
	if _, _, err := net.SplitHostPort(options.Address); err != nil {
		return nil, ErrLeaseInvalid
	}
	return &RedisClient{options: options}, nil
}

func (client *RedisClient) Eval(ctx context.Context, script string, keys []string, args ...string) ([]int64, error) {
	if client == nil || strings.TrimSpace(script) == "" {
		return nil, ErrCoordinationDown
	}
	connection, err := (&net.Dialer{}).DialContext(ctx, "tcp", client.options.Address)
	if err != nil {
		return nil, ErrCoordinationDown
	}
	defer connection.Close()
	if deadline, ok := ctx.Deadline(); ok {
		_ = connection.SetDeadline(deadline)
	} else {
		_ = connection.SetDeadline(time.Now().Add(5 * time.Second))
	}
	if client.options.TLS {
		host, _, _ := net.SplitHostPort(client.options.Address)
		secured := tls.Client(connection, &tls.Config{MinVersion: tls.VersionTLS13, ServerName: host})
		if err := secured.HandshakeContext(ctx); err != nil {
			return nil, ErrCoordinationDown
		}
		connection = secured
	}
	reader, writer := bufio.NewReader(connection), bufio.NewWriter(connection)
	auth := []string{"AUTH"}
	if client.options.Username != "" {
		auth = append(auth, client.options.Username)
	}
	auth = append(auth, client.options.Password)
	if err := redisCommand(writer, auth...); err != nil {
		return nil, ErrCoordinationDown
	}
	if _, err := redisReply(reader); err != nil {
		return nil, ErrCoordinationDown
	}
	if client.options.Database != 0 {
		if err := redisCommand(writer, "SELECT", strconv.Itoa(client.options.Database)); err != nil {
			return nil, ErrCoordinationDown
		}
		if _, err := redisReply(reader); err != nil {
			return nil, ErrCoordinationDown
		}
	}
	command := []string{"EVAL", script, strconv.Itoa(len(keys))}
	command = append(command, keys...)
	command = append(command, args...)
	if err := redisCommand(writer, command...); err != nil {
		return nil, ErrCoordinationDown
	}
	result, err := redisReply(reader)
	if err != nil {
		return nil, ErrCoordinationDown
	}
	items, ok := result.([]any)
	if !ok {
		return nil, ErrCoordinationDown
	}
	converted := make([]int64, len(items))
	for index, item := range items {
		value, ok := item.(int64)
		if !ok {
			return nil, ErrCoordinationDown
		}
		converted[index] = value
	}
	return converted, nil
}

func (client *RedisClient) Close() error { return nil }

func redisCommand(writer *bufio.Writer, values ...string) error {
	if _, err := writer.WriteString("*" + strconv.Itoa(len(values)) + "\r\n"); err != nil {
		return err
	}
	for _, value := range values {
		if _, err := writer.WriteString("$" + strconv.Itoa(len(value)) + "\r\n" + value + "\r\n"); err != nil {
			return err
		}
	}
	return writer.Flush()
}

func redisReply(reader *bufio.Reader) (any, error) {
	prefix, err := reader.ReadByte()
	if err != nil {
		return nil, err
	}
	line, err := reader.ReadString('\n')
	if err != nil || !strings.HasSuffix(line, "\r\n") {
		return nil, errors.New("invalid Redis reply")
	}
	line = strings.TrimSuffix(line, "\r\n")
	switch prefix {
	case '+':
		return line, nil
	case '-':
		return nil, errors.New("Redis command rejected")
	case ':':
		return strconv.ParseInt(line, 10, 64)
	case '$':
		length, parseErr := strconv.Atoi(line)
		if parseErr != nil || length < -1 || length > 16<<20 {
			return nil, errors.New("invalid Redis bulk reply")
		}
		if length == -1 {
			return nil, nil
		}
		value := make([]byte, length+2)
		if _, err := io.ReadFull(reader, value); err != nil || string(value[length:]) != "\r\n" {
			return nil, errors.New("invalid Redis bulk reply")
		}
		return string(value[:length]), nil
	case '*':
		length, parseErr := strconv.Atoi(line)
		if parseErr != nil || length < 0 || length > 1024 {
			return nil, errors.New("invalid Redis array reply")
		}
		values := make([]any, length)
		for index := range values {
			values[index], err = redisReply(reader)
			if err != nil {
				return nil, err
			}
		}
		return values, nil
	default:
		return nil, errors.New("invalid Redis reply type")
	}
}
