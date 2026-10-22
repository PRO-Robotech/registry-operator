// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package config

import (
	"os"
	"strings"
	"testing"
)

const samplePath = "../../config/samples/operator-config.yaml"

func sample(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(samplePath)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestParse_SampleIsComplete(t *testing.T) {
	c, err := Parse([]byte(sample(t)))
	if err != nil {
		t.Fatalf("sample configuration refused: %v", err)
	}
	if c.Policy.ResourceNamePrefix != "zot-" || c.Intervals.NotReady.Duration == 0 {
		t.Fatalf("sample parsed incompletely: %+v", c)
	}
}

// Каждая порча одного значения — отказ старта с названным полем; исходный
// пример — законный близнец (проверен выше).
func TestParse_RefusesAnIncompleteOrUnsafeConfiguration(t *testing.T) {
	cases := []struct{ name, old, repl, want string }{
		{"unknown field", "chartVerification:\n", "chartVerification:\n  insecure: true\n", "unknown field"},
		{"empty name prefix", "resourceNamePrefix: zot-", "resourceNamePrefix: \"\"", "resourceNamePrefix"},
		{"destination without login", "destinationPath: namespaces/{login}/apps/docker-registry/{name}", "destinationPath: shared/{name}", "storage.destinationPath"},
		{"root role policy", "audience: vault", "audience: vault\n    rolePolicies: [root]", "secrets.rolePolicies"},
		{"role without an audience", "audience: vault", "audience: \"\"", "secrets.audience"},
		{"kv mount with a path", "kvMount: secret", "kvMount: secret/sub", "secrets.kvMount"},
		{"destination names the id", "destinationPath: namespaces/{login}/apps/docker-registry/{name}", "destinationPath: namespaces/{login}/apps/docker-registry/{name}-{id}", "storage.destinationPath"},
		{"template variable is an order variable", "templateVariables: [bucket,", "templateVariables: [public, bucket,", "templateVariables"},
		{"kaname without mutual TLS", "keyFile: /etc/registry-operator/kaname/tls.key", "keyFile: \"\"", "kaname.certFile"},
		{"kaname address without port", "address: kaname-internal.iam-system.svc:9091", "address: kaname-internal.iam-system.svc", "kaname.address"},
		{"enrollment without TLS", "certFile: /etc/registry-operator/enrollment/tls.crt", "certFile: \"\"", "enrollment.certFile"},
		{"enrollment without audience", "audience: registry-operator", "audience: \"\"", "enrollment.audience"},
		{"enrollment without a repository limit", "maxRepositories: 256", "maxRepositories: 0", "enrollment.maxRepositories"},
		{"enrollment without a rate", "ratePerMinute: 30", "ratePerMinute: 0", "enrollment.ratePerMinute"},
		{"no allowed roles", "allowedRoles: [registry.pusher, registry.puller]", "allowedRoles: []", "identity.allowedRoles"},
		{"no route namespace", "namespaces: [registry-edge]", "namespaces: []", "route: either namespaces"},
		{"no hostnames", "hostnames: [registry.example.test]", "hostnames: []", "route.hostnames"},
		{"create namespace allowed", "allowed: [ServerSideApply=true,", "allowed: [CreateNamespace=true, ServerSideApply=true,", "CreateNamespace"},
		{"chart pin without digest", "digest: sha256:acf4e9b3f3cace43e81cb7eae9c52553217f30b4578fd812ef55657286390304", "digest: latest", "charts[0]"},
		{"values prefix overlaps the order addons", "valuesAddonPrefix: registry-platform", "valuesAddonPrefix: reg-", "valuesAddonPrefix"},
		{"retry unbounded", "maxRetryLimit: 10", "maxRetryLimit: 0", "maxRetryLimit"},
		{"zero not-ready interval", "notReady: 15s", "notReady: 0s", "intervals.notReady"},
		{"recovery ceiling below its start", "maxDelay: 30m", "maxDelay: 10s", "syncRecovery.maxDelay"},
		{"chart verification without timeout", "timeout: 10s", "timeout: 0s", "chartVerification.timeout"},
	}
	src := sample(t)
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if !strings.Contains(src, c.old) {
				t.Fatalf("fixture edit does not apply: %q", c.old)
			}
			_, err := Parse([]byte(strings.Replace(src, c.old, c.repl, 1)))
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("want a refusal naming %q, got %v", c.want, err)
			}
		})
	}
}
