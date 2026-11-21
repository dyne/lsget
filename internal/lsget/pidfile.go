package lsget

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
)

type ownedPIDFile struct {
	file    *os.File
	pid     string
	written bool
}

func claimPIDFile(name string) (*ownedPIDFile, error) {
	file, err := os.OpenFile(name, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o644)
	if errors.Is(err, os.ErrExist) {
		return nil, fmt.Errorf("%w; verify the previous process has exited before removing a stale PID file", err)
	}
	if err != nil {
		return nil, err
	}
	pid := &ownedPIDFile{file: file, pid: strconv.Itoa(os.Getpid())}
	if _, err := file.WriteString(pid.pid); err != nil {
		return nil, errors.Join(err, pid.remove())
	}
	pid.written = true
	return pid, nil
}

func (p *ownedPIDFile) remove() error {
	name := p.file.Name()
	owned, err := p.stillOwned()
	closeErr := p.file.Close()
	if err != nil || closeErr != nil {
		return fmt.Errorf("failed to check PID file %q: %w", name, errors.Join(err, closeErr))
	}
	// Close before removal for systems that require a closed file handle.
	if owned {
		if err := os.Remove(name); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("failed to remove PID file: %w", err)
		}
	}
	return nil
}

func (p *ownedPIDFile) stillOwned() (bool, error) {
	if p.written {
		contents := make([]byte, len(p.pid)+1)
		n, err := p.file.ReadAt(contents, 0)
		if err != nil && !errors.Is(err, io.EOF) {
			return false, err
		}
		if string(contents[:n]) != p.pid {
			return false, nil
		}
	}
	original, err := p.file.Stat()
	if err != nil {
		return false, err
	}
	current, err := os.Lstat(p.file.Name())
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return os.SameFile(original, current), nil
}
