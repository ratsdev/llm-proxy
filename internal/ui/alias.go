package ui

import "github.com/ratsdev/llm-proxy/internal/core"

// The console works with the shared configuration types from core.
type (
	Config        = core.Config
	Provider      = core.Provider
	ModelProvider = core.ModelProvider
	Model         = core.Model
	User          = core.User
	Store         = core.Store
	ProviderInput = core.ProviderInput
	ModelInput    = core.ModelInput
	UserInput     = core.UserInput
)

var ErrNotFound = core.ErrNotFound
