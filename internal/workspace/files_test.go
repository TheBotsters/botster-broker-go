package workspace

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func setupTestDir(t *testing.T) (string, *FileExecutor) {
	t.Helper()
	dir := t.TempDir()
	fe, err := NewFileExecutor(dir)
	if err != nil {
		t.Fatalf("NewFileExecutor: %v", err)
	}
	return dir, fe
}

func writeTestFile(t *testing.T, dir, name, content string) {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}
}

// --- NewFileExecutor ---

func TestNewFileExecutor_RelativePath(t *testing.T) {
	_, err := NewFileExecutor("relative/path")
	if err == nil {
		t.Fatal("expected error for relative path")
	}
}

func TestNewFileExecutor_NonexistentPath(t *testing.T) {
	_, err := NewFileExecutor("/nonexistent/path/that/does/not/exist")
	if err == nil {
		t.Fatal("expected error for nonexistent path")
	}
}

// --- ValidatePath ---

func TestValidatePath_RelativeOK(t *testing.T) {
	dir, fe := setupTestDir(t)
	writeTestFile(t, dir, "test.txt", "hello")

	resolved, err := fe.ValidatePath("test.txt")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.HasSuffix(resolved, "test.txt") {
		t.Errorf("expected resolved path to end with test.txt, got %s", resolved)
	}
}

func TestValidatePath_TraversalBlocked(t *testing.T) {
	_, fe := setupTestDir(t)

	_, err := fe.ValidatePath("../../etc/passwd")
	if err == nil {
		t.Fatal("expected error for path traversal")
	}
	if !strings.Contains(err.Error(), "escapes workspace root") {
		t.Errorf("expected 'escapes workspace root' error, got: %v", err)
	}
}

func TestValidatePath_AbsoluteOutside(t *testing.T) {
	_, fe := setupTestDir(t)

	_, err := fe.ValidatePath("/etc/passwd")
	if err == nil {
		t.Fatal("expected error for absolute path outside root")
	}
}

func TestValidatePath_NewFileInRoot(t *testing.T) {
	_, fe := setupTestDir(t)

	// File doesn't exist yet — should validate parent and succeed.
	resolved, err := fe.ValidatePath("newfile.txt")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.HasSuffix(resolved, "newfile.txt") {
		t.Errorf("expected resolved path to end with newfile.txt, got %s", resolved)
	}
}

