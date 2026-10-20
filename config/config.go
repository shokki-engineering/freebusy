// Package config loads the freebusy service configuration from an embedded TOML
// file (freebusy.release.toml) and exposes it via Get(). The release file is
// compiled into the binary with //go:embed and holds production-safe defaults
// (secrets left blank, to be injected at deploy time). On developer machines an
// optional, non-embedded config/freebusy.dev.toml is layered on top, so local
// runs need no environment variables. Configuration is the single source of
// truth for service identity and database connectivity.
package config

import (
	"embed"
	"fmt"
	"time"

	"github.com/the-protobuf-project/runtime-go/grpc/options"
)

//go:embed freebusy.release.toml
var configFS embed.FS

// devOverlayPath is the optional local-development config layered over the
// embedded production defaults. It is read from disk (relative to the working
// directory) only when present, so production builds — which ship just the
// binary — never load it.
const devOverlayPath = "config/freebusy.dev.toml"

// AppConfig is the top-level configuration tree.
type AppConfig struct {
	// Meta holds service identity metadata.
	Meta AppMeta `koanf:"app"`
	// Server holds the hybrid gRPC/HTTP/MCP server settings.
	Server ServerConfig `koanf:"server"`
	// Database holds the backend selection and per-provider connection settings.
	Database DatabaseConfig `koanf:"database"`
	// Seed holds dev-only startup seeding. Disabled in the release defaults; the
	// dev overlay turns it on to plant a known organisation.
	Seed SeedConfig `koanf:"seed"`
	// Cache holds the read-through cache backend. Disabled by default: the
	// service is correct without it, and a cache that is on before anyone has
	// measured the read it serves is just another thing that can be stale.
	Cache CacheConfig `koanf:"cache"`
}

// CacheConfig is the backend behind protoc-gen-cache's generated decorators.
//
// The backend is deliberately absent from the protos: cache.v1 names strategies,
// never drivers, so which server serves them is deployment policy and lives here.
// It matters which one, though — resources annotated STRATEGY_INDEXED need
// server-side sets, so Redis or Dragonfly. memcached has no sets and the
// generated constructor refuses at startup rather than failing every lookup
// later.
type CacheConfig struct {
	// Enabled turns the cache on. Off means repositories read straight through
	// to the database, which is always a valid configuration.
	Enabled bool `koanf:"enabled"`
	// Address is the Redis/Dragonfly server, host:port.
	Address string `koanf:"address"`
	// Username and Password authenticate, when the server asks.
	Username string `koanf:"username"`
	Password string `koanf:"password"`
	// Database is the Redis database index this service binds to.
	Database int `koanf:"database"`
	// PoolSize caps concurrent connections. Zero takes the driver's default.
	PoolSize int `koanf:"pool_size"`
	// Prefix namespaces every key, separating this deployment's entries from
	// another's sharing one server. It is deliberately not a proto annotation:
	// a prefix separates deployments, and an annotation travels with the schema.
	Prefix string `koanf:"prefix"`
}

// SeedConfig controls the dev-only data planted at startup. It exists so a
// freshly rebuilt database has a known organisation to hang properties and
// units off, instead of a first-run that starts with nothing to reference.
// Never enabled in the release defaults — production data is not seeded.
type SeedConfig struct {
	// Enabled gates all startup seeding. False (the release default) is a no-op.
	Enabled bool `koanf:"enabled"`
	// Organisation is the organisation planted when Enabled. Seeding is
	// idempotent on its ID, so restarts reuse the same organisation.
	Organisation SeedOrganisation `koanf:"organisation"`
}

// SeedOrganisation is the organisation the seeder ensures exists.
type SeedOrganisation struct {
	// ID is the caller-chosen final segment of organisations/{id}; a stable value
	// makes the resource name predictable across restarts and rebuilds.
	ID string `koanf:"id"`
	// DisplayName is the human-friendly name (e.g. "Machani Group").
	DisplayName string `koanf:"display_name"`
	// Slug is the URL-safe identifier.
	Slug string `koanf:"slug"`
	// BillingEmail is the billing contact address.
	BillingEmail string `koanf:"billing_email"`
}

// AppMeta contains service identity fields.
type AppMeta struct {
	// Name is the service display name.
	Name string `koanf:"name"`
	// Version is the service version (e.g. "v1.0.0").
	Version string `koanf:"version"`
	// Description is the long-form description shown to clients and MCP.
	Description string `koanf:"description"`
}

// ServerConfig holds the hybrid gRPC/HTTP/MCP server settings. GetGRPCOptions
// (options.go) maps these onto the runtime's options.Options.
type ServerConfig struct {
	// Environment is the operating mode: development, debug, staging, or production.
	Environment options.ServerEnvironment `koanf:"environment"`
	// EnableHTTP starts the HTTP/JSON gateway alongside gRPC.
	EnableHTTP bool `koanf:"enable_http"`
	// EnableHealth registers the standard gRPC health-check service.
	EnableHealth bool `koanf:"enable_health"`
	// EnableMCP starts the Model Context Protocol server.
	EnableMCP bool `koanf:"enable_mcp"`
	// ExperimentalHTTP3 enables experimental HTTP/3 on the HTTP port + 1. It
	// requires TLS (see TLS below).
	ExperimentalHTTP3 bool `koanf:"experimental_http3"`
	// TLS holds the certificate/key pair. When both paths are set, TLS is enabled
	// for gRPC and HTTP; it is required for ExperimentalHTTP3.
	TLS TLSConfig `koanf:"tls"`
	// GRPC is the gRPC listener host/port.
	GRPC ListenConfig `koanf:"grpc"`
	// HTTP is the HTTP gateway listener host/port.
	HTTP ListenConfig `koanf:"http"`
	// MCP is the MCP server listener and transport.
	MCP MCPConfig `koanf:"mcp"`
}

