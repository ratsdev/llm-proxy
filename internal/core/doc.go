// Package core holds the gateway's configuration model and its SQLite store.
//
// Config is an immutable snapshot rebuilt by the store whenever an
// administrator changes a record; the HTTP layer swaps snapshots instead of
// mutating shared state. The store files are split by aggregate: accounts,
// catalogue, settings, snapshot loading and schema migrations.
package core
