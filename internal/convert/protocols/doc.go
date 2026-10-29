// Package protocols defines the contract between the gateway IR and concrete
// wire formats. Concrete adapters live in sub-packages (openai, anthropic) so
// that each vendor's conversion rules stay isolated. Shared helpers for those
// adapters live here as well.
package protocols
