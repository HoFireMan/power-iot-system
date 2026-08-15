package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"log"
	"net/http"
	"os"
	"time"

	"power-iot-a3-deployment-control-plane/internal/api"
	"power-iot-a3-deployment-control-plane/internal/provider"
	"power-iot-a3-deployment-control-plane/internal/store"
	"power-iot-a3-deployment-control-plane/migrations"
)

func main() {
	ctx := context.Background()
	cfg, e := provider.LoadConfig(os.Getenv)
	if e != nil {
		log.Print(e)
		os.Exit(78)
	}
	s, e := store.Open(ctx, cfg.DatabaseURL)
	if e != nil {
		log.Print("provider database unavailable")
		os.Exit(78)
	}
	defer s.Close()
	a := provider.New(s)
	if e = a.StartWithBootstrap(ctx, migrations.Bootstrap); e != nil {
		log.Print("authority startup rejected")
		os.Exit(78)
	}
	defer a.Stop()
	go a.Monitor(ctx)
	addr := cfg.HTTPAddr
	if addr == "" {
		addr = ":8443"
	}
	certFile, keyFile, caFile := cfg.TLSCertFile, cfg.TLSKeyFile, cfg.TLSCAFile
	if certFile == "" || keyFile == "" || caFile == "" {
		log.Print("provider TLS certificate, key and trust-root files are required")
		os.Exit(78)
	}
	cert, e := tls.LoadX509KeyPair(certFile, keyFile)
	if e != nil {
		log.Print("provider TLS certificate unavailable")
		os.Exit(78)
	}
	caPEM, e := os.ReadFile(caFile)
	if e != nil {
		log.Print("provider TLS trust roots unavailable")
		os.Exit(78)
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(caPEM) {
		log.Print("provider TLS trust roots invalid")
		os.Exit(78)
	}
	tlsConfig := api.TLSConfig(&tls.Config{Certificates: []tls.Certificate{cert}, ClientCAs: roots, RootCAs: roots})
	srv := &http.Server{Addr: addr, Handler: api.NewHandler(s, a).Routes(), ReadHeaderTimeout: 5 * time.Second, TLSConfig: tlsConfig}
	log.Printf("d1l provider authority listening on %s (epoch=%d)", addr, a.Epoch())
	if e = srv.ListenAndServeTLS("", ""); e != nil && e != http.ErrServerClosed {
		log.Print("authority stopped")
		os.Exit(1)
	}
}
