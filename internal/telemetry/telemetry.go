// Copyright 2026 Srikanth K.
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package shared holds freebusy's process-wide telemetry and configuration
// state. Importing it does nothing: the server builds the telemetry pipeline
// with Init at startup, and a program embedding freebusy as a library either
// hands it a pipeline of its own (Use) or runs without one.
package telemetry

import (
	_ "embed"
	"fmt"
	"net"
	"os"
	"strconv"
	"sync"

	"github.com/knadh/koanf/parsers/toml"
	"github.com/knadh/koanf/providers/rawbytes"
	"github.com/knadh/koanf/v2"
	"github.com/the-protobuf-project/runtime-go/observability"
	"github.com/the-protobuf-project/telemetry/telemetry-go"
	"github.com/the-protobuf-project/telemetry/telemetry-go/options"
)

//go:embed telemetry.toml
var telemetryConfigData []byte

var (
	// Telemetry is the process's telemetry pipeline: set by Init (the
	// server) or Use (a library caller), nil until then. Nil means none --
	// no spans, no metrics, no logs -- and every reader in freebusy allows
	// for it.
	Telemetry *telemetry.Telemetry
	once      sync.Once // once builds the server's pipeline at most once.
	initErr   error
)

// Init builds the server's telemetry pipeline from the embedded
// telemetry.toml and the FREEBUSY_ deployment overrides, makes it the
// process's (Telemetry), and has runtime-go's modules -- the gRPC server, its
// otelgrpc spans and gRPC metrics, the cache -- log, trace and measure through
// it instead of each standing up an exporter of its own. The server calls it
// first thing at startup; later calls return the first result.
func Init() error {
	once.Do(func() {
		b := telemetry.New()
		if tmpPath, err := writeTelemetryConfig(); err == nil {
			defer os.Remove(tmpPath) //nolint:errcheck
			b.WithConfig(tmpPath)
		} else {
			// No writable temp directory: a scratch or read-only container.
			// The library reads its configuration only from a file, so the
			// service is named in code, from the same embedded file, and the
			// rest is the library's defaults -- OTLP export included, which
			// FREEBUSY_OTLP_ENDPOINT turns on.
			name, version, description, err := telemetryService()
			if err != nil {
				initErr = fmt.Errorf("read embedded telemetry config: %w", err)
				return
			}
			b.WithService(name, version).WithDescription(description)
		}
		b.WithTracing()
		applyDeploymentOverrides(b)

		o, err := b.Build()
		if err != nil {
			initErr = fmt.Errorf("create telemetry: %w", err)
			return
		}
		Use(o)
		observability.Adopt(o)
	})
	return initErr
}

// Use makes t the process's telemetry pipeline: freebusy's RPC spans and
// metrics, its database instrumentation and its own log lines go through it.
// Nil turns them off.
func Use(t *telemetry.Telemetry) {
	Telemetry = t
}

// Info logs a line through the process's pipeline, if there is one. kv is
// key/value pairs, or a single map.
func Info(msg string, kv ...any) {
	if t := Telemetry; t != nil {
		t.Logger.Info(msg, fields(kv)...)
	}
}

// Debug logs a debug line through the process's pipeline, if there is one.
// kv is key/value pairs, or a single map.
func Debug(msg string, kv ...any) {
	if t := Telemetry; t != nil {
		t.Logger.Debug(msg, fields(kv)...)
	}
}

// Warn logs a warning through the process's pipeline, if there is one. kv is
// key/value pairs, or a single map.
func Warn(msg string, kv ...any) {
	if t := Telemetry; t != nil {
		t.Logger.Warn(msg, fields(kv)...)
	}
}

// Error logs an error through the process's pipeline, if there is one. kv is
// key/value pairs, or a single map.
func Error(msg string, kv ...any) {
	if t := Telemetry; t != nil {
		_ = t.Logger.Error(msg, fields(kv)...)
	}
}

// fields is kv as the pipeline's logger takes it: one value, of which it
// logs only the first. Key/value pairs become one map, so none is dropped;
// a single value is passed as it is, and an odd trailing value is kept
// under "extra".
func fields(kv []any) []any {
	if len(kv) <= 1 {
		return kv
	}
	m := make(map[string]any, (len(kv)+1)/2)
	for i := 0; i < len(kv); i += 2 {
		if i+1 == len(kv) {
			m["extra"] = kv[i]
			break
		}
		v := kv[i+1]
		if err, ok := v.(error); ok {
			v = err.Error()
		}
		m[fmt.Sprint(kv[i])] = v
	}
	return []any{m}
}

