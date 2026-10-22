// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

// Команда fakekaname — двойник kaname для проб на kind: внутренний
// листенер (mTLS, RegisterResource/UnregisterResource), публичный
// AuthorizeService.Check и отладочный HTTP для пробы (зеркало, журнал
// вызовов, выдачи, токены). В посадку не входит: образ собирается только
// заданием kind-e2e.
package main

import (
	"crypto/tls"
	"encoding/json"
	"flag"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/PRO-Robotech/registry-operator/internal/kaname/kanametest"
	"github.com/PRO-Robotech/registry-operator/internal/tlsfile"
)

func main() {
	var internal, public, debug, cert, key, clientCA, san string
	flag.StringVar(&internal, "internal-listen", ":9091", "internal listener")
	flag.StringVar(&public, "public-listen", ":9090", "public listener")
	flag.StringVar(&debug, "debug-listen", ":8080", "debug listener")
	flag.StringVar(&cert, "cert", "/certs/tls.crt", "server certificate")
	flag.StringVar(&key, "key", "/certs/tls.key", "server key")
	flag.StringVar(&clientCA, "client-ca", "/certs/ca.crt", "CA of client leaves")
	flag.StringVar(&san, "allowed-san", "", "URI SAN allowed to write")
	flag.Parse()

	pair, err := tls.LoadX509KeyPair(cert, key)
	if err != nil {
		log.Fatal(err)
	}
	pool, err := tlsfile.CAPool(clientCA)
	if err != nil {
		log.Fatal(err)
	}
	f := kanametest.New(san)
	srv, err := f.ServeOn(pair, pool, internal, public)
	if err != nil {
		log.Fatal(err)
	}
	defer srv.Stop()

	mux := http.NewServeMux()
	mux.HandleFunc("GET /objects", func(w http.ResponseWriter, _ *http.Request) { _ = json.NewEncoder(w).Encode(f.Objects()) })
	mux.HandleFunc("GET /calls", func(w http.ResponseWriter, _ *http.Request) { _ = json.NewEncoder(w).Encode(f.Calls()) })
	mux.HandleFunc("GET /erased", func(w http.ResponseWriter, _ *http.Request) { _ = json.NewEncoder(w).Encode(f.Erased()) })
	mux.HandleFunc("POST /grants", func(w http.ResponseWriter, r *http.Request) {
		var g struct {
			Subject, Role, Registry, Account string
		}
		if err := json.NewDecoder(r.Body).Decode(&g); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		role, ok := map[string]kanametest.Role{"puller": kanametest.RolePuller, "pusher": kanametest.RolePusher, "admin": kanametest.RoleAdmin}[g.Role]
		if !ok {
			http.Error(w, "unknown role", http.StatusBadRequest)
			return
		}
		f.AddGrant(kanametest.Grant{Subject: g.Subject, Role: role, Registry: g.Registry, Account: g.Account})
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("POST /tokens", func(w http.ResponseWriter, r *http.Request) {
		var t struct{ Token, Subject string }
		if err := json.NewDecoder(r.Body).Decode(&t); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		f.AddToken(t.Token, t.Subject)
		w.WriteHeader(http.StatusNoContent)
	})
	d := &http.Server{Addr: debug, Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() {
		if err := d.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatal(err)
		}
	}()
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGTERM, syscall.SIGINT)
	<-sig
}
