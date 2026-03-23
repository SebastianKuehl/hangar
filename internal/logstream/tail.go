package logstream

import (
	"bufio"
	"context"
	"io"
	"os"
	"strings"
	"time"
)

type LogEvent struct {
	Lines []string
	Err   error
	Reset bool
}

type Tailer struct {
	pollInterval time.Duration
}

func NewTailer(pollInterval time.Duration) *Tailer {
	if pollInterval <= 0 {
		pollInterval = 250 * time.Millisecond
	}
	return &Tailer{pollInterval: pollInterval}
}

func (t *Tailer) TailFile(ctx context.Context, path string, fromEnd bool, lines int, out chan<- LogEvent) error {
	ticker := time.NewTicker(t.pollInterval)
	defer ticker.Stop()

	loadedInitial := false
	var offset int64
	var partial string

	for {
		select {
		case <-ctx.Done():
			return nil
		default:
		}

		info, err := os.Stat(path)
		if err != nil {
			if os.IsNotExist(err) {
				select {
				case <-ctx.Done():
					return nil
				case <-ticker.C:
					continue
				}
			}
			select {
			case <-ctx.Done():
				return nil
			case out <- LogEvent{Err: err}:
			}
			return err
		}

		if !loadedInitial {
			initialLines, snapshotSize, err := readSnapshotLines(path, fromEnd, lines)
			if err != nil {
				select {
				case <-ctx.Done():
					return nil
				case out <- LogEvent{Err: err}:
				}
				return err
			}
			if len(initialLines) > 0 {
				select {
				case <-ctx.Done():
					return nil
				case out <- LogEvent{Lines: initialLines, Reset: true}:
				}
			}
			offset = snapshotSize
			loadedInitial = true
		} else if info.Size() < offset {
			resetLines, snapshotSize, err := readSnapshotLines(path, true, lines)
			if err != nil {
				select {
				case <-ctx.Done():
					return nil
				case out <- LogEvent{Err: err}:
				}
				return err
			}
			partial = ""
			offset = snapshotSize
			select {
			case <-ctx.Done():
				return nil
			case out <- LogEvent{Lines: resetLines, Reset: true}:
			}
		}

		if info.Size() > offset {
			linesOut, newOffset, newPartial, err := readAppendedLines(path, offset, partial)
			if err != nil {
				select {
				case <-ctx.Done():
					return nil
				case out <- LogEvent{Err: err}:
				}
				return err
			}
			offset = newOffset
			partial = newPartial
			if len(linesOut) > 0 {
				select {
				case <-ctx.Done():
					return nil
				case out <- LogEvent{Lines: linesOut}:
				}
			}
		}

		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}

func readSnapshotLines(path string, fromEnd bool, count int) ([]string, int64, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, 0, err
	}
	defer file.Close()

	info, err := file.Stat()
	if err != nil {
		return nil, 0, err
	}
	size := info.Size()
	lines, err := readLines(io.LimitReader(file, size))
	if err != nil {
		return nil, 0, err
	}
	if !fromEnd || count <= 0 || len(lines) <= count {
		return lines, size, nil
	}
	return append([]string(nil), lines[len(lines)-count:]...), size, nil
}

func readLines(reader io.Reader) ([]string, error) {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	lines := []string{}
	for scanner.Scan() {
		lines = append(lines, scanner.Text())
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return lines, nil
}

func readAppendedLines(path string, offset int64, partial string) ([]string, int64, string, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, offset, partial, err
	}
	defer file.Close()

	if _, err := file.Seek(offset, 0); err != nil {
		return nil, offset, partial, err
	}
	data, err := io.ReadAll(file)
	if err != nil {
		return nil, offset, partial, err
	}
	text := partial + string(data)
	parts := strings.Split(text, "\n")
	if len(parts) == 0 {
		return nil, offset + int64(len(data)), partial, nil
	}

	lines := make([]string, 0, len(parts))
	for _, line := range parts[:len(parts)-1] {
		lines = append(lines, strings.TrimSuffix(line, "\r"))
	}
	return lines, offset + int64(len(data)), parts[len(parts)-1], nil
}