// deploymentEnvironment is the environment telemetry reports: the
// FREEBUSY_TELEMETRY_ENVIRONMENT override, else FREEBUSY_SERVER__ENVIRONMENT,
// else the configuration profile's -- "production" when the release defaults
// stand alone (FREEBUSY_PROFILE=release, or no development profile in the
// working directory), empty (telemetry.toml's "development") when the
// development profile applies. Without the profile check a release build said
// "development" in its banner and on every log line. config cannot be read
// here (it imports this package), so its profile rule is mirrored; see
// internal/config/load.go.
func deploymentEnvironment() string {
	if env := os.Getenv("FREEBUSY_TELEMETRY_ENVIRONMENT"); env != "" {
		return env
	}
	if env := os.Getenv("FREEBUSY_SERVER__ENVIRONMENT"); env != "" {
		return env
	}
	switch os.Getenv("FREEBUSY_PROFILE") {
	case "release":
		return "production"
	case "dev":
		return ""
	}
	if _, err := os.Stat("internal/config/freebusy.dev.toml"); err != nil {
		return "production"
	}
	return ""
}

// applyDeploymentOverrides layers per-deployment values over the embedded
// telemetry.toml defaults, so one binary serves every environment and
// deployment without a rebuild. Each override is optional — an unset
// variable leaves the embedded default in place.
//
//	FREEBUSY_TELEMETRY_ENVIRONMENT  deployment environment (development|staging|production)
//	FREEBUSY_OTLP_ENDPOINT          OTLP collector as host:port
//	FREEBUSY_OTLP_SECURE            true when that collector serves TLS; unset, TLS
//	                                is on only for a remote host on 443 or 4318
//	FREEBUSY_SITE                   label slug for what this deployment serves (e.g. "riverside-club")
//	FREEBUSY_SITE_NAME              human-readable name for it
//	FREEBUSY_TELEMETRY_INSTANCE_ID  service.instance.id; unset, the host name
//	                                (a pod's name in Kubernetes)
//
// The resource also carries host.name, and whatever OTEL_RESOURCE_ATTRIBUTES
// lists (the SDK's resource.Default reads it): the Helm chart passes the pod's
// k8s.pod.name, k8s.namespace.name and k8s.node.name through it. Without an
// instance id, replicas exporting the same service.name overwrite each other's
// metric series.
func applyDeploymentOverrides(b *telemetry.Builder) {
	if env := deploymentEnvironment(); env != "" {
		b.WithEnvironment(options.Environment(env))
	}
	if endpoint := os.Getenv("FREEBUSY_OTLP_ENDPOINT"); endpoint != "" {
		if host, port, ok := splitHostPort(endpoint); ok {
			b.WithOTLP(host, port)
		} else {
			fmt.Printf("WARNING: ignoring malformed FREEBUSY_OTLP_ENDPOINT %q (want host:port)\n", endpoint)
		}
	}
	if secure, err := strconv.ParseBool(os.Getenv("FREEBUSY_OTLP_SECURE")); err == nil {
		b.WithOTLPSecure(secure)
	}
	labels := instanceLabels()
	if v := os.Getenv("FREEBUSY_SITE"); v != "" {
		labels["site"] = v
	}
	if v := os.Getenv("FREEBUSY_SITE_NAME"); v != "" {
		labels["site_name"] = v
	}
	if len(labels) > 0 {
		b.WithLabels(labels)
	}
}

// instanceLabels returns the resource attributes that tell one running
// process from another: service.instance.id (FREEBUSY_TELEMETRY_INSTANCE_ID,
// else the host name) and host.name.
func instanceLabels() map[string]string {
	labels := map[string]string{}
	host, _ := os.Hostname()
	if host != "" {
		labels["host.name"] = host
	}
	id := os.Getenv("FREEBUSY_TELEMETRY_INSTANCE_ID")
	if id == "" {
		id = host
	}
	if id != "" {
		labels["service.instance.id"] = id
	}
	return labels
}

// splitHostPort splits a "host:port" OTLP endpoint into its parts.
func splitHostPort(endpoint string) (host string, port int, ok bool) {
	h, portStr, err := net.SplitHostPort(endpoint)
	if err != nil {
		return "", 0, false
	}
	p, err := strconv.Atoi(portStr)
	if err != nil {
		return "", 0, false
	}
	return h, p, true
}

// writeTelemetryConfig writes the embedded telemetry.toml to a temporary
// file and returns its path. The caller is responsible for cleanup.
func writeTelemetryConfig() (string, error) {
	f, err := os.CreateTemp("", "telemetry-*.toml")
	if err != nil {
		return "", fmt.Errorf("create temp file: %w", err)
	}
	if _, err := f.Write(telemetryConfigData); err != nil {
		_ = f.Close()
		_ = os.Remove(f.Name())
		return "", fmt.Errorf("write telemetry config: %w", err)
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(f.Name())
		return "", fmt.Errorf("close temp file: %w", err)
	}
	return f.Name(), nil
}

// telemetryService reads the service's identity from the embedded
// telemetry.toml, for when the library cannot be handed the file.
func telemetryService() (name, version, description string, err error) {
	k := koanf.New(".")
	if err := k.Load(rawbytes.Provider(telemetryConfigData), toml.Parser()); err != nil {
		return "", "", "", err
	}
	return k.String("service.name"), k.String("service.version"), k.String("service.description"), nil
}

// Close releases resources held by the Telemetry client.
// It is safe to call even when Telemetry was never initialised.
// The caller (typically main) should invoke this on application shutdown.
func Close() error {
	if Telemetry != nil {
		return Telemetry.Close()
	}
	return nil
}
