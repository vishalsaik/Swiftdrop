// Package swiftdrop holds the one thing every program here shares: the database schema,
// embedded so the migrate command (and the Docker image) carry it with them.
package swiftdrop

import _ "embed"

// Schema is schema.sql: safe to apply again, it upgrades a database in place.
//
//go:embed schema.sql
var Schema string
