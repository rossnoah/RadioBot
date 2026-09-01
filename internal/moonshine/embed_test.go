//go:build moonshine_embed

package moonshine

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestEmbeddedLibrariesUnpack covers the whole point of the tag: a binary
// built this way needs nothing deployed alongside it.
func TestEmbeddedLibrariesUnpack(t *testing.T) {
	t.Setenv("RADIOBOT_CACHE_DIR", t.TempDir())

	libs, ok := embeddedLibraries()
	if !ok {
		t.Fatal("built with moonshine_embed but carrying no libraries")
	}
	names, digest, err := inventory(libs)
	if err != nil {
		t.Fatal(err)
	}
	if len(names) < 2 {
		t.Errorf("embedded %v, want the library and its dependency", names)
	}

	path, err := unpackEmbedded(libraryName())
	if err != nil {
		t.Fatalf("unpackEmbedded: %v", err)
	}
	if filepath.Base(path) != libraryName() {
		t.Errorf("unpacked %q, want %s", path, libraryName())
	}
	if !strings.Contains(path, digest) {
		t.Errorf("path %q does not carry the content digest", path)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() < 1<<20 {
		t.Errorf("unpacked library is %d bytes, which is too small to be real", info.Size())
	}

	// A second call must reuse the directory rather than unpack again.
	again, err := unpackEmbedded(libraryName())
	if err != nil || again != path {
		t.Errorf("second unpack = (%q, %v), want the cached path %q", again, err, path)
	}
}

// TestResolveLibraryPrefersEmbedded checks the resolution order: with no
// override set, a self-contained binary uses what it carries.
func TestResolveLibraryPrefersEmbedded(t *testing.T) {
	t.Setenv("MOONSHINE_LIB", "")
	t.Setenv("RADIOBOT_CACHE_DIR", t.TempDir())

	path, err := resolveLibrary("")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(path, "moonshine-") {
		t.Errorf("resolveLibrary = %q, want the unpacked embedded copy", path)
	}
}
