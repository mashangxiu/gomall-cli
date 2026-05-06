package gitlfs

import (
	"bytes"
	"net/url"
	"strings"
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

func TestRewriteDownloadURL_KeepPathAndQuery(t *testing.T) {
	t.Parallel()

	orig := "http://10.170.130.22/lfs-objects/a8/8b/abc?AWSAccessKeyId=gomall&Signature=xyz&Expires=1778052324"
	got, err := rewriteDownloadURL(orig, "http://my.host.cn")
	if err != nil {
		t.Fatalf("rewriteDownloadURL() error=%v", err)
	}
	u, err := url.Parse(got)
	if err != nil {
		t.Fatalf("parse got url error=%v", err)
	}
	if u.Scheme != "http" || u.Host != "my.host.cn" {
		t.Fatalf("host override failed: %s", got)
	}
	if u.Path != "/lfs-objects/a8/8b/abc" {
		t.Fatalf("path changed unexpectedly: %s", u.Path)
	}
	if u.RawQuery == "" {
		t.Fatalf("query should be kept")
	}
}

func TestRewriteDownloadURL_WithOverridePathPrefix(t *testing.T) {
	t.Parallel()

	orig := "http://10.170.130.22/lfs-objects/a8/8b/abc?x=1"
	got, err := rewriteDownloadURL(orig, "http://my.host.cn/proxy")
	if err != nil {
		t.Fatalf("rewriteDownloadURL() error=%v", err)
	}
	u, err := url.Parse(got)
	if err != nil {
		t.Fatalf("parse got url error=%v", err)
	}
	if u.Path != "/proxy/lfs-objects/a8/8b/abc" {
		t.Fatalf("path prefix override failed: %s", u.Path)
	}
}

func TestApplyDownloadURLOverride(t *testing.T) {
	t.Parallel()

	resp := map[string]batchResponseObject{
		"oid1": {
			OID: "oid1",
			Actions: map[string]batchAction{
				batchOpDownload: {Href: "http://10.1.1.1/lfs-objects/a?x=1"},
			},
		},
	}
	if err := applyDownloadURLOverride(resp, "http://my.host.cn", false, nil); err != nil {
		t.Fatalf("applyDownloadURLOverride() error=%v", err)
	}
	got := resp["oid1"].Actions[batchOpDownload].Href
	if got != "http://my.host.cn/lfs-objects/a?x=1" {
		t.Fatalf("applyDownloadURLOverride()=%s", got)
	}
}

func TestApplyDownloadURLOverride_DebugPrintAfterURL(t *testing.T) {
	t.Parallel()

	resp := map[string]batchResponseObject{
		"oid1": {
			OID: "oid1",
			Actions: map[string]batchAction{
				batchOpDownload: {Href: "http://10.1.1.1/lfs-objects/a?x=1"},
			},
		},
	}
	var buf bytes.Buffer
	if err := applyDownloadURLOverride(resp, "http://my.host.cn", true, &buf); err != nil {
		t.Fatalf("applyDownloadURLOverride() error=%v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "[DEBUG] after: http://my.host.cn/lfs-objects/a?x=1") {
		t.Fatalf("debug output missing replaced url, got=%q", out)
	}
}
