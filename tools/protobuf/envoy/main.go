// Copyright 2026 Srikanth K.
// SPDX-License-Identifier: AGPL-3.0-or-later

// Command envoy generates the Envoy grpc-web proxy config (envoy/launch.yaml)
// from the generated per-service OpenAPI specs under protobuf/generated/openapi.
//
// freebusy serves every gRPC service from a single backend, so the proxy needs
// one route per gRPC service — each matching the service's wire prefix
// "/<package>.<Service>/" — all pointing at one cluster. The merged
// openapiv3-spec.yaml only carries REST paths, not gRPC service names, so this
// tool reads the per-service *_service.openapi.yaml files instead: the package
// comes from each file's directory tree (freebusy/<domain>/v1 -> freebusy.<domain>.v1)
// and the service name from its operationId prefix ("<Service>_<Method>").
//
// Usage:
//
//	go run ./tools/protobuf/envoy [spec-dir] [flags]
//
// spec-dir defaults to protobuf/generated/openapi under the repo root; the
// output defaults to envoy/launch.yaml.
package main

import (
	_ "embed"
	"flag"
	"os"
	"path/filepath"
)

//go:embed launch.yaml.tmpl
var launchTemplate string

var verbose bool

// service is one gRPC service to route, e.g. freebusy.promocode.v1.PromoCodeService.
type service struct {
	Package string // freebusy.promocode.v1
	Name    string // PromoCodeService
	Prefix  string // /freebusy.promocode.v1.PromoCodeService/
}

// model is the data the launch.yaml template renders against.
type model struct {
	Source      string
	ListenPort  int
	AdminPort   int
	ClusterName string
	BackendHost string
	BackendPort int
	Timeout     string
	Services    []service

	// DNSLookupFamily pins address-family resolution for the backend cluster.
	// STRICT_DNS defaults to AUTO, which prefers AAAA — inside Docker that
	// resolves host.docker.internal to a non-routable IPv6 and every request
	// fails with "Network is unreachable". V4_ONLY is the working default.
	DNSLookupFamily string

	// BackendTLS dials the backend over TLS. The freebusy server terminates TLS
	// on its gRPC port, so a plaintext upstream is an immediate 503 (UF/UPE).
	BackendTLS bool

	// BackendSNI is the server name presented upstream. It must match a SAN on
	// the backend's certificate — the mkcert dev cert carries `freebusy-dev`,
	// NOT `host.docker.internal`, so the dialled host and the SNI differ.
	BackendSNI string
}

// serviceDoc captures just the operationIds from a per-service OpenAPI file;
// every other field is ignored.
type serviceDoc struct {
	Paths map[string]map[string]struct {
		OperationID string `yaml:"operationId"`
	} `yaml:"paths"`
}

func main() {
	root := repoRoot()
	defaultSpecDir := filepath.Join(root, "protobuf", "generated", "openapi")
	defaultOutput := filepath.Join(root, "docker/envoy", "launch.yaml")

	var (
		output      string
		listenPort  int
		adminPort   int
		backendHost string
		backendPort int
		cluster     string
		timeout     string
		dnsFamily   string
		backendTLS  bool
		backendSNI  string
	)
	flag.StringVar(&output, "output", defaultOutput, "Path to write the Envoy config")
	flag.StringVar(&output, "o", defaultOutput, "Path to write the Envoy config (shorthand)")
	flag.IntVar(&listenPort, "listen-port", 8080, "Port the grpc-web listener binds")
	flag.IntVar(&adminPort, "admin-port", 9901, "Port the Envoy admin UI binds")
	flag.StringVar(&backendHost, "backend-host", "host.docker.internal",
		"Hostname of the freebusy gRPC backend (use 127.0.0.1 when running Envoy natively)")
	flag.IntVar(&backendPort, "backend-port", 50051, "Port of the freebusy gRPC backend")
	flag.StringVar(&cluster, "cluster", "freebusy_backend", "Name of the backend cluster")
	flag.StringVar(&timeout, "timeout", "60s", "Per-route upstream timeout")
	flag.StringVar(&dnsFamily, "dns-lookup-family", "V4_ONLY",
		"Address family for backend DNS (V4_ONLY avoids the unroutable Docker IPv6)")
	flag.BoolVar(&backendTLS, "backend-tls", true,
		"Dial the backend over TLS (the freebusy gRPC port terminates TLS)")
	flag.StringVar(&backendSNI, "backend-sni", "freebusy-dev",
		"SNI/server name presented upstream; must match a SAN on the backend certificate")
	flag.BoolVar(&verbose, "verbose", false, "Enable verbose (debug) logging")
	flag.BoolVar(&verbose, "v", false, "Enable verbose (debug) logging (shorthand)")
	flag.Parse()

	specDir := defaultSpecDir
	if flag.NArg() > 0 {
		specDir = flag.Arg(0)
	}
	specDir, _ = filepath.Abs(specDir)
	if fi, err := os.Stat(specDir); err != nil || !fi.IsDir() {
		fatalf("spec directory does not exist: %s", specDir)
	}

	services := collectServices(specDir)
	if len(services) == 0 {
		fatalf("no gRPC services found under %s (expected *_service.openapi.yaml files)", specDir)
	}

	source := "protobuf/generated/openapi/**/*_service.openapi.yaml"
	if rel, err := filepath.Rel(root, specDir); err == nil {
		source = filepath.ToSlash(filepath.Join(rel, "**", "*_service.openapi.yaml"))
	}

	data := model{
		Source:      source,
		ListenPort:  listenPort,
		AdminPort:   adminPort,
		ClusterName: cluster,
		BackendHost: backendHost,
		BackendPort: backendPort,
		Timeout:     timeout,
		Services:    services,

		DNSLookupFamily: dnsFamily,
		BackendTLS:      backendTLS,
		BackendSNI:      backendSNI,
	}

	output, _ = filepath.Abs(output)
	if err := render(output, data); err != nil {
		fatalf("write output: %v", err)
	}
	logf("INFO", "envoy config written to %s (%d services -> cluster %q)", output, len(services), cluster)
}
