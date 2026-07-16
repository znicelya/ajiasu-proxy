package main

import (
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
)

func main() {
	if len(os.Args) == 2 && os.Args[1] == "health" {
		response, err := http.Get("http://127.0.0.1:8080/healthz")
		if err != nil || response.StatusCode != http.StatusOK {
			os.Exit(1)
		}
		response.Body.Close()
		return
	}
	go serveEcho("0.0.0.0:8443")
	if err := http.ListenAndServe("0.0.0.0:8080", handler()); err != nil {
		fmt.Fprintln(os.Stderr, "fake target unavailable")
		os.Exit(1)
	}
}

func handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = io.WriteString(w, "ajiasu-compose-fake-target")
	})
	return mux
}

func serveEcho(address string) error {
	listener, err := net.Listen("tcp", address)
	if err != nil {
		return err
	}
	for {
		connection, acceptErr := listener.Accept()
		if acceptErr != nil {
			return acceptErr
		}
		go func() {
			defer connection.Close()
			_, _ = io.Copy(connection, connection)
		}()
	}
}
