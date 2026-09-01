package moonshine

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
)

func fakeLibs() fstest.MapFS {
	return fstest.MapFS{
		"libmoonshine.so":         {Data: []byte("main library bytes")},
		"libonnxruntime-abc.so.1": {Data: []byte("dependency bytes")},
	}
}

func TestInventoryIsDeterministic(t *testing.T) {
	libs := fakeLibs()

	names, digest, err := inventory(libs)
	if err != nil {
		t.Fatal(err)
	}
	if len(names) != 2 || names[0] != "libmoonshine.so" {
		t.Errorf("names = %v, want them sorted", names)
	}
	if len(digest) != 16 {
		t.Errorf("digest = %q, want 16 characters", digest)
	}

	_, again, err := inventory(libs)
	if err != nil {
		t.Fatal(err)
	}
	if again != digest {
		t.Errorf("digest changed between runs: %q then %q", digest, again)
	}
}

// TestInventoryDigestTracksContent is what makes the cache directory safe to
// reuse: different libraries must never land in the same directory.
func TestInventoryDigestTracksContent(t *testing.T) {
	_, first, err := inventory(fakeLibs())
	if err != nil {
		t.Fatal(err)
	}

	changed := fakeLibs()
	changed["libmoonshine.so"] = &fstest.MapFile{Data: []byte("a different build")}
	_, second, err := inventory(changed)
	if err != nil {
		t.Fatal(err)
	}

	if first == second {
		t.Error("changing a library did not change the digest")
	}
}

func TestUnpackIntoWritesEverySibling(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "moonshine-test")
	libs := fakeLibs()

	names, _, err := inventory(libs)
	if err != nil {
		t.Fatal(err)
	}
	if err := unpackInto(libs, names, dir, root); err != nil {
		t.Fatalf("unpackInto: %v", err)
	}

	// Both libraries must be present and side by side, since that is how the
	// loader finds the dependency.
	for name, want := range map[string]string{
		"libmoonshine.so":         "main library bytes",
		"libonnxruntime-abc.so.1": "dependency bytes",
	} {
		got, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Errorf("reading %s: %v", name, err)
			continue
		}
		if string(got) != want {
			t.Errorf("%s = %q, want %q", name, got, want)
		}
	}

	// No staging directories left behind.
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".unpack-") {
			t.Errorf("a staging directory was left behind: %s", entry.Name())
		}
	}
}

func TestUnpackIntoIsExecutable(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "moonshine-test")
	libs := fakeLibs()
	names, _, _ := inventory(libs)

	if err := unpackInto(libs, names, dir, root); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Join(dir, "libmoonshine.so"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm()&0o111 == 0 {
		t.Errorf("mode = %v, want the executable bit set", info.Mode().Perm())
	}
}

// TestUnpackIntoConcurrent covers several processes starting at once, which
// is what a restart loop looks like.
func TestUnpackIntoConcurrent(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "moonshine-test")
	libs := fakeLibs()
	names, _, _ := inventory(libs)

	var wg sync.WaitGroup
	errs := make([]error, 8)
	for i := range errs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			errs[i] = unpackInto(libs, names, dir, root)
		}(i)
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Errorf("unpack %d failed: %v", i, err)
		}
	}
	got, err := os.ReadFile(filepath.Join(dir, "libmoonshine.so"))
	if err != nil || string(got) != "main library bytes" {
		t.Errorf("library after concurrent unpacking = %q (err %v)", got, err)
	}
}

func TestCacheRootHonoursOverride(t *testing.T) {
	t.Setenv("RADIOBOT_CACHE_DIR", "/custom/cache")
	root, err := cacheRoot()
	if err != nil || root != "/custom/cache" {
		t.Errorf("cacheRoot = (%q, %v), want the override", root, err)
	}
}

func TestCacheRootDefault(t *testing.T) {
	t.Setenv("RADIOBOT_CACHE_DIR", "")
	root, err := cacheRoot()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(root, "radiobot") {
		t.Errorf("cacheRoot = %q, want it namespaced to radiobot", root)
	}
}

// TestUnpackEmbeddedWithoutTag documents the default build: no libraries are
// carried, and the caller falls through to looking on disk.
func TestUnpackEmbeddedWithoutTag(t *testing.T) {
	if _, ok := embeddedLibraries(); ok {
		t.Skip("built with moonshine_embed; see the tagged test")
	}
	if _, err := unpackEmbedded("libmoonshine.so"); !errors.Is(err, errNoEmbeddedLibrary) {
		t.Errorf("error = %v, want errNoEmbeddedLibrary", err)
	}
}
