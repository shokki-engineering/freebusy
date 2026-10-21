// Loading and validating the embedded + overlay TOML at process start.
package config

import (
	"fmt"
	"os"

	"github.com/oh-tarnished/freebusy/shared"
	"github.com/the-protobuf-project/runtime-go/grpc/options"
)

// EnvPrefix starts every environment variable that overrides a configuration
// key: FREEBUSY_<SECTION>__<KEY>, double underscores between levels.
const EnvPrefix = "FREEBUSY_"

// loadedConfig holds the parsed configuration singleton.
var loadedConfig *AppConfig

func init() {
	// 1. Embedded production defaults (always present).
	data, err := configFS.ReadFile("freebusy.release.toml")
	if err != nil {
		panic(fmt.Sprintf("embedded config not found: %v", err))
	}
	if err := shared.LoadTomlBytes(data); err != nil {
		panic(fmt.Sprintf("failed to load embedded config: %v", err))
	}

	// 2. The development profile: config/freebusy.dev.toml from the working
	// directory when it is there (a checkout, so edits apply without a
	// rebuild), else the embedded copy when FREEBUSY_PROFILE=dev asks for it.
	// Keys it defines win; everything else falls through to the release
	// defaults.
	overlay, err := os.ReadFile(devOverlayPath)
	if err != nil {
		switch profile := os.Getenv(ProfileVar); profile {
		case "", "release":
			overlay = nil
		case "dev":
			if overlay, err = configFS.ReadFile("freebusy.dev.toml"); err != nil {
				panic(fmt.Sprintf("embedded dev profile not found: %v", err))
			}
		default:
			panic(fmt.Sprintf("%s=%q: want dev or release", ProfileVar, profile))
		}
	}
	if overlay != nil {
		if err := shared.LoadTomlBytes(overlay); err != nil {
			panic(fmt.Sprintf("failed to load the dev profile: %v", err))
		}
	}

	// 3. Environment variables, last so they win: how a container with nothing
	// but the binary in it is configured. FREEBUSY_DATABASE__POSTGRES__HOST=db.
	if err := shared.LoadEnv(EnvPrefix); err != nil {
		panic(fmt.Sprintf("failed to load environment overrides: %v", err))
	}

	loadedConfig = &AppConfig{}
	if err := shared.UnmarshalConfig(loadedConfig); err != nil {
		panic(fmt.Sprintf("failed to unmarshal config: %v", err))
	}

	if loadedConfig.Meta.Name == "" || loadedConfig.Meta.Version == "" {
		panic(fmt.Sprintf("config invalid: name=%q version=%q",
			loadedConfig.Meta.Name, loadedConfig.Meta.Version))
	}
	if !loadedConfig.Server.Environment.IsValid() {
		panic(fmt.Sprintf("config invalid: server.environment=%q (want development|debug|staging|production)",
			loadedConfig.Server.Environment))
	}
	if loadedConfig.Server.EnableMCP && !validMCPTransport(loadedConfig.Server.MCP.Transport) {
		panic(fmt.Sprintf("config invalid: server.mcp.transport=%q (want stdio|streamable-http|sse)",
			loadedConfig.Server.MCP.Transport))
	}
	if loadedConfig.Server.ExperimentalHTTP3 && !loadedConfig.Server.TLS.Enabled() {
		panic("config invalid: server.experimental_http3 requires server.tls.cert_file and server.tls.key_file")
	}
}

// validMCPTransport reports whether t is one of the supported MCP transports.
// MCPTransport carries no validity method of its own, so the check lives here.
func validMCPTransport(t options.MCPTransport) bool {
	switch t {
	case options.MCPTransportStdio, options.MCPTransportStreamableHTTP, options.MCPTransportSSE:
		return true
	default:
		return false
	}
}

// Get returns the loaded application configuration.
func Get() *AppConfig {
	if loadedConfig == nil {
		panic("configuration not initialized")
	}
	return loadedConfig
}
