// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

// Команда iam-proxy — прокси прав перед zot одного регистри (контейнер
// пода zot-<id>).
package main

import (
	"context"
	"crypto/tls"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"

	"github.com/PRO-Robotech/registry-operator/internal/iamproxy"
	"github.com/PRO-Robotech/registry-operator/internal/tlsfile"
)

func main() {
	if err := run(); err != nil {
		slog.Error("iam-proxy stopped", "err", err)
		os.Exit(1)
	}
}

func run() error {
	var cfg iamproxy.Config
	fs := flag.NewFlagSet("iam-proxy", flag.ContinueOnError)
	cfg.BindFlags(fs)
	if err := fs.Parse(os.Args[1:]); err != nil {
		return err
	}
	if err := cfg.Validate(); err != nil {
		return fmt.Errorf("refusing to start: %w", err)
	}
	log := slog.New(slog.NewJSONHandler(os.Stderr, nil))

	jwksClient, err := iamproxy.TLSClient(cfg.JWKSCAFile, 10*time.Second)
	if err != nil {
		return err
	}
	verifier, err := iamproxy.NewVerifier(iamproxy.TokenConfig{
		JWKSURL: cfg.JWKSURL, HTTPClient: jwksClient, Issuer: cfg.Issuer, Audience: cfg.Audience,
		Algorithms: []string{"ES256"}, Types: cfg.Types(),
		KeysTTL: 5 * time.Minute, RefreshEvery: 30 * time.Second, Leeway: 30 * time.Second,
	})
	if err != nil {
		return err
	}
	pool, err := tlsfile.CAPool(cfg.KanameCAFile)
	if err != nil {
		return err
	}
	conn, err := grpc.NewClient(cfg.KanameAddress, grpc.WithTransportCredentials(credentials.NewTLS(&tls.Config{
		MinVersion: tls.VersionTLS12, RootCAs: pool, ServerName: cfg.KanameServerName,
	})))
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()
	checker := &iamproxy.CachedChecker{
		Inner: iamproxy.NewKanameChecker(conn, cfg.CheckTimeout), TTL: cfg.CacheTTL, MaxEntries: 16384, Now: time.Now,
	}
	enrollClient, err := iamproxy.TLSClient(cfg.EnrollCAFile, cfg.EnrollTimeout)
	if err != nil {
		return err
	}
	enroller, err := iamproxy.NewOperatorEnroller(cfg.EnrollURL, cfg.RegistryID, cfg.EnrollTokenFile, enrollClient)
	if err != nil {
		return err
	}
	upstream, err := url.Parse(cfg.Upstream)
	if err != nil {
		return err
	}

	reg := prometheus.NewRegistry()
	reg.MustRegister(collectors.NewGoCollector(), collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}))
	decisions := prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "registry_iam_proxy_decisions_total",
		Help: "Authorization decisions of the registry proxy, by action and outcome.",
	}, []string{"action", "outcome"})
	reg.MustRegister(decisions)

	proxy, err := iamproxy.New(iamproxy.Options{
		RegistryID: cfg.RegistryID, Public: cfg.Public, Upstream: upstream,
		Verifier: verifier, Checker: checker, Enroller: enroller, Log: log,
		Decisions: func(action, outcome string) { decisions.WithLabelValues(action, outcome).Inc() },
	})
	if err != nil {
		return err
	}

	health := http.NewServeMux()
	health.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	health.Handle("/readyz", iamproxy.Readiness(verifier, upstream, cfg.UpstreamReadyPath, &http.Client{}))
	health.Handle("/metrics", promhttp.HandlerFor(reg, promhttp.HandlerOpts{}))

	servers := []*http.Server{
		{Addr: cfg.Listen, Handler: proxy, ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 120 * time.Second, MaxHeaderBytes: 64 << 10},
		{Addr: cfg.HealthListen, Handler: health, ReadHeaderTimeout: 5 * time.Second},
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()
	errc := make(chan error, len(servers))
	for _, s := range servers {
		go func() {
			if err := s.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
				errc <- err
			}
		}()
	}
	log.Info("iam-proxy started", "registry", cfg.RegistryID, "public", cfg.Public)
	select {
	case <-ctx.Done():
	case err := <-errc:
		return err
	}
	shutdown, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	for _, s := range servers {
		_ = s.Shutdown(shutdown)
	}
	return nil
}
