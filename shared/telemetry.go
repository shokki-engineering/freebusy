// Copyright 2026 Srikanth K.
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package shared exposes process-wide singletons used across the freebusy service.
package shared

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
	Telemetry *telemetry.Telemetry // Telemetry is the singleton client initialised at package load time.
	once      sync.Once            // once is used to ensure that the client is initialised only once.
)

func init() {
	once.Do(func() {
		routeOTLPEndpoint()
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
				panic(fmt.Sprintf("failed to read embedded telemetry config: %v", err))
			}
			b.WithService(name, version).WithDescription(description)
		}
		b.WithTracing()
		applyDeploymentOverrides(b)

		o, err := b.Build()
		if err != nil {
			fmt.Printf("ERROR: Failed to create Telemetry: %v\n", err)
			panic(err)
		}

		Telemetry = o
		// The process's one pipeline: runtime-go's modules (the gRPC server,
		// its otelgrpc spans and gRPC metrics, the cache) log, trace and
		// measure through it instead of each standing up an exporter of its
		// own under its own service name.
		observability.Adopt(o)
	})
}

// applyDeploymentOverrides layers per-deployment values over the embedded
// telemetry.toml defaults, so one binary serves every environment and
// deployment without a rebuild. Each override is optional — an unset
// variable leaves the embedded default in place.
//
//	FREEBUSY_TELEMETRY_ENVIRONMENT  deployment environment (development|staging|production)
//	FREEBUSY_OTLP_ENDPOINT          OTLP collector as host:port (see routeOTLPEndpoint)
//	FREEBUSY_OTLP_SECURE            true when that collector serves TLS
//	FREEBUSY_SITE                   label slug for what this deployment serves (e.g. "riverside-club")
//	FREEBUSY_SITE_NAME              human-readable name for it
func applyDeploymentOverrides(b *telemetry.Builder) {
	if env := os.Getenv("FREEBUSY_TELEMETRY_ENVIRONMENT"); env != "" {
		b.WithEnvironment(options.Environment(env))
	}
	labels := map[string]string{}
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

// routeOTLPEndpoint hands FREEBUSY_OTLP_ENDPOINT and FREEBUSY_OTLP_SECURE to
// the library as its own endpoint and TLS settings (its TELEMETRY_ variables),
// which it reads when the builder loads its configuration. The builder's
// WithOTLP would take the endpoint too, but it turns TLS on for every host
// name, so a plaintext collector beside the server -- telemetry:6009 in the
// Compose stack, a collector Service in a cluster -- received nothing.
func routeOTLPEndpoint() {
	endpoint := os.Getenv("FREEBUSY_OTLP_ENDPOINT")
	if endpoint == "" {
		return
	}
	if _, _, ok := splitHostPort(endpoint); !ok {
		fmt.Printf("WARNING: ignoring malformed FREEBUSY_OTLP_ENDPOINT %q (want host:port)\n", endpoint)
		return
	}
	_ = os.Setenv("TELEMETRY_TELEMETRY_OTLP_ENABLED", "true")
	_ = os.Setenv("TELEMETRY_TELEMETRY_OTLP_ENDPOINT", endpoint)
	_ = os.Setenv("TELEMETRY_TELEMETRY_OTLP_SECURE", strconv.FormatBool(os.Getenv("FREEBUSY_OTLP_SECURE") == "true"))
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
