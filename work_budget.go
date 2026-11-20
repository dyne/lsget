package main

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path"
	"slices"
	"strings"
)

const (
	maxWorkEntries   = 4096
	maxWorkPathBytes = 1024 * 1024
	maxWalkDepth     = 128
	maxCommandText   = 256 * 1024
	maxGrepFile      = 10 * 1024 * 1024
	maxGrepBytes     = 32 * 1024 * 1024
	maxGrepLine      = 64 * 1024
	maxWorkJobs      = 8
	maxSitemapBytes  = 8 * 1024 * 1024
	truncationNote   = "[results truncated by server limits]"
)

var (
	errWorkLimit = errors.New("filesystem work limit exceeded")
	errWorkBusy  = errors.New("filesystem work busy, retry shortly")
)

type workBudget struct {
	entries   int
	pathBytes int
	scanBytes int64
}

func newWorkBudget() *workBudget {
	return &workBudget{entries: maxWorkEntries, pathBytes: maxWorkPathBytes, scanBytes: maxGrepBytes}
}

func (b *workBudget) childName(dir, base string) (string, error) {
	name := path.Join(dir, base)
	if len(name) > b.pathBytes {
		return "", errWorkLimit
	}
	b.pathBytes -= len(name)
	return name, nil
}

func (s *server) acquireWork(ctx context.Context) (func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	select {
	case s.workSlots <- struct{}{}:
		return func() { <-s.workSlots }, nil
	default:
		return nil, errWorkBusy
	}
}

func (s *server) reserveWork(w http.ResponseWriter, r *http.Request) func() {
	release, err := s.acquireWork(r.Context())
	if errors.Is(err, errWorkBusy) {
		writeWorkBusy(w)
	}
	return release
}

func writeWorkBusy(w http.ResponseWriter) {
	w.Header().Set("Retry-After", "1")
	http.Error(w, errWorkBusy.Error(), http.StatusServiceUnavailable)
}

func stopWork(err error) bool {
	return errors.Is(err, errWorkLimit) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}

func (b *workBudget) readEntries(ctx context.Context, directory *os.File) ([]fs.DirEntry, error) {
	var entries []fs.DirEntry
	var result error
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		// One lookahead entry distinguishes a full budget from actual truncation.
		batch, err := directory.ReadDir(max(1, min(128, b.entries)))
		if len(batch) > b.entries {
			result = errWorkLimit
			break
		}
		b.entries -= len(batch)
		entries = append(entries, batch...)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, err
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	slices.SortFunc(entries, func(a, b fs.DirEntry) int { return strings.Compare(a.Name(), b.Name()) })
	return entries, result
}

type boundedText struct {
	text      strings.Builder
	truncated bool
}

func (o *boundedText) remaining() int {
	return maxCommandText - o.text.Len() - len(truncationNote) - 1
}

func (o *boundedText) line(line string) error {
	separator := 0
	if o.text.Len() != 0 {
		separator = 1
	}
	if len(line)+separator > o.remaining() {
		return errWorkLimit
	}
	if separator != 0 {
		o.text.WriteByte('\n')
	}
	o.text.WriteString(line)
	return nil
}

func (o *boundedText) response(fallback string, err error) execResp {
	output := o.text.String()
	truncated := o.truncated || errors.Is(err, errWorkLimit)
	if truncated {
		if output != "" {
			output += "\n"
		}
		output += truncationNote
	} else if output == "" {
		output = fallback
	}
	return execResp{Output: output, Truncated: truncated}
}

type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r contextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(p)
}

type contextWriter struct {
	ctx    context.Context
	writer io.Writer
}

func (w contextWriter) Write(p []byte) (int, error) {
	if err := w.ctx.Err(); err != nil {
		return 0, err
	}
	return w.writer.Write(p)
}

type scanBudgetReader struct {
	reader io.Reader
	budget *workBudget
}

func (r scanBudgetReader) Read(p []byte) (int, error) {
	if r.budget.scanBytes == 0 {
		return 0, errWorkLimit
	}
	n, err := r.reader.Read(p[:min(int64(len(p)), r.budget.scanBytes)])
	r.budget.scanBytes -= int64(n)
	return n, err
}
