//go:build moonshine_embed

package moonshine

import (
	"embed"
	"io/fs"
)

// The native libraries are compiled into the binary, so a deployment is one
// file. Stage them first with:
//
//	go run ./tools/fetch-moonshine -os linux -arch arm64
//
// Building with this tag and no staged libraries is a compile error, which is
// the right outcome: it says the step was missed rather than producing a
// binary that silently cannot transcribe.
//
//go:embed embedded
var embeddedFS embed.FS

func embeddedLibraries() (fs.FS, bool) {
	libs, err := fs.Sub(embeddedFS, "embedded")
	if err != nil {
		return nil, false
	}
	return libs, true
}
