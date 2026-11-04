// Package api contains the public HTTP contract.
package api

import _ "embed"

// Spec is JSON-compatible YAML, embedded so the contract ships with the API.
//
//go:embed openapi.yaml
var Spec []byte
