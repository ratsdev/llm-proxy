package gateway

import "github.com/ratsdev/llm-proxy/internal/version"

// Version is the version reported by the HTTP API and the console. The value
// itself lives in internal/version so the routing engine can read it too.
var Version = version.Value
