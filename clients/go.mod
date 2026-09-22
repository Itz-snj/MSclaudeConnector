// This module boundary keeps `go build/vet/test ./...` from descending into
// clients/node_modules (some npm packages ship Go source). The clients tree is
// JavaScript/TypeScript only; this module intentionally has no Go packages.
module github.com/Itz-snj/MSclaudeConnector/clients

go 1.24
