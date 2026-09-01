package config

// Config file versioning and auto-migration.
//
// config.yaml carries a top-level `config_version` field. A config without one
// is treated as version 1 (the original, unversioned schema). On startup, any
// config older than Version is migrated one step at a time in memory, the
// original file is backed up alongside as config.yaml.bak.v<N>, and the
// migrated config is written back to disk.
//
// Migrations operate on a yaml.Node tree rather than a plain map, so key order
// and hand-written comments in config.yaml survive the rewrite. (The Python
// implementation went through safe_dump and lost both.)
//
// To add a migration:
//  1. Bump Version.
//  2. Write a func(*yaml.Node) error that transforms a version N-1 document
//     mapping into a version N one.
//  3. Register it in migrations under key N.

import (
	"bytes"
	"fmt"
	"io"
	"log/slog"
	"os"

	"gopkg.in/yaml.v3"
)

// Version is the config schema version this build understands.
const Version = 2

var migrations = map[int]func(doc *yaml.Node) error{
	2: migrateTo2,
}

// migrateTo2 renames application.prank_password -> application.test_password
// (the /prank page became the /test console).
func migrateTo2(doc *yaml.Node) error {
	app := mapValue(doc, "application")
	if app == nil || app.Kind != yaml.MappingNode {
		return nil
	}
	if mapValue(app, "test_password") != nil {
		// Already has the new key; just drop the old one.
		deleteKey(app, "prank_password")
		return nil
	}
	for i := 0; i+1 < len(app.Content); i += 2 {
		if app.Content[i].Value == "prank_password" {
			app.Content[i].Value = "test_password"
			return nil
		}
	}
	return nil
}

// Migrate brings raw config bytes up to Version, persisting the result to path.
// It always returns a config at the latest version — if the disk write fails,
// the returned bytes are still fully migrated. It never fails on I/O problems
// with the backup or the rewrite, only on unparseable YAML.
func Migrate(data []byte, path string) ([]byte, error) {
	var root yaml.Node
	if err := yaml.Unmarshal(data, &root); err != nil {
		return nil, fmt.Errorf("invalid YAML in %s: %w", path, err)
	}
	if len(root.Content) == 0 {
		return data, nil // empty file; validation will report the missing fields
	}
	doc := root.Content[0]
	if doc.Kind != yaml.MappingNode {
		return data, nil
	}

	current := 1
	if v := mapValue(doc, "config_version"); v != nil {
		if err := v.Decode(&current); err != nil {
			current = 1
		}
	}

	if current == Version {
		return data, nil
	}
	if current > Version {
		slog.Warn("config.yaml is newer than this build supports; using it as-is",
			"file_version", current, "supported_version", Version)
		return data, nil
	}

	// Preserve the pre-migration file before touching anything.
	backupPath := fmt.Sprintf("%s.bak.v%d", path, current)
	if _, err := os.Stat(backupPath); os.IsNotExist(err) {
		if err := copyFile(path, backupPath); err != nil {
			slog.Warn("could not back up config before migration", "error", err)
		}
	}

	for v := current + 1; v <= Version; v++ {
		if migrate, ok := migrations[v]; ok {
			if err := migrate(doc); err != nil {
				return nil, fmt.Errorf("migrating config to version %d: %w", v, err)
			}
		}
		slog.Info("migrated config", "from", v-1, "to", v)
	}

	setVersionFirst(doc, Version)

	out, err := encode(&root)
	if err != nil {
		return nil, err
	}

	if err := writeAtomic(path, out); err != nil {
		slog.Warn("could not write migrated config to disk; continuing with in-memory config", "error", err)
	} else {
		slog.Info("wrote migrated config", "version", Version, "path", path)
	}
	return out, nil
}

// setVersionFirst puts config_version at the head of the document mapping so
// it is visible at the top of the file.
func setVersionFirst(doc *yaml.Node, version int) {
	deleteKey(doc, "config_version")
	key := &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: "config_version"}
	val := &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!int", Value: fmt.Sprint(version)}
	doc.Content = append([]*yaml.Node{key, val}, doc.Content...)
}

func mapValue(mapping *yaml.Node, key string) *yaml.Node {
	if mapping == nil || mapping.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(mapping.Content); i += 2 {
		if mapping.Content[i].Value == key {
			return mapping.Content[i+1]
		}
	}
	return nil
}

func deleteKey(mapping *yaml.Node, key string) {
	for i := 0; i+1 < len(mapping.Content); i += 2 {
		if mapping.Content[i].Value == key {
			mapping.Content = append(mapping.Content[:i], mapping.Content[i+2:]...)
			return
		}
	}
}

func encode(root *yaml.Node) ([]byte, error) {
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(root); err != nil {
		return nil, err
	}
	if err := enc.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func writeAtomic(path string, data []byte) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	info, err := in.Stat()
	if err != nil {
		return err
	}
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, info.Mode())
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}
