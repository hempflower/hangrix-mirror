package config

import "github.com/hangrix/hangrix/pkg/ioc"

// Module registers the *Config provider. Loaded first by the container
// so every other module's Deps can pull from a validated config.
func Module() *ioc.Module {
	m := ioc.NewModule()
	m.Provide(NewConfig).ToSelf()
	return m
}
