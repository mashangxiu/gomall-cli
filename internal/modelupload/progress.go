package modelupload

import (
	"fmt"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/schollz/progressbar/v3"
)

type uploadProgressReporter struct {
	out        io.Writer
	totalBytes int64
	bar        *progressbar.ProgressBar
	mu         sync.Mutex
	current    atomic.Int64
}

type progressReader struct {
	r      io.Reader
	onRead func(int64)
}

func (pr *progressReader) Read(p []byte) (int, error) {
	n, err := pr.r.Read(p)
	if n > 0 && pr.onRead != nil {
		pr.onRead(int64(n))
	}
	return n, err
}

func newUploadProgressReporter(out io.Writer, totalBytes int64) *uploadProgressReporter {
	return &uploadProgressReporter{
		out:        out,
		totalBytes: totalBytes,
	}
}

func (p *uploadProgressReporter) start() {
	if p.out == nil || p.totalBytes <= 0 {
		return
	}
	p.bar = progressbar.NewOptions64(
		p.totalBytes,
		progressbar.OptionSetWriter(p.out),
		progressbar.OptionSetDescription("LFS上传"),
		progressbar.OptionShowBytes(true),
		progressbar.OptionShowCount(),
		progressbar.OptionSetPredictTime(true),
		progressbar.OptionSetWidth(36),
		progressbar.OptionSetRenderBlankState(true),
		progressbar.OptionThrottle(120*time.Millisecond),
		progressbar.OptionSetTheme(progressbar.Theme{
			Saucer:        "█",
			SaucerPadding: "░",
			SaucerHead:    "▓",
			BarStart:      "▕",
			BarEnd:        "▏",
		}),
		progressbar.OptionOnCompletion(func() {
			_, _ = fmt.Fprint(p.out, "\n")
		}),
	)
	_ = p.bar.Set64(p.current.Load())
}

func (p *uploadProgressReporter) add(n int64) {
	if n == 0 || p.out == nil || p.totalBytes <= 0 {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()

	next := p.current.Load() + n
	if next < 0 {
		next = 0
	}
	if next > p.totalBytes {
		next = p.totalBytes
	}
	p.current.Store(next)
	if p.bar != nil {
		_ = p.bar.Set64(next)
	}
}

func (p *uploadProgressReporter) finish() {
	if p.out == nil || p.totalBytes <= 0 {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.bar != nil {
		_ = p.bar.Finish()
	}
}

func (p *uploadProgressReporter) logUploading(label string) {
	p.logLine("正在上传: %s", label)
}

func (p *uploadProgressReporter) logComplete(label string, size int64) {
	p.logLine("LFS上传完成: %s (%s)", label, formatBytes(size))
}

func (p *uploadProgressReporter) logSkipped(label string) {
	p.logLine("LFS已存在: %s", label)
}

func (p *uploadProgressReporter) logRetry(label string, attempt, maxAttempt int, wait time.Duration, err error) {
	p.logLine("LFS上传重试(%d/%d): %s，等待 %s，原因: %v", attempt, maxAttempt, label, wait, err)
}

func (p *uploadProgressReporter) logLine(format string, args ...any) {
	if p.out == nil || strings.TrimSpace(format) == "" {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.bar != nil {
		_ = p.bar.Clear()
	}
	_, _ = fmt.Fprintf(p.out, format+"\n", args...)
	if p.bar != nil {
		_ = p.bar.Set64(p.current.Load())
	}
}
