// Copyright 2026 Srikanth K.
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package config loads the freebusy service configuration and exposes it via
// Get(). Every configuration file is compiled into the binary, so it runs with
// nothing beside it: freebusy.release.toml holds production-safe defaults
// (secrets blank, injected at deploy time), and freebusy.dev.toml the
// development profile, chosen with FREEBUSY_PROFILE=dev. See load.go for the
// layers. Configuration is the single source of truth for service identity and
// database connectivity.
package config

import (
	"cmp"
	"embed"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"time"

	"github.com/the-protobuf-project/runtime-go/grpc/options"
)

//go:embed freebusy.release.toml freebusy.dev.toml
var configFS embed.FS

// devOverlayPath is the development profile's file on disk, relative to the
// working directory. When present it is read instead of the embedded copy, so
// a developer's edits apply without a rebuild.
const devOverlayPath = "internal/config/freebusy.dev.toml"

// ProfileVar picks the profile layered over the release defaults: "dev" for
// the development profile, "release" for none. Unset, the development profile
// applies only when its file is in the working directory -- a checkout.
const ProfileVar = "FREEBUSY_PROFILE"

// AppConfig is the top-level configuration tree.
type AppConfig struct {
	// Meta holds service identity metadata.
	Meta AppMeta `koanf:"app"`
	// Server holds the hybrid gRPC/HTTP/MCP server settings.
	Server ServerConfig `koanf:"server"`
	// Database holds the backend selection and per-provider connection settings.
	Database DatabaseConfig `koanf:"database"`
	// Cache holds the read-through cache backend. Disabled by default: the
	// service is correct without it, and a cache that is on before anyone has
	// measured the read it serves is just another thing that can be stale.
	Cache CacheConfig `koanf:"cache"`
	// CalDAV is the RFC 4791 server mounted on the HTTP port.
	CalDAV CalDAVConfig `koanf:"caldav"`

	// Webhooks tunes delivery of hold and reservation changes.
	Webhooks WebhooksConfig `koanf:"webhooks"`
	// Retention decides how long soft-deleted records are kept.
	Retention RetentionConfig `koanf:"retention"`
	// Engine bounds the availability engine's work.
	Engine EngineConfig `koanf:"engine"`
	// Search configures the availability read model (allocation.v1.ResourceDay)
	// the searches read their candidates from.
	Search SearchConfig `koanf:"search"`
	// ReferenceServices are the services that keep resources freebusy names
	// but does not store, such as a reservation's customer (users/{user}).
	ReferenceServices []ReferenceServiceConfig `koanf:"reference_services"`
}

// ReferenceServiceConfig is one deployment fact about a resource type kept
// by another service, the runtime counterpart of store.yaml's
// reference_services: where its AIP Get is served, so a write naming one is
// checked there (REFERENCE_NOT_FOUND when it is missing), and which of
// freebusy's resource types that service refers to, so deleting one of them
// asks its store.v1.ReferenceService first (RESOURCE_REFERENCED when one of
// its referrers restricts the delete). Without an entry for a type, a
// reference to it is stored unchecked.
type ReferenceServiceConfig struct {
	// Type is the AIP resource type, e.g. "users.shokki.engineering/User".
	Type string `koanf:"type"`
	// Get is its full AIP Get method, e.g. "users.v1.UserService/GetUser".
	Get string `koanf:"get"`
	// Target is the gRPC dial target; ${NAME} is read from the environment,
	// and an empty one leaves the entry off.
	Target string `koanf:"target"`
	// Referrers are freebusy resource types the service refers to, e.g.
	// "the-protobuf-project.org/Resource"; it serves store.v1.ReferenceService.
	Referrers []string `koanf:"referrers"`
	// Plaintext dials without TLS, for a service on the same host or mesh.
	Plaintext bool `koanf:"plaintext"`
}

