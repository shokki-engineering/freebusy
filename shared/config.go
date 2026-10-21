package shared

import (
	"log"
	"sync"

	"github.com/knadh/koanf/parsers/toml"
	"github.com/knadh/koanf/providers/rawbytes"
	"github.com/oh-tarnished/runtime-go/config"
)

var (
	// The global instance is unexported to prevent other packages from modifying it directly.
	globalconfig *config.Config
	// Once ensures the initialization function is called exactly one time.
	configOnce sync.Once
)

// Getconfig returns the single, shared instance of the config session.
// It initializes the session on the first call. If initialization fails,
// the program will exit with a fatal error.
func Getconfig() *config.Config {
	// once.Do will execute the function only on the very first call to Getconfig.
	// All subsequent calls will skip the function but still get the result.
	configOnce.Do(func() {
		// Initialize the session with a default configuration.
		// You can customize the BasePath or other options here.
		sy, err := config.New(config.Options{
			BasePath:   ".",
			YamlParser: config.KoanfYamlParser,
			JsonParser: config.KoanfJsonParser,
			TomlParser: config.KoanfTomlParser,
		})
		if err != nil {
			// Using log.Fatalf is preferable to panic for initialization errors.
			log.Fatalf("FATAL: Failed to initialize global config session: %v", err)
		}
		globalconfig = sy
	})

	return globalconfig
}

// LoadTomlBytes loads raw TOML bytes into the shared config's koanf state,
// with the parser config's own Toml.Load uses. It reads from memory, so it
// works where the working directory is not writable -- a scratch container
// running as an unprivileged user.
func LoadTomlBytes(data []byte) error {
	return Getconfig().Koanf().Load(rawbytes.Provider(data), toml.Parser())
}

// LoadEnv overlays every environment variable starting with prefix: double
// underscores separate the levels and the rest is the key, lower-cased --
// FREEBUSY_DATABASE__POSTGRES__HOST is database.postgres.host. A variable with
// no double underscore has each underscore read as a level, so a key that has
// one of its own, like enable_http, needs the double-underscore form.
func LoadEnv(prefix string) error {
	return Getconfig().LoadEnv(prefix)
}