func TestValidatePath_NewFileInSubdir(t *testing.T) {
	dir, fe := setupTestDir(t)

	// Create the subdirectory.
	if err := os.MkdirAll(filepath.Join(dir, "sub"), 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	resolved, err := fe.ValidatePath("sub/newfile.txt")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.HasSuffix(resolved, filepath.Join("sub", "newfile.txt")) {
		t.Errorf("expected path ending with sub/newfile.txt, got %s", resolved)
	}
}

func TestValidatePath_SymlinkEscape(t *testing.T) {
	dir, fe := setupTestDir(t)

	// Create a symlink pointing outside the root.
	outsideDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(outsideDir, "secret.txt"), []byte("secret"), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := os.Symlink(outsideDir, filepath.Join(dir, "escape")); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	_, err := fe.ValidatePath("escape/secret.txt")
	if err == nil {
		t.Fatal("expected error for symlink traversal")
	}
}

// --- Read ---

func TestRead_Basic(t *testing.T) {
	dir, fe := setupTestDir(t)
	writeTestFile(t, dir, "hello.txt", "line1\nline2\nline3")

	result, err := fe.Read("hello.txt", 0, 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Content != "line1\nline2\nline3" {
		t.Errorf("unexpected content: %q", result.Content)
	}
	if result.LinesRead != 3 {
		t.Errorf("expected 3 lines, got %d", result.LinesRead)
	}
	if result.Truncated {
		t.Error("expected not truncated")
	}
}

func TestRead_WithOffset(t *testing.T) {
	dir, fe := setupTestDir(t)
	writeTestFile(t, dir, "lines.txt", "a\nb\nc\nd\ne")

	result, err := fe.Read("lines.txt", 3, 0) // Start at line 3 (1-based)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Content != "c\nd\ne" {
		t.Errorf("unexpected content: %q", result.Content)
	}
	if result.LinesRead != 3 {
		t.Errorf("expected 3 lines, got %d", result.LinesRead)
	}
}

func TestRead_WithLimit(t *testing.T) {
	dir, fe := setupTestDir(t)
	writeTestFile(t, dir, "lines.txt", "a\nb\nc\nd\ne")

	result, err := fe.Read("lines.txt", 1, 2) // First 2 lines
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Content != "a\nb" {
		t.Errorf("unexpected content: %q", result.Content)
	}
	if result.LinesRead != 2 {
		t.Errorf("expected 2 lines, got %d", result.LinesRead)
	}
	if !result.Truncated {
		t.Error("expected truncated")
	}
}

func TestRead_OffsetBeyondEnd(t *testing.T) {
	dir, fe := setupTestDir(t)
	writeTestFile(t, dir, "short.txt", "one\ntwo")

	result, err := fe.Read("short.txt", 100, 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Content != "" {
		t.Errorf("expected empty content, got %q", result.Content)
	}
	if result.LinesRead != 0 {
		t.Errorf("expected 0 lines, got %d", result.LinesRead)
	}
}

func TestRead_FileNotFound(t *testing.T) {
	_, fe := setupTestDir(t)

	_, err := fe.Read("nonexistent.txt", 0, 0)
	if err == nil {
		t.Fatal("expected error for missing file")
	}
	if !strings.Contains(err.Error(), "file not found") {
		t.Errorf("expected 'file not found' error, got: %v", err)
	}
}

func TestRead_PathTraversal(t *testing.T) {
	_, fe := setupTestDir(t)

	_, err := fe.Read("../../etc/passwd", 0, 0)
	if err == nil {
		t.Fatal("expected error for path traversal")
	}
}

func TestRead_LineTruncation(t *testing.T) {
	dir, fe := setupTestDir(t)
	// Create a file with more lines than MaxLines.
	lines := make([]string, MaxLines+100)
	for i := range lines {
		lines[i] = "x"
	}
	writeTestFile(t, dir, "big.txt", strings.Join(lines, "\n"))

	result, err := fe.Read("big.txt", 0, 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.LinesRead != MaxLines {
		t.Errorf("expected %d lines, got %d", MaxLines, result.LinesRead)
	}
	if !result.Truncated {
		t.Error("expected truncated")
	}
}

// --- Write ---

func TestWrite_NewFile(t *testing.T) {
	dir, fe := setupTestDir(t)

	if err := fe.Write("new.txt", "hello world"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(dir, "new.txt"))
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if string(data) != "hello world" {
		t.Errorf("unexpected content: %q", string(data))
	}
}

func TestWrite_CreateParentDirs(t *testing.T) {
	dir, fe := setupTestDir(t)

	if err := fe.Write("sub/dir/file.txt", "nested"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(dir, "sub", "dir", "file.txt"))
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if string(data) != "nested" {
		t.Errorf("unexpected content: %q", string(data))
	}
}

func TestWrite_Overwrite(t *testing.T) {
	dir, fe := setupTestDir(t)
	writeTestFile(t, dir, "exists.txt", "old content")

	if err := fe.Write("exists.txt", "new content"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(dir, "exists.txt"))
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if string(data) != "new content" {
		t.Errorf("unexpected content: %q", string(data))
	}
}

func TestWrite_PathTraversal(t *testing.T) {
	_, fe := setupTestDir(t)

	err := fe.Write("../../evil.txt", "bad")
	if err == nil {
		t.Fatal("expected error for path traversal")
	}
}

// --- Edit ---

func TestEdit_Basic(t *testing.T) {
	dir, fe := setupTestDir(t)
	writeTestFile(t, dir, "edit.txt", "hello world")

	if err := fe.Edit("edit.txt", "world", "Go"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(dir, "edit.txt"))
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if string(data) != "hello Go" {
		t.Errorf("unexpected content: %q", string(data))
	}
}

func TestEdit_FirstOccurrenceOnly(t *testing.T) {
	dir, fe := setupTestDir(t)
	writeTestFile(t, dir, "dupes.txt", "foo bar foo baz foo")

	if err := fe.Edit("dupes.txt", "foo", "qux"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(dir, "dupes.txt"))
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if string(data) != "qux bar foo baz foo" {
		t.Errorf("unexpected content: %q", string(data))
	}
}

func TestEdit_OldTextNotFound(t *testing.T) {
	dir, fe := setupTestDir(t)
	writeTestFile(t, dir, "nope.txt", "hello world")

	err := fe.Edit("nope.txt", "missing", "replacement")
	if err == nil {
		t.Fatal("expected error for missing old text")
	}
	if !strings.Contains(err.Error(), "old text not found") {
		t.Errorf("expected 'old text not found' error, got: %v", err)
	}
}

func TestEdit_FileNotFound(t *testing.T) {
	_, fe := setupTestDir(t)

	err := fe.Edit("nonexistent.txt", "old", "new")
	if err == nil {
		t.Fatal("expected error for missing file")
	}
}

func TestEdit_PathTraversal(t *testing.T) {
	_, fe := setupTestDir(t)

	err := fe.Edit("../../etc/passwd", "root", "hacked")
	if err == nil {
		t.Fatal("expected error for path traversal")
	}
}

func TestEdit_MultilineText(t *testing.T) {
	dir, fe := setupTestDir(t)
	writeTestFile(t, dir, "multi.txt", "line1\nline2\nline3\nline4")

	if err := fe.Edit("multi.txt", "line2\nline3", "replaced2\nreplaced3"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(dir, "multi.txt"))
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if string(data) != "line1\nreplaced2\nreplaced3\nline4" {
		t.Errorf("unexpected content: %q", string(data))
	}
}