// EngineConfig is admission control for the availability engine: how many
// evaluations -- SearchAvailability, ComputeAvailability (and its batch),
// CheckAvailability, and CreateHold's choice of resources -- run at once,
// and how long one waits for a turn before it is refused with
// RESOURCE_EXHAUSTED / ENGINE_BUSY (docs/errors.md).
type EngineConfig struct {
	// MaxEvaluations is how many run at once; 0 is the number of CPUs.
	// FREEBUSY_ENGINE__MAX_EVALUATIONS.
	MaxEvaluations int `koanf:"max_evaluations"`
	// AdmissionWaitMS is how long one waits for a turn, in milliseconds; 0 is
	// 250. FREEBUSY_ENGINE__ADMISSION_WAIT_MS.
	AdmissionWaitMS int `koanf:"admission_wait_ms"`
}

// SearchConfig is the availability read model: one row per resource per UTC
// day of a rolling horizon, holding its standing free time, kept current in
// every write to its sources and searched by SearchAvailability,
// ComputeAvailability, CheckAvailability and CreateHold for candidates the
// engine then evaluates.
type SearchConfig struct {
	// HorizonDays is how many days ahead, from today (UTC), rows are kept;
	// 0 is 365. A window past the horizon is evaluated without the read
	// model. FREEBUSY_SEARCH__HORIZON_DAYS.
	HorizonDays int `koanf:"horizon_days"`
	// Provider is where SearchAvailability searches: "postgres" (the
	// default) or "opensearch", which falls back to Postgres on an error or a
	// timeout. FREEBUSY_SEARCH__PROVIDER.
	Provider string `koanf:"provider"`
	// RebuildOnStart is when the server recomputes the whole read model as
	// it starts, in the background while it serves: "empty" (the default)
	// when the table has no rows -- a fresh deployment, or an upgrade from a
	// version without it; "always" on every start -- the repair after writes
	// that bypassed the API, applied by restarting with it set; "never".
	// FREEBUSY_SEARCH__REBUILD_ON_START.
	RebuildOnStart string `koanf:"rebuild_on_start"`
	// OpenSearch is the cluster provider "opensearch" indexes into.
	OpenSearch OpenSearchConfig `koanf:"opensearch"`
}

// When the server rebuilds the read model, or reindexes its OpenSearch copy,
// at startup ([search] rebuild_on_start, [search.opensearch]
// reindex_on_start).
const (
	OnStartEmpty  = "empty"
	OnStartAlways = "always"
	OnStartNever  = "never"
)

// OnStartModes are the accepted rebuild_on_start / reindex_on_start values.
var OnStartModes = []string{OnStartEmpty, OnStartAlways, OnStartNever}

// RebuildMode is RebuildOnStart, defaulted.
func (s SearchConfig) RebuildMode() string { return cmp.Or(s.RebuildOnStart, OnStartEmpty) }

// ReindexMode is ReindexOnStart, defaulted.
func (o OpenSearchConfig) ReindexMode() string { return cmp.Or(o.ReindexOnStart, OnStartEmpty) }

// OpenSearchConfig is the OpenSearch cluster the read model is copied to.
type OpenSearchConfig struct {
	// URL is the cluster endpoint, e.g. "http://localhost:9200".
	// FREEBUSY_SEARCH__OPENSEARCH__URL.
	URL string `koanf:"url"`
	// Username and Password are HTTP basic credentials; empty sends none.
	// FREEBUSY_SEARCH__OPENSEARCH__USERNAME, FREEBUSY_SEARCH__OPENSEARCH__PASSWORD.
	Username string `koanf:"username"`
	Password string `koanf:"password"`
	// IndexQueue is how many committed changes the indexer buffers before a
	// write waits for it; 0 is 1024. FREEBUSY_SEARCH__OPENSEARCH__INDEX_QUEUE.
	IndexQueue int `koanf:"index_queue"`
	// TimeoutMS bounds one search before it falls back to Postgres; 0 is
	// 500. FREEBUSY_SEARCH__OPENSEARCH__TIMEOUT_MS.
	TimeoutMS int `koanf:"timeout_ms"`
	// ReindexOnStart is when the server reloads the index from the read
	// model's table as it starts, in the background: "empty" (the default)
	// when the index has no documents but the table has rows -- the alias
	// was just created, or the cluster was replaced; "always" on every start
	// -- the repair after the indexer missed changes, applied by restarting
	// one replica with it set; "never". A start that rebuilt the read model
	// leaves the index to the indexer, which follows the rebuild.
	// FREEBUSY_SEARCH__OPENSEARCH__REINDEX_ON_START.
	ReindexOnStart string `koanf:"reindex_on_start"`
}

