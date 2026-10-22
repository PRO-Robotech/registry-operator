// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

// Команда registry-operator — оператор заказов Registry: пайплайн ресурсов
// регистри, регистрация ресурсов в IAM как служба registry и эндпоинт
// заявок рабочей нагрузки на репозитории.
package main

import (
	"context"
	"crypto/tls"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"sync"
	"time"

	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/utils/clock"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	observedv1alpha1 "github.com/PRO-Robotech/registry-operator/api/observed/v1alpha1"
	registryv1alpha1 "github.com/PRO-Robotech/registry-operator/api/v1alpha1"
	"github.com/PRO-Robotech/registry-operator/internal/config"
	"github.com/PRO-Robotech/registry-operator/internal/controller"
	"github.com/PRO-Robotech/registry-operator/internal/enroll"
	"github.com/PRO-Robotech/registry-operator/internal/kaname"
	"github.com/PRO-Robotech/registry-operator/internal/ledger"
	"github.com/PRO-Robotech/registry-operator/internal/ocidigest"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "registry-operator:", err)
		os.Exit(1)
	}
}

func run() error {
	var (
		configPath, metricsAddr, probeAddr string
		leaderElect                        bool
		concurrency                        int
	)
	flag.StringVar(&configPath, "config", "/etc/registry-operator/config.yaml", "operator configuration")
	flag.StringVar(&metricsAddr, "metrics-bind-address", ":8080", "metrics listener")
	flag.StringVar(&probeAddr, "health-probe-bind-address", ":8081", "health probe listener")
	flag.BoolVar(&leaderElect, "leader-elect", true, "leader election for the reconcilers")
	flag.IntVar(&concurrency, "max-concurrent-reconciles", 4, "parallel reconciles of orders")
	opts := zap.Options{}
	opts.BindFlags(flag.CommandLine)
	flag.Parse()
	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&opts)))

	cfg, err := config.Load(configPath)
	if err != nil {
		return fmt.Errorf("refusing to start: %w", err)
	}
	scheme := runtime.NewScheme()
	for _, add := range []func(*runtime.Scheme) error{clientgoscheme.AddToScheme, registryv1alpha1.AddToScheme, observedv1alpha1.AddToScheme} {
		if err := add(scheme); err != nil {
			return err
		}
	}
	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		Scheme:                 scheme,
		Metrics:                metricsserver.Options{BindAddress: metricsAddr},
		HealthProbeBindAddress: probeAddr,
		LeaderElection:         leaderElect,
		LeaderElectionID:       "registry-operator.registry.in-cloud.io",
		Cache:                  cache.Options{ByObject: controller.CacheByObject()},
	})
	if err != nil {
		return err
	}
	kn, err := kaname.Dial(kaname.ClientConfig{
		Address: cfg.Kaname.Address, ServerName: cfg.Kaname.ServerName,
		CertFile: cfg.Kaname.CertFile, KeyFile: cfg.Kaname.KeyFile, CAFile: cfg.Kaname.CAFile,
		Timeout: cfg.Kaname.Timeout.Duration,
	})
	if err != nil {
		return err
	}
	defer func() { _ = kn.Close() }()

	cv := cfg.ChartVerification
	deps := &controller.Deps{
		Config:                  cfg,
		Verifier:                ocidigest.NewHTTPVerifier(cv.Timeout.Duration, cv.CacheTTL.Duration, cv.PlainHTTPHosts),
		Kaname:                  kn,
		Recorder:                mgr.GetEventRecorder("registry-operator"),
		Clock:                   clock.RealClock{},
		APIReader:               mgr.GetAPIReader(),
		MaxConcurrentReconciles: concurrency,
	}
	ctx := ctrl.SetupSignalHandler()
	if err := controller.Setup(ctx, mgr, deps); err != nil {
		return err
	}

	e := cfg.Enrollment
	srv := &enroll.Server{
		Reader:       mgr.GetAPIReader(),
		Ledger:       &ledger.Store{Client: mgr.GetClient(), Reader: mgr.GetAPIReader()},
		Kaname:       kn,
		Reviewer:     enroll.KubeTokenReviewer{Client: mgr.GetClient()},
		Audience:     e.Audience,
		WorkloadName: func(id string) string { return cfg.Policy.ResourceName(id, "") },
		Limits: enroll.Limits{
			MaxRepositories: e.MaxRepositories, RatePerMinute: e.RatePerMinute, Burst: e.Burst,
			EnrollerGrace: e.EnrollerGrace.Duration, RequestTimeout: e.RequestTimeout.Duration,
		},
		Now: time.Now,
		Log: slog.New(slog.NewJSONHandler(os.Stderr, nil)).With("component", "enrollment"),
	}
	certs := &servingCert{certFile: e.CertFile, keyFile: e.KeyFile}
	if _, err := certs.get(nil); err != nil {
		return fmt.Errorf("refusing to start: enrollment certificate: %w", err)
	}
	if err := mgr.Add(&enrollmentRunnable{server: &http.Server{
		Addr:              e.Listen,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      e.RequestTimeout.Duration + 5*time.Second,
		MaxHeaderBytes:    16 << 10,
		TLSConfig:         &tls.Config{MinVersion: tls.VersionTLS12, GetCertificate: certs.get},
	}}); err != nil {
		return err
	}
	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		return err
	}
	if err := mgr.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		return err
	}
	return mgr.Start(ctx)
}

// enrollmentRunnable — эндпоинт заявок на каждой реплике (не только у
// лидера): запись описи защищена версией объекта, а не единственностью
// писателя.
type enrollmentRunnable struct {
	server *http.Server
}

func (r *enrollmentRunnable) NeedLeaderElection() bool { return false }

func (r *enrollmentRunnable) Start(ctx context.Context) error {
	errc := make(chan error, 1)
	go func() { errc <- r.server.ListenAndServeTLS("", "") }()
	select {
	case <-ctx.Done():
		shutdown, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		return r.server.Shutdown(shutdown)
	case err := <-errc:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
}

// servingCert — серверный сертификат с диска, перечитываемый при смене
// (cert-manager ротирует его на месте).
type servingCert struct {
	certFile, keyFile string

	mu      sync.Mutex
	cert    *tls.Certificate
	modTime time.Time
}

func (s *servingCert) get(*tls.ClientHelloInfo) (*tls.Certificate, error) {
	st, err := os.Stat(s.certFile)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cert != nil && st.ModTime().Equal(s.modTime) {
		return s.cert, nil
	}
	c, err := tls.LoadX509KeyPair(s.certFile, s.keyFile)
	if err != nil {
		if s.cert != nil {
			return s.cert, nil
		}
		return nil, err
	}
	s.cert, s.modTime = &c, st.ModTime()
	return s.cert, nil
}
