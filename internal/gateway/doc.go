// Package gateway is the HTTP front end: it authenticates callers, resolves the
// public model to a configuration snapshot, asks the routing engine for an
// upstream reply and relays that reply in the caller's wire format.
//
// The HTTP API, the access-log middleware and the relay live in separate files;
// api.go holds the Server and its route table, api_*.go the metadata endpoints,
// proxy.go the request path and relay*.go the four write modes. The console
// wiring sits in console_bridge.go.
package gateway
