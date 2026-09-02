package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	gogit "github.com/go-git/go-git/v5"
	ggconfig "github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing/object"
)

func TestSplitModelRef(t *testing.T) {
	t.Parallel()

	author, name, err := splitModelRef("gomall/test1")
	if err != nil {
		t.Fatalf("splitModelRef() error = %v", err)
	}
	if author != "gomall" || name != "test1" {
		t.Fatalf("splitModelRef() = %q/%q", author, name)
	}
}

func TestSplitModelRefInvalid(t *testing.T) {
	t.Parallel()

	cases := []string{"", "gomall", "/test", "gomall/"}
	for _, c := range cases {
		_, _, err := splitModelRef(c)
		if err == nil {
			t.Fatalf("splitModelRef(%q) should fail", c)
		}
	}
}

func TestDeriveRepoDirName(t *testing.T) {
	t.Parallel()

	got := deriveRepoDirName("http://10.208.61.121:8090/Qwen/Qwen2.5-3B-Instruct.git", "fallback")
	if got != "Qwen2.5-3B-Instruct" {
		t.Fatalf("deriveRepoDirName() = %q", got)
	}
}

func TestIsCloneRepoURL(t *testing.T) {
	t.Parallel()

	if !isCloneRepoURL("https://example.com/group/model.git") {
		t.Fatalf("https repo URL should be accepted")
	}
	if isCloneRepoURL("gomall/test1") {
		t.Fatalf("model ref should not be treated as repo URL")
	}
}

func TestResolveCloneTarget(t *testing.T) {
	t.Parallel()

	target, err := resolveCloneTarget(
		"http://10.208.61.121:8090/Qwen/Qwen2.5-3B-Instruct.git",
		"Qwen2.5-3B-Instruct",
		"./downloads",
		"",
	)
	if err != nil {
		t.Fatalf("resolveCloneTarget() error = %v", err)
	}
	if target != "downloads/Qwen2.5-3B-Instruct" {
		t.Fatalf("resolveCloneTarget() = %q", target)
	}
}

func TestTryParsePositiveInt64(t *testing.T) {
	t.Parallel()

	id, ok := tryParsePositiveInt64("1700")
	if !ok || id != 1700 {
		t.Fatalf("tryParsePositiveInt64() = (%d, %t), want (1700, true)", id, ok)
	}
}

func TestTryParsePositiveInt64Invalid(t *testing.T) {
	t.Parallel()

	cases := []string{"", "  ", "gomall/test1", "abc", "-1", "0", "1a"}
	for _, c := range cases {
		if _, ok := tryParsePositiveInt64(c); ok {
			t.Fatalf("tryParsePositiveInt64(%q) should be invalid", c)
		}
	}
}

func TestDeleteAcceptsIDInput(t *testing.T) {
	t.Parallel()

	id, ok := tryParsePositiveInt64("2546")
	if !ok || id != 2546 {
		t.Fatalf("delete input id parse failed: (%d, %t)", id, ok)
	}
}

func TestIsYesInput(t *testing.T) {
	t.Parallel()

	if !isYesInput("y") {
		t.Fatalf("isYesInput(y) should be true")
	}
	if !isYesInput("yes") {
		t.Fatalf("isYesInput(yes) should be true")
	}
	if !isYesInput(" YES \n") {
		t.Fatalf("isYesInput(YES) should be true")
	}
	if isYesInput("n") {
		t.Fatalf("isYesInput(n) should be false")
	}
	if isYesInput("") {
		t.Fatalf("isYesInput(empty) should be false")
	}
}

func TestCloneAcceptsIDInput(t *testing.T) {
	t.Parallel()

	if id, ok := tryParsePositiveInt64("1700"); !ok || id != 1700 {
		t.Fatalf("clone input id parse failed: (%d, %t)", id, ok)
	}
}

func TestCloneTokenFlagsExist(t *testing.T) {
	t.Parallel()

	cmd := newModelCloneCmd()
	if cmd.Flags().Lookup("token") == nil {
		t.Fatalf("clone command should expose --token")
	}
	if cmd.Flags().Lookup("token-stdin") == nil {
		t.Fatalf("clone command should expose --token-stdin")
	}
	if cmd.Flags().Lookup("file") == nil {
		t.Fatalf("clone command should expose --file")
	}
}

func TestReadTokenFromStdin(t *testing.T) {
	t.Parallel()

	cmd := newModelCloneCmd()
	cmd.SetIn(strings.NewReader("  test-token  \n"))
	got, err := readTokenFromStdin(cmd)
	if err != nil {
		t.Fatalf("readTokenFromStdin() error = %v", err)
	}
	if got != "test-token" {
		t.Fatalf("readTokenFromStdin() = %q, want %q", got, "test-token")
	}
}

