// Package config resolves $KAIROS_HOME and creates it. The full config.yaml
// schema (admission, models, limits, exec — see 02-config.md) is added
// incrementally by the documents that own each subsystem; this package only
// establishes where Kairos keeps its state, because main.go and every later
// document need that answered the same way.
package config

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/viper"
)

// Config is the bootstrap configuration: where Kairos keeps its state.
type Config struct {
	// Home is $KAIROS_HOME, default ~/.kairos, respecting $XDG_STATE_HOME.
	Home string
}

// Load resolves $KAIROS_HOME (env override, then $XDG_STATE_HOME, then
// ~/.kairos) and ensures the directory exists at mode 0700.
func Load() (Config, error) {
	v := viper.New()
	v.SetEnvPrefix("KAIROS")
	v.AutomaticEnv()

	home := v.GetString("HOME")
	if home == "" {
		if xdg := os.Getenv("XDG_STATE_HOME"); xdg != "" {
			home = filepath.Join(xdg, "kairos")
		} else {
			uh, err := os.UserHomeDir()
			if err != nil {
				return Config{}, fmt.Errorf("resolving user home: %w", err)
			}
			home = filepath.Join(uh, ".kairos")
		}
	}

	if err := os.MkdirAll(home, 0o700); err != nil {
		return Config{}, fmt.Errorf("creating kairos home %s: %w", home, err)
	}

	return Config{Home: home}, nil
}
