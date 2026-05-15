package app

import "github.com/hangrix/hangrix/pkg/ioc"

// Module is the top-level wiring node. Loaded last by main.go so the
// container can resolve *App once every dependency module has
// registered its providers.
func Module() *ioc.Module {
	m := ioc.NewModule()
	m.Provide(New).ToSelf()
	return m
}
