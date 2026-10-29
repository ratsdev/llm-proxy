// Package router selects an upstream for a gateway request. It owns the
// retry loop, the sticky conversation pins, the 429 cooldown table and the
// shared HTTP transport pool.
//
// The engine works on a core.Config snapshot and returns a RouteResult whose
// body the gateway relays; it does not know about HTTP handlers, wire formats
// or the console.
package router
