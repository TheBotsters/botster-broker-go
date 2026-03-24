// Package workspace implements broker-local file operations for agent workspaces.
//
// When an agent has a workspace_root configured, the broker handles read/write/edit
// commands directly via local disk I/O instead of routing them to an actuator.
// This eliminates a WebSocket hop for file operations on the agent's workspace.
package workspace

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const (
	// MaxFileSize is the maximum bytes returned from a read operation.
	MaxFileSize = 50 * 1024 // 50KB
	// MaxLines is the maximum number of lines returned from a read operation.
	MaxLines = 2000
)

// FileExecutor handles file operations scoped to a root directory.
// All paths are validated to prevent directory traversal.
type FileExecutor struct {
	rootPath string
}

// NewFileExecutor creates a FileExecutor rooted at the given path.
// The root path must be an absolute path.
func NewFileExecutor(rootPath string) (*FileExecutor, error) {
	if !filepath.IsAbs(rootPath) {
		return nil, fmt.Errorf("workspace root must be absolute: %s", rootPath)
	}
	// Resolve any symlinks in the root path itself.
	resolved, err := filepath.EvalSymlinks(rootPath)
	if err != nil {
		return nil, fmt.Errorf("resolve workspace root: %w", err)
	}
	return &FileExecutor{rootPath: resolved}, nil
}

// ValidatePath resolves inputPath relative to the root and ensures it doesn't escape.
// Returns the absolute resolved path or an error.
func (f *FileExecutor) ValidatePath(inputPath string) (string, error) {
	// Resolve the path relative to root.
	var candidate string
	if filepath.IsAbs(inputPath) {
		candidate = inputPath
	} else {
		candidate = filepath.Join(f.rootPath, inputPath)
	}
	candidate = filepath.Clean(candidate)

	// Evaluate symlinks to catch symlink-based traversal.
	// If the file/dir doesn't exist yet, walk up the path until we find
	// an existing ancestor and validate that.
	resolved, err := filepath.EvalSymlinks(candidate)
	if err != nil {
		if os.IsNotExist(err) {
			// Walk up to the nearest existing ancestor.
			ancestor, tail, aerr := nearestExistingAncestor(candidate)
			if aerr != nil {
				return "", fmt.Errorf("invalid path: cannot resolve ancestor: %w", aerr)
			}
			// Check that the existing ancestor is within root.
			if !isWithin(ancestor, f.rootPath) {
				return "", fmt.Errorf("invalid path: escapes workspace root")
			}
			// Reconstruct the full path with the resolved ancestor.
			return filepath.Join(ancestor, tail), nil
		}
		return "", fmt.Errorf("invalid path: %w", err)
	}

	if !isWithin(resolved, f.rootPath) {
		return "", fmt.Errorf("invalid path: escapes workspace root")
	}
	return resolved, nil
}

// nearestExistingAncestor walks up from path until it finds a directory that exists,
// resolves symlinks on that ancestor, and returns (resolvedAncestor, remainingTail, error).
func nearestExistingAncestor(path string) (string, string, error) {
	current := path
	var parts []string
	for {
		resolved, err := filepath.EvalSymlinks(current)
		if err == nil {
			tail := filepath.Join(parts...)
			return resolved, tail, nil
		}
		if !os.IsNotExist(err) {
			return "", "", err
		}
		parts = append([]string{filepath.Base(current)}, parts...)
		parent := filepath.Dir(current)
		if parent == current {
			// Reached filesystem root without finding existing dir.
			return "", "", fmt.Errorf("no existing ancestor found for %s", path)
		}
		current = parent
	}
}

// isWithin checks whether child is inside (or equal to) parent.
func isWithin(child, parent string) bool {
	// filepath.Rel returns a clean relative path; if it starts with ".."
	// the child is outside the parent.
	rel, err := filepath.Rel(parent, child)
	if err != nil {
		return false
	}
	return !strings.HasPrefix(rel, "..") && rel != ".."
}

// ReadResult holds the output of a Read operation.
type ReadResult struct {
	Content   string `json:"content"`
	LinesRead int    `json:"linesRead,omitempty"`
	Truncated bool   `json:"truncated,omitempty"`
}

// Read reads a file with optional offset (1-based line number) and line limit.
func (f *FileExecutor) Read(path string, offset, limit int) (*ReadResult, error) {
	validPath, err := f.ValidatePath(path)
	if err != nil {
		return nil, err
	}

	data, err := os.ReadFile(validPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("file not found: %s", path)
		}
		return nil, fmt.Errorf("read file: %w", err)
	}

	content := string(data)
	lines := strings.Split(content, "\n")

	// Apply offset (1-based).
	startLine := 0
	if offset > 0 {
		startLine = offset - 1
	}
	if startLine >= len(lines) {
		return &ReadResult{Content: "", LinesRead: 0}, nil
	}

	// Apply limit.
	maxLines := MaxLines
	if limit > 0 && limit < maxLines {
		maxLines = limit
	}

	endLine := startLine + maxLines
	truncated := false
	if endLine > len(lines) {
		endLine = len(lines)
	} else if endLine < len(lines) {
		truncated = true
	}

	selected := lines[startLine:endLine]
	result := strings.Join(selected, "\n")

	// Check byte size limit.
	if len(result) > MaxFileSize {
		result = result[:MaxFileSize]
		truncated = true
	}

	return &ReadResult{
		Content:   result,
		LinesRead: len(selected),
		Truncated: truncated,
	}, nil
}

// Write writes content to a file, creating parent directories as needed.
func (f *FileExecutor) Write(path string, content string) error {
	validPath, err := f.ValidatePath(path)
	if err != nil {
		return err
	}

	// Create parent directories if they don't exist.
	parentDir := filepath.Dir(validPath)
	if err := os.MkdirAll(parentDir, 0755); err != nil {
		return fmt.Errorf("create parent directories: %w", err)
	}

	if err := os.WriteFile(validPath, []byte(content), 0644); err != nil {
		return fmt.Errorf("write file: %w", err)
	}
	return nil
}

// Edit replaces the first occurrence of oldText with newText in a file.
func (f *FileExecutor) Edit(path string, oldText, newText string) error {
	validPath, err := f.ValidatePath(path)
	if err != nil {
		return err
	}

	data, err := os.ReadFile(validPath)
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("file not found: %s", path)
		}
		return fmt.Errorf("read file for edit: %w", err)
	}

	content := string(data)
	idx := strings.Index(content, oldText)
	if idx == -1 {
		return fmt.Errorf("old text not found in file")
	}

	newContent := content[:idx] + newText + content[idx+len(oldText):]

	if err := os.WriteFile(validPath, []byte(newContent), 0644); err != nil {
		return fmt.Errorf("write edited file: %w", err)
	}
	return nil
}
