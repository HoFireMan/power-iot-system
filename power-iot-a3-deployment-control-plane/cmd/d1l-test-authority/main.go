//go:build securitytesthelper

package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"time"

	"power-iot-a3-deployment-control-plane/internal/api"
	"power-iot-a3-deployment-control-plane/internal/provider"
	"power-iot-a3-deployment-control-plane/internal/store"
	"power-iot-a3-deployment-control-plane/migrations"
)

// This command is test-only. It is deliberately a small startup wrapper around
// the unchanged Provider authority/API so tests can receive a real listener
// barrier rather than infer readiness from pre-ListenAndServe logging.
func main() {
	ctx := context.Background()
	cfg, err := provider.LoadConfig(os.Getenv)
	if err != nil {
		log.Print(err)
		os.Exit(78)
	}
	s, err := store.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		log.Print("provider database unavailable")
		os.Exit(78)
	}
	defer s.Close()
	a := provider.New(s)
	if err = a.StartWithBootstrap(ctx, migrations.Bootstrap); err != nil {
		log.Print("authority startup rejected")
		os.Exit(78)
	}
	defer a.Stop()

	cert, err := tls.LoadX509KeyPair(cfg.TLSCertFile, cfg.TLSKeyFile)
	if err != nil {
		log.Print("provider TLS certificate unavailable")
		os.Exit(78)
	}
	caPEM, err := os.ReadFile(cfg.TLSCAFile)
	if err != nil {
		log.Print("provider TLS trust roots unavailable")
		os.Exit(78)
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(caPEM) {
		log.Print("provider TLS trust roots invalid")
		os.Exit(78)
	}
	listener, err := net.Listen("tcp", cfg.HTTPAddr)
	if err != nil {
		log.Print("provider listener unavailable")
		os.Exit(78)
	}
	defer listener.Close()
	tlsConfig := api.TLSConfig(&tls.Config{Certificates: []tls.Certificate{cert}, ClientCAs: roots, RootCAs: roots})
	srv := &http.Server{Handler: api.NewHandler(s, a).Routes(), ReadHeaderTimeout: 5 * time.Second, TLSConfig: tlsConfig}
	tlsListener := tls.NewListener(listener, tlsConfig)
	fmt.Fprintf(os.Stdout, "READY %s (epoch=%d)\n", listener.Addr().String(), a.Epoch())
	if err = srv.Serve(tlsListener); err != nil && err != http.ErrServerClosed {
		log.Print("authority stopped")
		os.Exit(1)
	}
}
