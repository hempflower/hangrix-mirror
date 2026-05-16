package service

import "github.com/hangrix/hangrix/apps/hangrix/internal/modules/agents_config/domain"

// Parser is a stateless wrapper around the package-level parse
// functions so the ioc container has something to bind in M7a Phase 2.
// All work happens in the pure functions — the struct exists only as
// an injection seam. Methods delegate without state so consumers can
// also call the package functions directly during tests.
type Parser struct{}

// NewParser returns a singleton Parser. ioc Provide returns *Parser
// and binds .ToSelf() in module.go.
func NewParser() *Parser {
	return &Parser{}
}

// ParseAgentManifest is the method form of the package function.
func (Parser) ParseAgentManifest(body []byte) (*domain.AgentManifest, error) {
	return ParseAgentManifest(body)
}

// ParseHostConfig is the method form of the package function.
func (Parser) ParseHostConfig(body []byte) (*domain.HostConfig, error) {
	return ParseHostConfig(body)
}

// NormalizeHostConfig is the method form of the package function.
func (Parser) NormalizeHostConfig(cfg *domain.HostConfig) {
	NormalizeHostConfig(cfg)
}

// ParseLockFile is the method form of the package function.
func (Parser) ParseLockFile(body []byte) (*domain.LockFile, error) {
	return ParseLockFile(body)
}

// SerializeLockFile is the method form of the package function.
func (Parser) SerializeLockFile(lf *domain.LockFile) ([]byte, error) {
	return SerializeLockFile(lf)
}