func TestUploadExistingModelRefParsing(t *testing.T) {
	t.Parallel()

	if id, ok := tryParsePositiveInt64("2546"); !ok || id != 2546 {
		t.Fatalf("upload model id parse failed: (%d, %t)", id, ok)
	}
	author, name, err := splitModelRef("gomall/demomodel")
	if err != nil {
		t.Fatalf("splitModelRef() error = %v", err)
	}
	if author != "gomall" || name != "demomodel" {
		t.Fatalf("splitModelRef() = %q/%q", author, name)
	}
}

func TestVisibilityText(t *testing.T) {
	t.Parallel()

	if got := visibilityText(1); got != "私有(1)" {
		t.Fatalf("visibilityText(1) = %q", got)
	}
	if got := visibilityText(5); got != "公开(5)" {
		t.Fatalf("visibilityText(5) = %q", got)
	}
	if got := visibilityText(9); got != "9" {
		t.Fatalf("visibilityText(9) = %q", got)
	}
}

func TestSameRepoURL(t *testing.T) {
	t.Parallel()

	if !sameRepoURL("http://a/b/c.git", "http://a/b/c") {
		t.Fatalf("sameRepoURL() should match .git suffix")
	}
	if !sameRepoURL("HTTP://A/B/C.git/", "http://a/b/c") {
		t.Fatalf("sameRepoURL() should ignore case and trailing slash")
	}
	if sameRepoURL("http://a/b/c", "http://a/b/d") {
		t.Fatalf("sameRepoURL() should detect mismatch")
	}
}

func TestNormalizeCloneIncludePaths(t *testing.T) {
	t.Parallel()

	got := normalizeCloneIncludePaths([]string{
		" ./model-00005-of-00015.safetensors ",
		"model-00005-of-00015.safetensors",
		"/tmp/model-00006-of-00015.safetensors",
	})
	want := []string{"model-00005-of-00015.safetensors", "model-00006-of-00015.safetensors"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("normalizeCloneIncludePaths() = %#v, want %#v", got, want)
	}
}

func TestClonePathMatches(t *testing.T) {
	t.Parallel()

	if !clonePathMatches("weights/model-00005-of-00015.safetensors", []string{"model-00005-of-00015.safetensors"}) {
		t.Fatalf("basename include should match nested file")
	}
	if !clonePathMatches("weights/model-00005-of-00015.safetensors", []string{"weights/model-00005-of-00015.safetensors"}) {
		t.Fatalf("relative include should match exact path")
	}
	if clonePathMatches("weights/model-00006-of-00015.safetensors", []string{"weights/model-00005-of-00015.safetensors"}) {
		t.Fatalf("different relative path should not match")
	}
}

func TestEnsureResumableRepo(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	repo, err := gogit.PlainInit(dir, false)
	if err != nil {
		t.Fatalf("PlainInit() error = %v", err)
	}
	_, err = repo.CreateRemote(&ggconfig.RemoteConfig{
		Name: gogit.DefaultRemoteName,
		URLs: []string{"http://gomall.ac.cn/a/b.git"},
	})
	if err != nil {
		t.Fatalf("CreateRemote() error = %v", err)
	}

	if err := ensureResumableRepo(dir, "http://gomall.ac.cn/a/b"); err != nil {
		t.Fatalf("ensureResumableRepo() should succeed, got %v", err)
	}
	if err := ensureResumableRepo(dir, "http://gomall.ac.cn/a/c"); err == nil {
		t.Fatalf("ensureResumableRepo() should fail for mismatched repo")
	}
}

