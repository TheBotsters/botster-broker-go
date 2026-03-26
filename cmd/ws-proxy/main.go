// ws-proxy is the long-lived WebSocket frontend for the broker process split.
//
// It accepts WebSocket connections from brains and actuators, delegates
// authentication to the broker process over a Unix domain socket, and
// forwards messages bidirectionally. Ping/pong keepalive is handled
// autonomously — broker restarts do not drop WebSocket connections.
//
// Usage:
//
//	ws-proxy [flags]
//	  -socket string  Unix socket path (default "/run/botster-broker/hub.sock")
//	  -listen string  WebSocket listen address (default ":9084")
//	  -auth-timeout duration  Auth delegation timeout (default 5s)
//	  -buffer-size int  Max buffered messages during broker restart (default 1000)
package main

import (
	"flag"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/TheBotsters/botster-broker-go/internal/proxy"
)

func main() {
	log.SetFlags(log.Ldate | log.Ltime | log.Lshortfile)

	socketPath := flag.String("socket", "/run/botster-broker/hub.sock", "Unix socket path for broker link")
	listenAddr := flag.String("listen", ":9084", "WebSocket listen address")
	authTimeout := flag.Duration("auth-timeout", 5*time.Second, "Auth delegation timeout")
	bufferSize := flag.Int("buffer-size", 1000, "Max buffered messages during broker restart")
	flag.Parse()

	cfg := proxy.Config{
		SocketPath:     *socketPath,
		WSListenAddr:   *listenAddr,
		AuthTimeout:    *authTimeout,
		BufferSize:     *bufferSize,
		BufferMaxBytes: 10 << 20, // 10MB
	}

	p := proxy.New(cfg)

	// Start Unix socket listener for broker connections.
	go listenForBroker(p, cfg.SocketPath)

	// HTTP server for WebSocket and health.
	mux := http.NewServeMux()
	mux.HandleFunc("/ws", p.HandleWebSocket)
	mux.HandleFunc("/health", p.HealthHandler())

	srv := &http.Server{Addr: cfg.WSListenAddr, Handler: mux}

	log.Printf("[ws-proxy] listening on %s (socket: %s)", cfg.WSListenAddr, cfg.SocketPath)

	// Graceful shutdown on signal.
	go func() {
		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
		sig := <-sigCh
		log.Printf("[ws-proxy] shutting down (signal: %s)", sig)
		_ = srv.Close()
		_ = os.Remove(cfg.SocketPath)
	}()

	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatalf("[ws-proxy] server error: %v", err)
	}
}

// listenForBroker listens on the Unix socket for broker connections.
// When a broker connects, it's handled by the proxy. When the connection
// drops (broker restart), the proxy buffers messages until the next
// broker connects.
func listenForBroker(p *proxy.Proxy, socketPath string) {
	// Clean up stale socket file.
	_ = os.Remove(socketPath)

	ln, err := net.Listen("unix", socketPath)
	if err != nil {
		log.Fatalf("[ws-proxy] failed to listen on %s: %v", socketPath, err)
	}
	defer ln.Close()

	// Make socket group-readable so the broker process can connect.
	_ = os.Chmod(socketPath, 0660)

	log.Printf("[ws-proxy] Unix socket ready at %s", socketPath)

	for {
		conn, err := ln.Accept()
		if err != nil {
			log.Printf("[ws-proxy] accept error: %v", err)
			continue
		}
		// Only one broker at a time. HandleLinkConnection blocks until
		// the broker disconnects, then we accept the next one.
		p.HandleLinkConnection(conn)
	}
}
