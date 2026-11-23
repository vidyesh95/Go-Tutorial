// Package sqlfiles embeds the canonical SQL files of the SQL track.
//
// go:embed can only reach files inside the package directory (or below it), so this tiny package
// lives next to schema.sql and seed.sql and exposes them as strings. Everything else in the track
// imports it instead of keeping its own copy of the schema.
package sqlfiles

import _ "embed"

// Schema is the contents of schema.sql (CREATE TABLE / CREATE INDEX statements).
//
//go:embed schema.sql
var Schema string

// Seed is the contents of seed.sql (sample users, projects, tasks, tags).
//
//go:embed seed.sql
var Seed string
