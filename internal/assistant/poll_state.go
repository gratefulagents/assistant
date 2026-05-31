// SPDX-License-Identifier: GPL-3.0-only

package assistant

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
)

func stateFilePath(cfg appConfig, name string) string {
	dir := cfg.StateDir
	if dir == "" {
		dir = defaultStateDir()
	}
	return filepath.Join(dir, name)
}

func readJSONFile(path string, target any) (bool, error) {
	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, err
	}
	defer f.Close()
	if err := json.NewDecoder(f).Decode(target); err != nil {
		return true, err
	}
	return true, nil
}

func writeJSONFile(path string, value any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp := path + ".tmp"
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
