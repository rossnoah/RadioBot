//go:build !moonshine_embed

package moonshine

import "io/fs"

// Without the moonshine_embed tag the binary carries no native libraries and
// resolves them from disk instead. This keeps a default build small for
// anyone running Deepgram-only, and keeps `go test ./...` from needing a
// staging step.
func embeddedLibraries() (fs.FS, bool) { return nil, false }
