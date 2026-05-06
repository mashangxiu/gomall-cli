package gitlfs

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"time"
)

func formatBytes(n int64) string {
	if n < 0 {
		n = 0
	}
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%dB", n)
	}
	div, exp := int64(unit), 0
	for v := n / unit; v >= unit && exp < 5; v /= unit {
		div *= unit
		exp++
	}
	value := float64(n) / float64(div)
	suffixes := []string{"KB", "MB", "GB", "TB", "PB", "EB"}
	return fmt.Sprintf("%.1f%s", value, suffixes[exp])
}

func formatDuration(d time.Duration) string {
	if d <= 0 {
		return "0s"
	}
	sec := int64(d.Round(time.Second) / time.Second)
	h := sec / 3600
	m := (sec % 3600) / 60
	s := sec % 60
	if h > 0 {
		return fmt.Sprintf("%dh%dm%ds", h, m, s)
	}
	if m > 0 {
		return fmt.Sprintf("%dm%ds", m, s)
	}
	return fmt.Sprintf("%ds", s)
}

func classifyRetryReason(err error) string {
	if err == nil {
		return "unknown"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "timeout"
	}
	if errors.Is(err, context.Canceled) {
		return "canceled"
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return "timeout"
	}

	msg := strings.ToLower(err.Error())
	switch {
	case strings.Contains(msg, "idle timeout"):
		return "idle timeout"
	case strings.Contains(msg, "http status 401"):
		return "http 401 unauthorized"
	case strings.Contains(msg, "http status 403"):
		return "http 403 forbidden"
	case strings.Contains(msg, "http status 404"):
		return "http 404 not found"
	case strings.Contains(msg, "connection reset by peer"):
		return "connection reset"
	case strings.Contains(msg, "broken pipe"):
		return "broken pipe"
	case strings.Contains(msg, "timeout"):
		return "timeout"
	default:
		return strings.TrimSpace(err.Error())
	}
}
