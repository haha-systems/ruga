package tool

import (
	"fmt"
	"os"
	"path/filepath"
)

func absoluteRoot(root string) (string, error) {
	absolute, err := filepath.Abs(root)
	if err != nil {
		return "", fmt.Errorf("resolve repository root: %w", err)
	}

	info, err := os.Stat(absolute)
	if err != nil {
		return "", fmt.Errorf("stat repository root: %w", err)
	}

	if !info.IsDir() {
		return "", fmt.Errorf("repository root is not a directory: %s", absolute)
	}

	return absolute, nil
}
