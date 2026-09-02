package modelupload

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/filemode"
)

func TestLFSPointer(t *testing.T) {
	t.Parallel()

	got := lfsPointer("abc123", 42)
	want := "version https://git-lfs.github.com/spec/v1\noid sha256:abc123\nsize 42\n"
	if got != want {
		t.Fatalf("lfsPointer() = %q, want %q", got, want)
	}
}

func TestBuildGitAttributes(t *testing.T) {
	t.Parallel()

	content := buildGitAttributes("*.md text\n", []lfsObject{
		{entry: fileEntry{rel: "weights/model.safetensors"}},
	})
	if !strings.Contains(content, "*.md text\n") {
		t.Fatalf("existing attributes not kept: %q", content)
	}
	if !strings.Contains(content, "\"/weights/model.safetensors\" filter=lfs diff=lfs merge=lfs -text") {
		t.Fatalf("lfs attribute missing: %q", content)
	}
}

func TestFileMode(t *testing.T) {
	t.Parallel()

	if got := fileMode(0o644); got != filemode.Regular {
		t.Fatalf("fileMode(0644) = %q", got)
	}
	if got := fileMode(0o755); got != filemode.Executable {
		t.Fatalf("fileMode(0755) = %q", got)
	}
}

func TestShouldUseChunked(t *testing.T) {
	t.Parallel()

	if !shouldUseChunked(map[string]string{"Transfer-Encoding": "chunked"}) {
		t.Fatal("expected chunked transfer to be detected")
	}
	if !shouldUseChunked(map[string]string{"transfer-encoding": " Chunked "}) {
		t.Fatal("expected case-insensitive chunked transfer to be detected")
	}
	if shouldUseChunked(map[string]string{"Transfer-Encoding": "gzip"}) {
		t.Fatal("unexpected chunked detection")
	}
}

func TestResolveUploadRootFollowsSymlinkDirectory(t *testing.T) {
	t.Parallel()

	tmp := t.TempDir()
	realRoot := filepath.Join(tmp, "real-model")
	if err := os.Mkdir(realRoot, 0o755); err != nil {
		t.Fatalf("Mkdir(realRoot) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(realRoot, "README.md"), []byte("hello"), 0o644); err != nil {
		t.Fatalf("WriteFile(README.md) error = %v", err)
	}
	linkRoot := filepath.Join(tmp, "linked-model")
	if err := os.Symlink(realRoot, linkRoot); err != nil {
		t.Skipf("Symlink not supported: %v", err)
	}

	root, err := resolveUploadRoot(linkRoot)
	if err != nil {
		t.Fatalf("resolveUploadRoot() error = %v", err)
	}
	wantRoot, err := filepath.EvalSymlinks(realRoot)
	if err != nil {
		t.Fatalf("EvalSymlinks(realRoot) error = %v", err)
	}
	if root != wantRoot {
		t.Fatalf("resolveUploadRoot() = %q, want %q", root, wantRoot)
	}
	files, err := scanFiles(root)
	if err != nil {
		t.Fatalf("scanFiles() error = %v", err)
	}
	if len(files) != 1 || files[0].rel != "README.md" {
		t.Fatalf("scanFiles() = %#v, want README.md only", files)
	}
	for _, f := range files {
		if f.rel == "." {
			t.Fatalf("scanFiles() included illegal dot path: %#v", files)
		}
	}
}

func TestRewriteTransferURL(t *testing.T) {
	t.Parallel()

	got, err := rewriteTransferURL(
		"http://10.170.130.22/lfs-objects/a8/8b/file?AWSAccessKeyId=gomall&Signature=x",
		"http://my.host.cn/proxy",
	)
	if err != nil {
		t.Fatalf("rewriteTransferURL() error=%v", err)
	}
	want := "http://my.host.cn/proxy/lfs-objects/a8/8b/file?AWSAccessKeyId=gomall&Signature=x"
	if got != want {
		t.Fatalf("rewriteTransferURL()=%q, want %q", got, want)
	}
}

func TestMergeIndexEntries(t *testing.T) {
	t.Parallel()

	remoteKeep := plumbing.NewHash("1111111111111111111111111111111111111111")
	remoteOld := plumbing.NewHash("2222222222222222222222222222222222222222")
	localNew := plumbing.NewHash("3333333333333333333333333333333333333333")

	got := mergeIndexEntries(
		[]indexEntry{
			{path: "README.md", mode: filemode.Regular, hash: remoteKeep},
			{path: "weights.bin", mode: filemode.Regular, hash: remoteOld},
		},
		[]indexEntry{
			{path: "weights.bin", mode: filemode.Regular, hash: localNew},
			{path: "config.json", mode: filemode.Regular, hash: localNew},
		},
	)
	if len(got) != 3 {
		t.Fatalf("len(mergeIndexEntries())=%d, want 3", len(got))
	}
	if got[0].path != "README.md" || got[1].path != "config.json" || got[2].path != "weights.bin" {
		t.Fatalf("entries not sorted by path: %#v", got)
	}
	if got[2].hash != localNew {
		t.Fatalf("local overlay did not replace remote entry")
	}
}
