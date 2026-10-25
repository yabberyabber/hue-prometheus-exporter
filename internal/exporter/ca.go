package exporter

import _ "embed"

// DefaultCA is Signify's published root CA for Hue bridge certificates,
// used to verify the bridge unless a different bundle is supplied.
//
//go:embed hue-ca.pem
var DefaultCA []byte
