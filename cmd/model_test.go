package cmd

import (
	"os"
	"path/filepath"
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

	restored, err := restoreMissingTrackedFiles(repoDir, tree)
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
