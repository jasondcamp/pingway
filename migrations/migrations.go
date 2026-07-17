// Package migrations embeds the numbered schema migration files applied by
// the store at startup. Files are named NNNN_description.sql and applied in
// lexical order; the last applied number is tracked in settings under
// "schema_version".
package migrations

import "embed"

//go:embed *.sql
var FS embed.FS
