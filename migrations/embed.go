// Package migrations embeds the SQL schema files so they ship inside the binary
// and are applied automatically at startup.
package migrations

import "embed"

// FS holds all *.sql migration files in lexical order.
//
//go:embed *.sql
var FS embed.FS
