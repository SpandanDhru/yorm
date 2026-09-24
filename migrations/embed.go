// Package migrations holds the goose SQL migrations, embedded into the binary.
package migrations

import "embed"

//go:embed *.sql
var FS embed.FS
