package moonshine

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
)

// errNoEmbeddedLibrary reports a binary built without embedded libraries.
var errNoEmbeddedLibrary = errors.New("this build has no embedded moonshine library")

// unpackEmbedded writes the embedded libraries to a cache directory and
// returns the path of the main one.
//
// They have to reach the filesystem because dlopen takes a path, and they go
// side by side because that is how each platform resolves a library's own
// dependencies: RPATH $ORIGIN on Linux, @loader_path on macOS. The directory
// is named after a digest of the contents, so a rebuilt binary lands in a new
// directory instead of colliding with the previous one.
func unpackEmbedded(libraryName string) (string, error) {
	libs, ok := embeddedLibraries()
	if !ok {
		return "", errNoEmbeddedLibrary
	}

	names, digest, err := inventory(libs)
	if err != nil {
		return "", err
	}
	if len(names) == 0 {
		return "", errNoEmbeddedLibrary
	}

	root, err := cacheRoot()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(root, "moonshine-"+digest)
	target := filepath.Join(dir, libraryName)

	// A previous run already unpacked this exact build.
	if _, err := os.Stat(target); err == nil {
		return target, nil
	}

	if err := unpackInto(libs, names, dir, root); err != nil {
		return "", err
	}
	if _, err := os.Stat(target); err != nil {
		return "", fmt.Errorf("embedded libraries do not contain %s", libraryName)
	}

	slog.Info("unpacked the embedded moonshine library", "dir", dir)
	return target, nil
}

// unpackInto writes the libraries to a temporary directory and moves it into
// place as a unit, so a concurrent start or an interrupted write can never
// leave a half-populated directory that looks usable.
func unpackInto(libs fs.FS, names []string, dir, root string) error {
	if err := os.MkdirAll(root, 0o755); err != nil {
		return err
	}
	staging, err := os.MkdirTemp(root, ".unpack-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(staging)

	for _, name := range names {
		if err := copyOut(libs, name, filepath.Join(staging, name)); err != nil {
			return fmt.Errorf("unpacking %s: %w", name, err)
		}
	}

	if err := os.Rename(staging, dir); err != nil {
		// Another process almost certainly got there first; its copy is the
		// same bytes, since the directory is named after their digest.
		if _, statErr := os.Stat(dir); statErr == nil {
			return nil
		}
		return err
	}
	return nil
}

func copyOut(libs fs.FS, name, dest string) error {
	src, err := libs.Open(name)
	if err != nil {
		return err
	}
	defer src.Close()

	// Executable permissions: some loaders refuse to map a library that is
	// not marked as such.
	out, err := os.OpenFile(dest, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o755)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, src); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// inventory lists the embedded libraries and digests their contents, so the
// cache directory name changes whenever the libraries do.
func inventory(libs fs.FS) (names []string, digest string, err error) {
	entries, err := fs.ReadDir(libs, ".")
	if err != nil {
		return nil, "", err
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			names = append(names, entry.Name())
		}
	}
	sort.Strings(names)

	hash := sha256.New()
	for _, name := range names {
		fmt.Fprintf(hash, "%s\x00", name)
		file, err := libs.Open(name)
		if err != nil {
			return nil, "", err
		}
		if _, err := io.Copy(hash, file); err != nil {
			file.Close()
			return nil, "", err
		}
		file.Close()
	}
	return names, hex.EncodeToString(hash.Sum(nil))[:16], nil
}

// cacheRoot is where unpacked libraries live. It follows the user's cache
// directory, falling back to the temporary directory on a system where that
// is not defined (a service account with no HOME, for instance).
func cacheRoot() (string, error) {
	if override := os.Getenv("RADIOBOT_CACHE_DIR"); override != "" {
		return override, nil
	}
	if dir, err := os.UserCacheDir(); err == nil {
		return filepath.Join(dir, "radiobot"), nil
	}
	return filepath.Join(os.TempDir(), "radiobot-cache"), nil
}
