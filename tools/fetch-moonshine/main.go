// Command fetch-moonshine downloads the Moonshine native libraries for a
// target platform and stages them for embedding into the radiobot binary.
//
// Moonshine publishes its native libraries inside Python wheels, but the
// wheels are just zip archives and only the shared libraries are taken, so
// nothing here needs Python. Because the target is a flag rather than the
// host, a Raspberry Pi build can be staged entirely from a laptop.
//
//	go run ./tools/fetch-moonshine -os linux -arch arm64
//	go build -tags moonshine_embed ./cmd/radiobot
package main

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"
)

// version is the Moonshine release the binding targets. The C ABI version is
// checked again at runtime, so a mismatch here fails loudly rather than
// silently misreading the library's structures.
const version = "0.1.5"

// wheelTags maps a Go target to the wheel carrying its libraries. Linux arm64
// deliberately uses the manylinux_2_31 build: it runs on glibc 2.31 and newer,
// which covers Raspberry Pi OS bullseye as well as the bookworm and Ubuntu
// releases a 2_34 wheel would restrict it to.
var wheelTags = map[string]string{
	"linux/arm64":  "manylinux_2_31_aarch64",
	"linux/amd64":  "manylinux_2_34_x86_64",
	"darwin/arm64": "macosx_15_0_arm64",
}

func main() {
	log.SetFlags(0)

	goos := flag.String("os", "linux", "target operating system")
	goarch := flag.String("arch", "arm64", "target architecture")
	outDir := flag.String("out", filepath.Join("internal", "moonshine", "embedded"),
		"directory to stage the libraries in")
	wheelTag := flag.String("wheel", "", "override the wheel platform tag")
	flag.Parse()

	target := *goos + "/" + *goarch
	tag := *wheelTag
	if tag == "" {
		var ok bool
		if tag, ok = wheelTags[target]; !ok {
			log.Fatalf("no known Moonshine wheel for %s; pass -wheel to name one", target)
		}
	}

	if err := fetch(tag, *outDir); err != nil {
		log.Fatalf("fetching the Moonshine libraries: %v", err)
	}
}

func fetch(wheelTag, outDir string) error {
	url, err := wheelURL(wheelTag)
	if err != nil {
		return err
	}
	log.Printf("downloading %s", path.Base(url))

	archive, err := download(url)
	if err != nil {
		return err
	}

	reader, err := zip.NewReader(bytes.NewReader(archive), int64(len(archive)))
	if err != nil {
		return fmt.Errorf("reading the wheel: %w", err)
	}

	// Replace whatever was staged before, so a target switch cannot leave a
	// library for the wrong platform behind.
	if err := os.RemoveAll(outDir); err != nil {
		return err
	}
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return err
	}

	var extracted int
	for _, file := range reader.File {
		name := path.Base(file.Name)
		if !isSharedLibrary(name) {
			continue
		}
		// Flattened deliberately: both platforms resolve a library's
		// dependencies from its own directory ($ORIGIN / @loader_path), so
		// the pair has to land side by side.
		if err := extractFile(file, filepath.Join(outDir, name)); err != nil {
			return fmt.Errorf("extracting %s: %w", name, err)
		}
		log.Printf("staged %s (%s)", name, humanSize(int64(file.UncompressedSize64)))
		extracted++
	}

	if extracted == 0 {
		return fmt.Errorf("the wheel contained no shared libraries")
	}
	log.Printf("staged %d libraries in %s", extracted, outDir)
	log.Printf("now build with: go build -tags moonshine_embed ./cmd/radiobot")
	return nil
}

func isSharedLibrary(name string) bool {
	return strings.HasSuffix(name, ".dylib") ||
		strings.HasSuffix(name, ".so") ||
		strings.Contains(name, ".so.")
}

// wheelURL finds the download URL for a platform tag on PyPI.
func wheelURL(wheelTag string) (string, error) {
	url := fmt.Sprintf("https://pypi.org/pypi/moonshine-voice/%s/json", version)

	resp, err := http.Get(url)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("PyPI returned %s for moonshine-voice %s", resp.Status, version)
	}

	var release struct {
		URLs []struct {
			Filename string `json:"filename"`
			URL      string `json:"url"`
		} `json:"urls"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&release); err != nil {
		return "", err
	}

	suffix := wheelTag + ".whl"
	var available []string
	for _, file := range release.URLs {
		if strings.HasSuffix(file.Filename, suffix) {
			return file.URL, nil
		}
		available = append(available, file.Filename)
	}
	return "", fmt.Errorf("no wheel matching %q in moonshine-voice %s; available: %s",
		wheelTag, version, strings.Join(available, ", "))
}

func download(url string) ([]byte, error) {
	client := &http.Client{Timeout: 10 * time.Minute}
	resp, err := client.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("server returned %s", resp.Status)
	}
	return io.ReadAll(resp.Body)
}

func extractFile(file *zip.File, dest string) error {
	src, err := file.Open()
	if err != nil {
		return err
	}
	defer src.Close()

	out, err := os.OpenFile(dest, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, src); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

func humanSize(bytes int64) string {
	value := float64(bytes)
	for _, unit := range []string{"B", "KB", "MB", "GB"} {
		if value < 1024 {
			return fmt.Sprintf("%.1f %s", value, unit)
		}
		value /= 1024
	}
	return fmt.Sprintf("%.1f TB", value)
}
