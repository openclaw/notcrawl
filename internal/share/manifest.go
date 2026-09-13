package share

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func validateManifest(repoPath string, manifest Manifest) error {
	if err := validateManifestShape(manifest); err != nil {
		return err
	}
	for _, table := range manifest.Tables {
		if err := validateManifestFile(repoPath, table.Path); err != nil {
			return fmt.Errorf("snapshot table %s: %w", table.Name, err)
		}
	}
	if manifest.RecordSources != nil {
		if err := validateManifestFile(repoPath, manifest.RecordSources.Path); err != nil {
			return fmt.Errorf("record_sources snapshot: %w", err)
		}
	}
	return nil
}

func validateManifestShape(manifest Manifest) error {
	expected := make(map[string]bool, len(exportTables))
	for _, table := range exportTables {
		expected[table] = true
	}
	seen := make(map[string]bool, len(manifest.Tables))
	for _, table := range manifest.Tables {
		if table.Rows < 0 {
			return fmt.Errorf("snapshot table %s has negative row count", table.Name)
		}
		if !expected[table.Name] {
			return fmt.Errorf("unsupported snapshot table %q", table.Name)
		}
		if seen[table.Name] {
			return fmt.Errorf("duplicate snapshot table %q", table.Name)
		}
		seen[table.Name] = true
		if err := validateRelativeSnapshotPath(table.Path); err != nil {
			return fmt.Errorf("snapshot table %s: %w", table.Name, err)
		}
	}
	for _, table := range exportTables {
		if !seen[table] {
			return fmt.Errorf("snapshot manifest missing required table %q", table)
		}
	}
	if manifest.RecordSources != nil {
		if manifest.RecordSources.Rows < 0 {
			return fmt.Errorf("record_sources has negative row count")
		}
		if manifest.RecordSources.Name != "record_sources" {
			return fmt.Errorf("invalid record_sources table name %q", manifest.RecordSources.Name)
		}
		if err := validateRelativeSnapshotPath(manifest.RecordSources.Path); err != nil {
			return fmt.Errorf("record_sources snapshot: %w", err)
		}
	}
	return nil
}

func validateRelativeSnapshotPath(value string) error {
	clean := filepath.Clean(strings.TrimSpace(value))
	if clean == "." || clean == ".." || filepath.IsAbs(clean) || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return fmt.Errorf("path escapes snapshot repository: %s", value)
	}
	return nil
}

func validateManifestFile(repoPath, path string) error {
	if path == "" {
		return fmt.Errorf("missing path")
	}
	root, err := filepath.Abs(repoPath)
	if err != nil {
		return err
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		return err
	}
	full, err := filepath.Abs(filepath.Join(repoPath, path))
	if err != nil {
		return err
	}
	info, err := os.Lstat(full)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("symlink snapshot path is not allowed: %s", path)
	}
	resolved, err := filepath.EvalSymlinks(full)
	if err != nil {
		return err
	}
	rel, err := filepath.Rel(root, resolved)
	if err != nil {
		return err
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return fmt.Errorf("path escapes snapshot repository: %s", path)
	}
	info, err = os.Stat(resolved)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("not a regular file: %s", path)
	}
	return nil
}