// TLSConfig is the TLS certificate/key pair. Leave both paths empty to serve
// plaintext; set both to enable TLS (and, with ExperimentalHTTP3, HTTP/3).
type TLSConfig struct {
	// CertFile is the path to the PEM-encoded certificate.
	CertFile string `koanf:"cert_file"`
	// KeyFile is the path to the PEM-encoded private key.
	KeyFile string `koanf:"key_file"`
}

// Enabled reports whether both a certificate and key are configured.
func (t TLSConfig) Enabled() bool { return t.CertFile != "" && t.KeyFile != "" }

// ListenConfig is a network host/port pair.
type ListenConfig struct {
	// Host is the interface to bind (e.g. "0.0.0.0").
	Host string `koanf:"host"`
	// Port is the TCP port to listen on.
	Port int `koanf:"port"`
}

// MCPConfig is the MCP listener plus its transport protocol.
type MCPConfig struct {
	// Host is the interface to bind (ignored for the stdio transport).
	Host string `koanf:"host"`
	// Port is the TCP port for HTTP-based transports (ignored for stdio).
	Port int `koanf:"port"`
	// Transport is the MCP protocol: stdio, streamable-http, or sse.
	Transport options.MCPTransport `koanf:"transport"`
}

// DatabaseConfig selects the persistence backend and carries the connection
// settings for each supported provider. Only the selected provider's block
// needs to be populated.
type DatabaseConfig struct {
	// Provider selects the backend: "gorm" (Postgres) or "hasura".
	Provider string `koanf:"provider"`
	// Postgres holds the GORM/Postgres connection settings.
	Postgres PostgresConfig `koanf:"postgres"`
	// Hasura holds the Hasura GraphQL endpoint and admin authentication.
	Hasura HasuraConfig `koanf:"hasura"`
}

// PostgresConfig is the GORM/Postgres backend connection.
type PostgresConfig struct {
	// Host is the database server hostname or IP (e.g. "127.0.0.1").
	Host string `koanf:"host"`
	// Port is the database server port (Postgres default is 5432).
	Port int `koanf:"port"`
	// User is the role to connect as.
	User string `koanf:"user"`
	// Password is the role's password (blank in release; injected at deploy).
	Password string `koanf:"password"`
	// DBName is the target database name.
	DBName string `koanf:"dbname"`
	// SSLMode is the libpq sslmode, e.g. "disable" (local) or "require" (prod).
	SSLMode string `koanf:"sslmode"`
	// TimeZone is the session time zone passed to the driver (e.g. "UTC").
	TimeZone string `koanf:"timezone"`
	// MaxOpenConns caps the pool's open connections; excess requests queue on
	// the pool, making it the process's backpressure point (0 or negative = 25).
	MaxOpenConns int `koanf:"max_open_conns"`
	// MaxIdleConns is how many idle connections the pool retains (0 or negative
	// = MaxOpenConns, so steady traffic reuses instead of churning connections).
	MaxIdleConns int `koanf:"max_idle_conns"`
	// ConnMaxLifetimeMinutes recycles connections after this long, so load
	// re-spreads after database failovers (0 or negative = 30).
	ConnMaxLifetimeMinutes int `koanf:"conn_max_lifetime_minutes"`
	// ConnMaxIdleMinutes closes connections idle this long (0 or negative = 5).
	ConnMaxIdleMinutes int `koanf:"conn_max_idle_minutes"`
}

// PoolSettings are the resolved connection-pool bounds: PostgresConfig's pool
// fields with the defaults applied for unset values.
type PoolSettings struct {
	MaxOpen     int
	MaxIdle     int
	MaxLifetime time.Duration
	MaxIdleTime time.Duration
}

// Pool resolves the connection-pool settings, applying defaults for unset fields.
func (p PostgresConfig) Pool() PoolSettings {
	s := PoolSettings{
		MaxOpen:     p.MaxOpenConns,
		MaxIdle:     p.MaxIdleConns,
		MaxLifetime: time.Duration(p.ConnMaxLifetimeMinutes) * time.Minute,
		MaxIdleTime: time.Duration(p.ConnMaxIdleMinutes) * time.Minute,
	}
	if s.MaxOpen <= 0 {
		s.MaxOpen = 25
	}
	if s.MaxIdle <= 0 {
		s.MaxIdle = s.MaxOpen
	}
	if s.MaxLifetime <= 0 {
		s.MaxLifetime = 30 * time.Minute
	}
	if s.MaxIdleTime <= 0 {
		s.MaxIdleTime = 5 * time.Minute
	}
	return s
}

// DSN renders the libpq keyword/value connection string that
// gorm.Open(postgres.Open(...)) expects.
func (p PostgresConfig) DSN() string {
	return fmt.Sprintf(
		"host=%s port=%d user=%s password=%s dbname=%s sslmode=%s TimeZone=%s",
		p.Host, p.Port, p.User, p.Password, p.DBName, p.SSLMode, p.TimeZone,
	)
}

// HasuraConfig is the Hasura GraphQL backend connection and admin auth.
type HasuraConfig struct {
	// URL is the GraphQL endpoint, e.g. "http://localhost:8080/v1/graphql".
	URL string `koanf:"url"`
	// AdminSecret is sent as the x-hasura-admin-secret header (empty to omit).
	AdminSecret string `koanf:"admin_secret"`
}