func TestRestoreMissingTrackedFiles_DoesNotOverwriteExistingFiles(t *testing.T) {
	t.Parallel()

	repoDir := t.TempDir()
	repo, err := gogit.PlainInit(repoDir, false)
	if err != nil {
		t.Fatalf("PlainInit() error = %v", err)
	}

	trackedPointer := []byte("version https://git-lfs.github.com/spec/v1\noid sha256:abc\nsize 3\n")
	if err := os.WriteFile(filepath.Join(repoDir, "model.bin"), trackedPointer, 0o644); err != nil {
		t.Fatalf("WriteFile(model.bin) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(repoDir, "README.md"), []byte("hello"), 0o644); err != nil {
		t.Fatalf("WriteFile(README.md) error = %v", err)
	}

	wt, err := repo.Worktree()
	if err != nil {
		t.Fatalf("Worktree() error = %v", err)
	}
	if _, err := wt.Add("model.bin"); err != nil {
		t.Fatalf("Add(model.bin) error = %v", err)
	}
	if _, err := wt.Add("README.md"); err != nil {
		t.Fatalf("Add(README.md) error = %v", err)
	}
	if _, err := wt.Commit("init", &gogit.CommitOptions{
		Author: &object.Signature{Name: "test", Email: "test@example.com"},
	}); err != nil {
		t.Fatalf("Commit() error = %v", err)
	}

	head, err := repo.Head()
	if err != nil {
		t.Fatalf("Head() error = %v", err)
	}
	commit, err := repo.CommitObject(head.Hash())
	if err != nil {
		t.Fatalf("CommitObject() error = %v", err)
	}
	tree, err := commit.Tree()
	if err != nil {
		t.Fatalf("Tree() error = %v", err)
	}

	// Simulate hydrated LFS content already present locally.
	hydrated := []byte{0x01, 0x02, 0x03, 0x04}
	if err := os.WriteFile(filepath.Join(repoDir, "model.bin"), hydrated, 0o644); err != nil {
		t.Fatalf("overwrite model.bin error = %v", err)
	}
	// Simulate missing normal git file that should be recovered.
	if err := os.Remove(filepath.Join(repoDir, "README.md")); err != nil {
		t.Fatalf("Remove(README.md) error = %v", err)
	}

	restored, err := restoreMissingTrackedFiles(repoDir, tree, nil)
	if err != nil {
		t.Fatalf("restoreMissingTrackedFiles() error = %v", err)
	}
	if restored != 1 {
		t.Fatalf("restored = %d, want 1", restored)
	}

	gotHydrated, err := os.ReadFile(filepath.Join(repoDir, "model.bin"))
	if err != nil {
		t.Fatalf("ReadFile(model.bin) error = %v", err)
	}
	if string(gotHydrated) != string(hydrated) {
		t.Fatalf("model.bin should keep hydrated content, got %q", string(gotHydrated))
	}

	gotReadme, err := os.ReadFile(filepath.Join(repoDir, "README.md"))
	if err != nil {
		t.Fatalf("ReadFile(README.md) error = %v", err)
	}
	if string(gotReadme) != "hello" {
		t.Fatalf("README.md = %q, want %q", string(gotReadme), "hello")
	}
}

func TestRestoreMissingTrackedFilesWithInclude(t *testing.T) {
	t.Parallel()

	repoDir := t.TempDir()
	repo, err := gogit.PlainInit(repoDir, false)
	if err != nil {
		t.Fatalf("PlainInit() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(repoDir, "keep.bin"), []byte("keep"), 0o644); err != nil {
		t.Fatalf("WriteFile(keep.bin) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(repoDir, "want.bin"), []byte("want"), 0o644); err != nil {
		t.Fatalf("WriteFile(want.bin) error = %v", err)
	}
	wt, err := repo.Worktree()
	if err != nil {
		t.Fatalf("Worktree() error = %v", err)
	}
	if _, err := wt.Add("keep.bin"); err != nil {
		t.Fatalf("Add(keep.bin) error = %v", err)
	}
	if _, err := wt.Add("want.bin"); err != nil {
		t.Fatalf("Add(want.bin) error = %v", err)
	}
	if _, err := wt.Commit("init", &gogit.CommitOptions{
		Author: &object.Signature{Name: "test", Email: "test@example.com"},
	}); err != nil {
		t.Fatalf("Commit() error = %v", err)
	}
	head, err := repo.Head()
	if err != nil {
		t.Fatalf("Head() error = %v", err)
	}
	commit, err := repo.CommitObject(head.Hash())
	if err != nil {
		t.Fatalf("CommitObject() error = %v", err)
	}
	tree, err := commit.Tree()
	if err != nil {
		t.Fatalf("Tree() error = %v", err)
	}
	if err := os.Remove(filepath.Join(repoDir, "keep.bin")); err != nil {
		t.Fatalf("Remove(keep.bin) error = %v", err)
	}
	if err := os.Remove(filepath.Join(repoDir, "want.bin")); err != nil {
		t.Fatalf("Remove(want.bin) error = %v", err)
	}

	restored, err := restoreMissingTrackedFiles(repoDir, tree, []string{"want.bin"})
	if err != nil {
		t.Fatalf("restoreMissingTrackedFiles() error = %v", err)
	}
	if restored != 1 {
		t.Fatalf("restored = %d, want 1", restored)
	}
	if _, err := os.Stat(filepath.Join(repoDir, "want.bin")); err != nil {
		t.Fatalf("want.bin should be restored: %v", err)
	}
	if _, err := os.Stat(filepath.Join(repoDir, "keep.bin")); !os.IsNotExist(err) {
		t.Fatalf("keep.bin should remain missing, err=%v", err)
	}
}
