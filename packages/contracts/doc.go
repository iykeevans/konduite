// Package contracts holds the types generated from the JSON Schemas in
// schemas/ (types.gen.go, written by make contracts). The schemas are the
// source of truth: edit them, run make contracts and commit the output.
// Validate incoming frames and events with the validate subpackage before
// decoding their payloads.
package contracts
