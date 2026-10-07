package cli

import (
	"fmt"
	"io"
	"strings"
)

var progressLineActive bool

func progressLine(w io.Writer, label string, done, total int) {
	if w == nil {
		return
	}
	progressLineActive = true
	if total <= 0 {
		total = 1
	}
	if done < 0 {
		done = 0
	}
	if done > total {
		done = total
	}
	const width = 24
	filled := done * width / total
	bar := strings.Repeat("#", filled) + strings.Repeat("-", width-filled)
	pct := done * 100 / total
	_, _ = fmt.Fprintf(w, "\r%-16s [%s] %3d%% (%d/%d)", label, bar, pct, done, total)
}

func progressDone(w io.Writer) {
	if w == nil {
		return
	}
	progressLineActive = false
	_, _ = fmt.Fprintln(w)
}
