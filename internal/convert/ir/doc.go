// Package ir defines the protocol-neutral intermediate representation shared by
// every wire-format adapter.
//
// Every inbound protocol parses into these types and every outbound protocol
// formats from them, so adding a protocol means writing one adapter rather
// than pairwise conversion logic. The files split the representation into
// types, block helpers, reasoning signatures and token usage.
package ir
