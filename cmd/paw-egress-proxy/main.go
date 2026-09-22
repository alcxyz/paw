// Command paw-egress-proxy is the per-workspace bounded egress proxy
// described in ADR-013. It accepts only HTTP CONNECT to port 443, admits a
// destination only when its hostname exactly matches a reviewed destination
// list, and refuses destinations that resolve to private, link-local,
// loopback, multicast, or other disallowed address ranges.
package main

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/alcxyz/paw/internal/egress"
)

const (
	defaultListen       = ":3128"
	defaultDestinations = "/etc/paw-egress/destinations"
	readHeaderTimeout   = 10 * time.Second
	maxHeaderBytes      = 16 * 1024
	shutdownGrace       = 10 * time.Second
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("paw-egress-proxy", flag.ContinueOnError)
	fs.SetOutput(stderr)
	listen := fs.String("listen", defaultListen, "address to listen on")
	destinationsPath := fs.String("destinations", defaultDestinations, "path to the destination list")

	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 2
		}
		return 2
	}
	if fs.NArg() != 0 {
		fmt.Fprintf(stderr, "paw-egress-proxy: unexpected arguments: %v\n", fs.Args())
		fs.Usage()
		return 2
	}

	destinations, err := egress.LoadDestinations(*destinationsPath)
	if err != nil {
		fmt.Fprintf(stderr, "paw-egress-proxy: destination list unavailable; denying all egress: %v\n", err)
		destinations = nil
	}

	logEvent := func(e egress.Event) {
		writeEventJSON(stdout, e)
	}

	handler := egress.NewHandler(egress.Config{
		Destinations: destinations,
		Log:          logEvent,
	})

	server := &http.Server{
		Addr:              *listen,
		Handler:           handler,
		ReadHeaderTimeout: readHeaderTimeout,
		MaxHeaderBytes:    maxHeaderBytes,
		TLSNextProto:      map[string]func(*http.Server, *tls.Conn, http.Handler){},
	}

	fmt.Fprintf(stderr, "paw-egress-proxy: listening on %s with %d destination(s)\n", *listen, len(destinations))

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()

	serveErr := make(chan error, 1)
	go func() {
		serveErr <- server.ListenAndServe()
	}()

	select {
	case err := <-serveErr:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			fmt.Fprintf(stderr, "paw-egress-proxy: server error: %v\n", err)
			return 1
		}
		return 0
	case <-ctx.Done():
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownGrace)
	defer cancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		fmt.Fprintf(stderr, "paw-egress-proxy: shutdown error: %v\n", err)
	}
	return 0
}

// eventJSON mirrors egress.Event with omitempty tags for JSON logging.
type eventJSON struct {
	Host    string `json:"host,omitempty"`
	Port    int    `json:"port,omitempty"`
	Purpose string `json:"purpose,omitempty"`
	Outcome string `json:"outcome,omitempty"`
	Address string `json:"address,omitempty"`
}

func writeEventJSON(w io.Writer, e egress.Event) {
	line := eventJSON{
		Host:    e.Host,
		Port:    e.Port,
		Purpose: e.Purpose,
		Outcome: e.Outcome,
		Address: e.Address,
	}
	enc := json.NewEncoder(w)
	if err := enc.Encode(line); err != nil {
		fmt.Fprintf(os.Stderr, "paw-egress-proxy: failed to log event: %v\n", err)
	}
}
