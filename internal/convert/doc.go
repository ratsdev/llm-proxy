// Package convert exposes the pairwise wire-format conversions the gateway can
// perform. It owns the converter catalogue (see catalog.go) and the
// request/response entry points, while the field mapping lives in the protocol
// adapters under convert/protocols/ and the protocol-native error envelopes
// stay beside each adapter.
package convert
