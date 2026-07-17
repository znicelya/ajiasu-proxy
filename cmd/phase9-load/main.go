package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net"
	"os"
	"runtime"
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

type result struct {
	Connections int     `json:"connections"`
	Succeeded   int64   `json:"succeeded"`
	Failed      int64   `json:"failed"`
	P95Millis   float64 `json:"p95_millis"`
	P99Millis   float64 `json:"p99_millis"`
	HeapMiB     float64 `json:"heap_mib"`
	Duration    string  `json:"duration"`
}

func main() {
	address := flag.String("address", "127.0.0.1:8080", "proxy address")
	target := flag.String("target", "example.test:443", "CONNECT target")
	connections := flag.Int("connections", 100, "concurrent connections, maximum 10000")
	hold := flag.Duration("hold", time.Second, "connection hold duration")
	timeout := flag.Duration("timeout", 10*time.Second, "dial and response timeout")
	maxErrors := flag.Int("max-errors", 0, "maximum accepted connection failures")
	maxHeap := flag.Float64("max-heap-mib", 1024, "maximum client heap allocation")
	flag.Parse()
	if *connections < 1 || *connections > 10000 {
		fatal(errors.New("connections must be between 1 and 10000"))
	}
	if *connections == 10000 && os.Getenv("AJIASU_PHASE9_LOAD_GATE") != "I_UNDERSTAND" {
		fatal(errors.New("10000-connection run requires AJIASU_PHASE9_LOAD_GATE=I_UNDERSTAND"))
	}
	ctx, cancel := context.WithTimeout(context.Background(), *timeout+*hold+30*time.Second)
	defer cancel()
	started := time.Now()
	output := run(ctx, *address, *target, *connections, *hold, *timeout)
	output.Duration = time.Since(started).Round(time.Millisecond).String()
	if err := json.NewEncoder(os.Stdout).Encode(output); err != nil {
		fatal(err)
	}
	if output.Failed > int64(*maxErrors) || output.HeapMiB > *maxHeap {
		os.Exit(1)
	}
}

func run(ctx context.Context, address, target string, connections int, hold, timeout time.Duration) result {
	var succeeded, failed atomic.Int64
	latencies := make([]time.Duration, 0, connections)
	var lock sync.Mutex
	var group sync.WaitGroup
	ready := make(chan struct{})
	group.Add(connections)
	for range connections {
		go func() {
			defer group.Done()
			<-ready
			started := time.Now()
			dialer := net.Dialer{Timeout: timeout}
			connection, err := dialer.DialContext(ctx, "tcp", address)
			if err != nil {
				failed.Add(1)
				return
			}
			defer connection.Close()
			_ = connection.SetDeadline(time.Now().Add(timeout))
			if _, err = fmt.Fprintf(connection, "CONNECT %s HTTP/1.1\r\nHost: %s\r\n\r\n", target, target); err != nil {
				failed.Add(1)
				return
			}
			line, err := bufio.NewReader(connection).ReadString('\n')
			if err != nil || len(line) < 12 || line[9:12] != "200" {
				failed.Add(1)
				return
			}
			latency := time.Since(started)
			lock.Lock()
			latencies = append(latencies, latency)
			lock.Unlock()
			succeeded.Add(1)
			select {
			case <-ctx.Done():
			case <-time.After(hold):
			}
		}()
	}
	close(ready)
	group.Wait()
	sort.Slice(latencies, func(i, j int) bool { return latencies[i] < latencies[j] })
	var memory runtime.MemStats
	runtime.ReadMemStats(&memory)
	return result{Connections: connections, Succeeded: succeeded.Load(), Failed: failed.Load(), P95Millis: percentile(latencies, .95), P99Millis: percentile(latencies, .99), HeapMiB: float64(memory.HeapAlloc) / 1024 / 1024}
}

func percentile(values []time.Duration, percentile float64) float64 {
	if len(values) == 0 {
		return 0
	}
	index := int(float64(len(values)-1) * percentile)
	return float64(values[index].Microseconds()) / 1000
}

func fatal(err error) {
	_, _ = fmt.Fprintln(os.Stderr, err)
	os.Exit(2)
}
