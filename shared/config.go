package shared

import (
	"strings"
	"sync"

	"github.com/knadh/koanf/parsers/toml"
	"github.com/knadh/koanf/providers/env/v2"
	"github.com/knadh/koanf/providers/rawbytes"
	"github.com/knadh/koanf/v2"
)

var (
	// configState is the process's configuration, built in layers by
	// config/load.go: embedded defaults, the dev overlay, the environment.
	configState *koanf.Koanf
	configOnce  sync.Once
)

// Config returns the process's configuration state, created on first use.
func Config() *koanf.Koanf {
	configOnce.Do(func() { configState = koanf.New(".") })
	return configState
}

// LoadTomlBytes overlays raw TOML onto the configuration. It reads from
// memory, so it works where the working directory is not writable -- a scratch
// container running as an unprivileged user.
func LoadTomlBytes(data []byte) error {
	return Config().Load(rawbytes.Provider(data), toml.Parser())
}

// LoadEnv overlays every environment variable starting with prefix: double
// underscores separate the levels and the rest is the key, lower-cased --
// FREEBUSY_DATABASE__POSTGRES__HOST is database.postgres.host. A variable with
// no double underscore has each underscore read as a level, so a key that has
// one of its own, like enable_http, needs the double-underscore form.
func LoadEnv(prefix string) error {
	return Config().Load(env.Provider(".", env.Opt{
		Prefix: prefix,
		TransformFunc: func(k, v string) (string, any) {
			key := strings.ToLower(strings.TrimPrefix(k, prefix))
			if strings.Contains(key, "__") {
				return strings.ReplaceAll(key, "__", "."), v
			}
			return strings.ReplaceAll(key, "_", "."), v
		},
	}), nil)
}

// UnmarshalConfig decodes the configuration into out by its koanf tags.
// Strings convert to the field's type, so an environment variable can set a
// port or a flag.
func UnmarshalConfig(out any) error {
	return Config().UnmarshalWithConf("", out, koanf.UnmarshalConf{Tag: "koanf"})
}
