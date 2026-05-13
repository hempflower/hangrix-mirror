package server

import "github.com/hangrix/hangrix/pkg/ioc"

func Module() *ioc.Module {
	m := ioc.NewModule()
	m.Provide(NewServer).ToSelf()
	return m
}
