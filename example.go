// Package rondo holds the site manifest template that `rondo init` writes.
package rondo

import _ "embed"

//go:embed rondo.yaml.example
var ExampleConfig []byte
