package tool

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func resolvePath(root, requested string) (string, string, error) {
	if strings.TrimSpace(requested) == "" {
		requested = "."
	}

	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return "", "", fmt.Errorf("resolve repository root: %w", err)
	}

	rootReal, err := filepath.EvalSymlinks(rootAbs)
	if err != nil {
		return "", "", fmt.Errorf("resolve repository root: %w", err)
	}

	target := requested
	if !filepath.IsAbs(target) {
		target = filepath.Join(rootAbs, target)
	}

	target, err = filepath.Abs(target)
	if err != nil {
		return "", "", fmt.Errorf("resolve path: %w", err)
	}

	lexicalRel, err := filepath.Rel(rootAbs, target)
	if err != nil || lexicalRel == ".." || strings.HasPrefix(lexicalRel, ".."+string(filepath.Separator)) {
		return "", "", fmt.Errorf("path is outside the repository: %s", requested)
	}

	realTarget, err := filepath.EvalSymlinks(target)
	if err != nil {
		if !os.IsNotExist(err) {
			return "", "", fmt.Errorf("resolve path: %w", err)
		}

		return "", "", fmt.Errorf("path does not exist: %s", requested)
	}

	rel, err := filepath.Rel(rootReal, realTarget)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", "", fmt.Errorf("path is outside the repository: %s", requested)
	}

	if rel == "." {
		return realTarget, ".", nil
	}

	return realTarget, filepath.ToSlash(rel), nil
}

func resolveMutationPath(root, requested string) (string, string, error) {
	if strings.TrimSpace(requested) == "" {
		return "", "", fmt.Errorf("path is required")
	}

	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return "", "", fmt.Errorf("resolve repository root: %w", err)
	}

	rootReal, err := filepath.EvalSymlinks(rootAbs)
	if err != nil {
		return "", "", fmt.Errorf("resolve repository root: %w", err)
	}

	target := requested
	if !filepath.IsAbs(target) {
		target = filepath.Join(rootAbs, target)
	}

	target, err = filepath.Abs(target)
	if err != nil {
		return "", "", fmt.Errorf("resolve path: %w", err)
	}

	lexicalRel, err := filepath.Rel(rootAbs, target)
	if err != nil || lexicalRel == ".." || strings.HasPrefix(lexicalRel, ".."+string(filepath.Separator)) {
		return "", "", fmt.Errorf("path is outside the repository: %s", requested)
	}

	ancestor := target
	for {
		realAncestor, resolveErr := filepath.EvalSymlinks(ancestor)
		if resolveErr == nil {
			realRel, relErr := filepath.Rel(rootReal, realAncestor)
			if relErr != nil || realRel == ".." || strings.HasPrefix(realRel, ".."+string(filepath.Separator)) {
				return "", "", fmt.Errorf("path is outside the repository: %s", requested)
			}

			break
		}

		if !os.IsNotExist(resolveErr) {
			return "", "", fmt.Errorf("resolve path: %w", resolveErr)
		}

		parent := filepath.Dir(ancestor)
		if parent == ancestor {
			return "", "", fmt.Errorf("could not resolve path parent: %s", requested)
		}

		ancestor = parent
	}

	return target, filepath.ToSlash(lexicalRel), nil
}
