package gitlfs

import (
	"fmt"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/schollz/progressbar/v3"
)

type progressReporter struct {
	out        io.Writer
	totalBytes int64
	bar        *progressbar.ProgressBar
	mu         sync.Mutex
	current    atomic.Int64
}

func newProgressReporter(out io.Writer, totalBytes int64) *progressReporter {
	return &progressReporter{
		out:        out,
		totalBytes: totalBytes,
	}
}

func (p *progressReporter) start() {
	if p.out == nil || p.totalBytes <= 0 {
		return
	}

	p.bar = progressbar.NewOptions64(
		p.totalBytes,
		progressbar.OptionSetWriter(p.out),
		progressbar.OptionSetDescription("LFS下载"),
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

func (p *progressReporter) add(n int64) {
	if n == 0 {
		return
	}
	if p.out == nil || p.totalBytes <= 0 {
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

func (p *progressReporter) finish() {
	if p.out == nil || p.totalBytes <= 0 {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.bar != nil {
		_ = p.bar.Finish()
	}
}

func (p *progressReporter) logDownloading(label string) {
	if p.out == nil || strings.TrimSpace(label) == "" {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.bar != nil {
		_ = p.bar.Clear()
	}
	_, _ = fmt.Fprintf(p.out, "正在下载: %s\n", label)
	if p.bar != nil {
		_ = p.bar.Set64(p.current.Load())
	}
}

func (p *progressReporter) logRetry(label string, attempt int, wait time.Duration, err error) {
	if p.out == nil || strings.TrimSpace(label) == "" {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.bar != nil {
		_ = p.bar.Clear()
	}
	_, _ = fmt.Fprintf(
		p.out,
		"下载重试(%d): %s, 原因: %s, 等待 %s\n",
		attempt,
		label,
		classifyRetryReason(err),
		formatDuration(wait),
	)
	if p.bar != nil {
		_ = p.bar.Set64(p.current.Load())
	}
}

func (p *progressReporter) logLocalHit(label string) {
	if p.out == nil || strings.TrimSpace(label) == "" {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.bar != nil {
		_ = p.bar.Clear()
	}
	_, _ = fmt.Fprintf(p.out, "本地断点已完整: %s（跳过网络下载）\n", label)
	if p.bar != nil {
		_ = p.bar.Set64(p.current.Load())
	}
}
