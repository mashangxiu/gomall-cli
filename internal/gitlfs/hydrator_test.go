package gitlfs

import (
	"testing"
	"time"
)

func TestParsePointer(t *testing.T) {
	t.Parallel()

	raw := []byte("version https://git-lfs.github.com/spec/v1\noid sha256:93de6f2ca6450f8d9695f2524f7397246394ee0d0ebf1963be88365ef9f2f5d7\nsize 42\n")
	oid, size, ok := parsePointer(raw)
	if !ok {
		t.Fatalf("parsePointer() should succeed")
	}
	if oid != "93de6f2ca6450f8d9695f2524f7397246394ee0d0ebf1963be88365ef9f2f5d7" {
		t.Fatalf("parsePointer() oid=%s", oid)
	}
	if size != 42 {
		t.Fatalf("parsePointer() size=%d", size)
	}
}

func TestParsePointerInvalid(t *testing.T) {
	t.Parallel()

	cases := [][]byte{
		[]byte(""),
		[]byte("version https://git-lfs.github.com/spec/v1\n"),
		[]byte("version https://git-lfs.github.com/spec/v1\noid sha256:abc\nsize 1\n"),
		[]byte("version https://git-lfs.github.com/spec/v1\noid sha256:93de6f2ca6450f8d9695f2524f7397246394ee0d0ebf1963be88365ef9f2f5d7\nsize -1\n"),
	}
	for _, c := range cases {
		if _, _, ok := parsePointer(c); ok {
			t.Fatalf("parsePointer(%q) should fail", string(c))
		}
	}
}

func TestBuildBatchURL(t *testing.T) {
	t.Parallel()

	got, err := buildBatchURL("http://10.208.61.121:8090/Qwen/Qwen2.5-3B-Instruct.git")
	if err != nil {
		t.Fatalf("buildBatchURL() error=%v", err)
	}
	want := "http://10.208.61.121:8090/Qwen/Qwen2.5-3B-Instruct.git/info/lfs/objects/batch"
	if got != want {
		t.Fatalf("buildBatchURL()=%s, want=%s", got, want)
	}
}

func TestFormatBytes(t *testing.T) {
	t.Parallel()

	cases := map[int64]string{
		0:               "0B",
		999:             "999B",
		1024:            "1.0KB",
		1536:            "1.5KB",
		5 * 1024 * 1024: "5.0MB",
	}
	for in, want := range cases {
		got := formatBytes(in)
		if got != want {
			t.Fatalf("formatBytes(%d)=%s, want=%s", in, got, want)
		}
	}
}

func TestFormatDuration(t *testing.T) {
	t.Parallel()

	cases := []struct {
		in   time.Duration
		want string
	}{
		{0, "0s"},
		{5 * time.Second, "5s"},
		{65 * time.Second, "1m5s"},
		{3665 * time.Second, "1h1m5s"},
	}
	for _, c := range cases {
		got := formatDuration(c.in)
		if got != c.want {
			t.Fatalf("formatDuration(%s)=%s, want=%s", c.in, got, c.want)
		}
	}
}
