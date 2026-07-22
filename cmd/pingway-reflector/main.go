// pingway-reflector: stateless UDP echo for the pingway call probe.
// Run it on a host outside the network under test; pingway senders fire
// RTP-shaped packets at it and measure what comes back.
//
// Safe for public operation: a HELLO→TOKEN handshake (HMAC of the
// observed source) means spoofed sources never receive echoes, replies
// are never larger than requests, and both per-source and global rate
// limits apply. See internal/callprobe for the protocol.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"syscall"

	"pingway.net/pingway/internal/callprobe"
)

var version = "dev"

func main() {
	listen := flag.String("listen", ":15000", "UDP address to listen on")
	maxPPS := flag.Int("max-pps", 20000, "global cap on echoed packets/sec")
	perIP := flag.Int("per-ip-pps", 120, "per-source cap on echoed packets/sec")
	flag.Parse()

	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	addr, err := net.ResolveUDPAddr("udp", *listen)
	if err != nil {
		fmt.Fprintln(os.Stderr, "fatal:", err)
		os.Exit(1)
	}
	conn, err := net.ListenUDP("udp", addr)
	if err != nil {
		fmt.Fprintln(os.Stderr, "fatal:", err)
		os.Exit(1)
	}
	srv, err := callprobe.NewReflectorServer(conn, *maxPPS, *perIP, log)
	if err != nil {
		fmt.Fprintln(os.Stderr, "fatal:", err)
		os.Exit(1)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	log.Info("pingway-reflector listening", "addr", *listen, "version", version,
		"max_pps", *maxPPS, "per_ip_pps", *perIP)
	if err := srv.Serve(ctx); err != nil {
		fmt.Fprintln(os.Stderr, "fatal:", err)
		os.Exit(1)
	}
	log.Info("pingway-reflector stopped")
}
