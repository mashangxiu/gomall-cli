package cmd

import "testing"

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

func TestCloneAcceptsIDInput(t *testing.T) {
	t.Parallel()

	if id, ok := tryParsePositiveInt64("1700"); !ok || id != 1700 {
		t.Fatalf("clone input id parse failed: (%d, %t)", id, ok)
	}
}