// Horizon is HorizonDays, defaulted.
func (s SearchConfig) Horizon() int { return cmp.Or(s.HorizonDays, 365) }

// UsesOpenSearch reports whether SearchAvailability is served from OpenSearch.
func (s SearchConfig) UsesOpenSearch() bool { return s.Provider == "opensearch" }

// Timeout is TimeoutMS as a duration, defaulted.
func (o OpenSearchConfig) Timeout() time.Duration {
	return time.Duration(cmp.Or(o.TimeoutMS, 500)) * time.Millisecond
}

// AdmissionWait is AdmissionWaitMS as a duration, zero when unset.
func (e EngineConfig) AdmissionWait() time.Duration {
	return time.Duration(e.AdmissionWaitMS) * time.Millisecond
}

// RetentionConfig ends soft deletes (AIP-164): organizations, sites,
// resources, calendars, events and availability deleted longer ago than
// DeletedDays are purged for good, and expire_time says when.
type RetentionConfig struct {
	// Purge runs the purger on this server. Safe on several servers at once.
	Purge bool `koanf:"purge"`
	// DeletedDays keeps a soft-deleted record this long; 0 is 30.
	DeletedDays int `koanf:"deleted_days"`
}

// Period is how long a soft-deleted record stays restorable: DeletedDays, or
// 30 days when unset.
func (r RetentionConfig) Period() time.Duration {
	return time.Duration(cmp.Or(r.DeletedDays, 30)) * 24 * time.Hour
}

// WebhooksConfig tunes webhook delivery (internal/service/webhook).
// WebhookService itself is always served; this decides whether this server
// also sends what it records.
type WebhooksConfig struct {
	// Enabled runs delivery on this server: recording changes and sending
	// them. Safe on several servers at once.
	Enabled bool `koanf:"enabled"`
	// AllowInsecureEndpoints accepts http:// endpoints on loopback hosts, for
	// a local receiver in development.
	AllowInsecureEndpoints bool `koanf:"allow_insecure_endpoints"`
	// AllowPrivateNetworks lets endpoints reach loopback, private and
	// link-local addresses. Off, only public addresses are dialled, so a
	// webhook cannot be pointed into this server's own network.
	AllowPrivateNetworks bool `koanf:"allow_private_networks"`
	// RetentionDays keeps finished deliveries this long; 0 is 30.
	RetentionDays int `koanf:"retention_days"`
	// IntervalSeconds between delivery rounds; 0 is 5.
	IntervalSeconds int `koanf:"interval_seconds"`
	// MaxAttempts before a delivery is given up as FAILED; 0 is 8.
	MaxAttempts int `koanf:"max_attempts"`
	// SecretKey seals webhook signing secrets at rest: the base64 of 32 random
	// bytes (openssl rand -base64 32). Empty stores them unsealed. A secret,
	// so set it through FREEBUSY_WEBHOOKS__SECRET_KEY, not a file.
	SecretKey string `koanf:"secret_key"`
	// PreviousSecretKeys are keys SecretKey replaced, comma-separated, kept so
	// secrets they sealed still open; each is resealed at its webhook's next
	// rotation.
	PreviousSecretKeys string `koanf:"previous_secret_keys"`
}

// CalDAVConfig switches the CalDAV server (internal/caldav) on. It has no
// authentication yet -- every request acts as one principal -- so it is a
// development surface: on in freebusy.dev.toml, off in the release defaults.
type CalDAVConfig struct {
	// Enabled mounts CalDAV under /dav/ on the HTTP gateway's port.
	Enabled bool `koanf:"enabled"`
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
	// ShutdownTimeoutSeconds bounds how long a stop waits for in-flight
	// requests -- HTTP gateways first, then gRPC -- before cutting them off.
	// 0 keeps the runtime's default (20 s). Keep it below the orchestrator's
	// grace period minus any preStop delay, leaving room to flush telemetry.
	ShutdownTimeoutSeconds int `koanf:"shutdown_timeout_seconds"`
}

