package model

import (
	"strings"
	"testing"
)

func TestBuildSearchPath(t *testing.T) {
	t.Parallel()

	got := buildSearchPath("qwen", 1, 10)
	wantContains := []string{
		"/goMallApi/api/v2/models?",
		"name=qwen",
		"order_by=comprehensive",
		"page=1",
		"search_type=public",
		"size=10",
		"sort=desc",
		"task_ids=",
	}

	for _, part := range wantContains {
		if !strings.Contains(got, part) {
			t.Fatalf("path %q should contain %q", got, part)
		}
	}
}

func TestBuildCreatedPath(t *testing.T) {
	t.Parallel()

	got := buildCreatedPath("", 1, 16)
	wantContains := []string{
		"/goMallApi/api/v2/models?",
		"name=",
		"order_by=updated_at",
		"page=1",
		"search_type=create",
		"size=16",
		"sort=desc",
		"task_ids=",
	}

	for _, part := range wantContains {
		if !strings.Contains(got, part) {
			t.Fatalf("path %q should contain %q", got, part)
		}
	}
}

func TestBuildDetailPath(t *testing.T) {
	t.Parallel()

	got := buildDetailPath("gomall", "test1")
	want := "/goMallApi/api/v2/models/detail/gomall/test1"
	if got != want {
		t.Fatalf("buildDetailPath() = %q, want %q", got, want)
	}
}
