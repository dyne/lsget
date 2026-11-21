package lsget

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path"
	"regexp"
	"strconv"
	"strings"
)

var errGrepFileTooLarge = errors.New("file too large")

type grepSearch struct {
	pattern     string
	folded      *regexp.Regexp
	lineNumbers bool
	reader      *bufio.Reader
	output      boundedText
}

func newGrepSearch(pattern string, ignoreCase, lineNumbers bool) *grepSearch {
	search := &grepSearch{
		pattern:     pattern,
		lineNumbers: lineNumbers,
		reader:      bufio.NewReaderSize(nil, maxGrepLine+2), // Include CRLF.
	}
	if ignoreCase {
		search.folded = regexp.MustCompile("(?i)" + regexp.QuoteMeta(pattern))
	}
	return search
}

func (s *grepSearch) file(ctx context.Context, budget *workBudget, root *os.Root, name string, showFilename bool) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	file, info, err := openVisibleFile(root, name)
	if err != nil {
		return err
	}
	defer func() { _ = file.Close() }()
	if info.Size() > maxGrepFile {
		s.output.truncated = true
		return errGrepFileTooLarge
	}

	limited := &io.LimitedReader{R: contextReader{ctx, file}, N: maxGrepFile + 1}
	s.reader.Reset(scanBudgetReader{limited, budget})
	sample, err := s.reader.Peek(4096)
	if err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	if !looksText(sample) {
		return nil
	}

	lineNumber := 1
	skipping := false
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		line, readErr := s.reader.ReadSlice('\n')
		if limited.N == 0 {
			s.output.truncated = true
			return errGrepFileTooLarge
		}
		if errors.Is(readErr, bufio.ErrBufferFull) {
			skipping = true
			s.output.truncated = true
			continue
		}
		if readErr != nil && !errors.Is(readErr, io.EOF) {
			return readErr
		}
		if !skipping && len(line) > 0 {
			line = bytes.TrimSuffix(bytes.TrimSuffix(line, []byte("\n")), []byte("\r"))
			if len(line) > maxGrepLine {
				s.output.truncated = true
			} else if err := s.match(line, name, lineNumber, showFilename); err != nil {
				return err
			}
		}
		if readErr != nil {
			return nil
		}
		skipping = false
		lineNumber++
	}
}

func (s *grepSearch) match(line []byte, name string, number int, showFilename bool) error {
	var span []int
	var matches int
	if s.folded != nil {
		span = s.folded.FindIndex(line)
		if span == nil {
			return nil
		}
		matches = 1
	} else {
		matches = bytes.Count(line, []byte(s.pattern))
		if matches == 0 {
			return nil
		}
	}

	var label string
	if showFilename {
		label = colorCyan + cleanVirtual(name) + colorReset + ":"
	}
	if s.lineNumbers {
		label += colorGreen + strconv.Itoa(number) + colorReset + ":"
	}
	if len(label)+len(line)+matches*len(colorYellow+colorBold+colorReset) > s.output.remaining() {
		return errWorkLimit
	}
	if span != nil {
		return s.output.line(label + string(line[:span[0]]) + colorYellow + colorBold +
			string(line[span[0]:span[1]]) + colorReset + string(line[span[1]:]))
	}
	highlighted := strings.ReplaceAll(string(line), s.pattern, colorYellow+colorBold+s.pattern+colorReset)
	return s.output.line(label + highlighted)
}

func (s *grepSearch) directory(ctx context.Context, budget *workBudget, root *os.Root, dir string) error {
	return walkVisibleDir(ctx, budget, root, dir, func(entry visibleEntry) (bool, error) {
		if strings.HasPrefix(path.Base(entry.name), ".") {
			return false, nil
		}
		if entry.info.Mode().IsRegular() {
			if err := s.file(ctx, budget, root, entry.name, true); stopWork(err) {
				return false, err
			}
		}
		return true, nil
	})
}