// ShutdownTimeout is ShutdownTimeoutSeconds as a duration; 0 means the
// runtime's default.
func (s ServerConfig) ShutdownTimeout() time.Duration {
	return time.Duration(s.ShutdownTimeoutSeconds) * time.Second
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
	// Path is where one MCP server carrying every service's tools answers on
	// the MCP port. Empty means "/mcp".
	Path string `koanf:"path"`
	// Stateless serves streamable HTTP without sessions, at Path and at each
	// service's own path: no Mcp-Session-Id is issued, so any replica answers
	// any request. It gives up server-to-client requests (elicitation,
	// sampling, roots), the standalone GET stream and DELETE.
	Stateless bool `koanf:"stateless"`
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
	// PlanCacheMode is the plan_cache_mode every connection the server opens
	// sets at startup: "force_custom_plan" (the default when empty), "auto"
	// (Postgres's own default) or "force_generic_plan"; "database" sends
	// nothing and leaves it to ALTER DATABASE/ROLE, for a pooler that refuses
	// startup parameters. See PlanCacheModes.
	PlanCacheMode string `koanf:"plan_cache_mode"`
}

// PlanCacheModeDatabase is the PlanCacheMode that leaves plan_cache_mode to
// the database or role settings instead of setting it per connection.
const PlanCacheModeDatabase = "database"

// PlanCacheModes are the accepted PlanCacheMode values ("" means the first).
var PlanCacheModes = []string{"force_custom_plan", "auto", "force_generic_plan", PlanCacheModeDatabase}

// PlanCache is the plan_cache_mode the connections set, "" for none.
//
// The default forces a custom plan because freebusy's statements are short
// and their best plan depends on the values bound: the read model's search
// filters on a JSON attribute and a day, and with Postgres's default a
// pooled connection switches a prepared statement to a generic plan after
// five runs, which for a filtered, faceted search took 150-420 ms instead of
// 7-25 ms. Planning each run costs well under a millisecond.
func (p PostgresConfig) PlanCache() string {
	switch p.PlanCacheMode {
	case "":
		return PlanCacheModes[0]
	case PlanCacheModeDatabase:
		return ""
	}
	return p.PlanCacheMode
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
//
// Keys pgx does not know itself, like plan_cache_mode, are sent as runtime
// parameters in the startup message, so every pooled connection has them.
func (p PostgresConfig) DSN() string {
	dsn := fmt.Sprintf(
		"host=%s port=%d user=%s password=%s dbname=%s sslmode=%s TimeZone=%s",
		p.Host, p.Port, p.User, p.Password, p.DBName, p.SSLMode, p.TimeZone,
	)
	if mode := p.PlanCache(); mode != "" {
		dsn += " plan_cache_mode=" + mode
	}
	return dsn
}

// URL is the connection as a postgres:// URL, for tools that take one (atlas),
// optionally naming another database on the same server.
func (p PostgresConfig) URL(dbname string) string {
	if dbname == "" {
		dbname = p.DBName
	}
	u := url.URL{
		Scheme:   "postgres",
		User:     url.UserPassword(p.User, p.Password),
		Host:     net.JoinHostPort(p.Host, strconv.Itoa(p.Port)),
		Path:     "/" + dbname,
		RawQuery: url.Values{"sslmode": {cmp.Or(p.SSLMode, "disable")}}.Encode(),
	}
	return u.String()
}

// HasuraConfig is the Hasura GraphQL backend connection and admin auth.
type HasuraConfig struct {
	// URL is the GraphQL endpoint, e.g. "http://localhost:8080/v1/graphql".
	URL string `koanf:"url"`
	// AdminSecret is sent as the x-hasura-admin-secret header (empty to omit).
	AdminSecret string `koanf:"admin_secret"`
}
