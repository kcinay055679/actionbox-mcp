package broker

import (
	"bytes"
	"errors"
	"io"
	"os"
	"unicode/utf8"

	"actionbox/internal/jobrun"
	"actionbox/internal/protocol"
)

func tail(path string) (string, error) {
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return "", err
	}
	offset := max(0, info.Size()-protocol.TailBytes)
	buf := make([]byte, info.Size()-offset)
	n, err := f.ReadAt(buf, offset)
	if err != nil && !errors.Is(err, io.EOF) {
		return "", err
	}
	buf = buf[:n]
	for offset > 0 && len(buf) > 0 && !utf8.RuneStart(buf[0]) {
		buf = buf[1:]
	}

	end := len(buf)
	if end > 0 && buf[end-1] == '\n' {
		end--
	}
	for range protocol.TailLines {
		i := bytes.LastIndexByte(buf[:end], '\n')
		if i < 0 {
			return string(buf), nil
		}
		end = i
	}
	return string(buf[end+1:]), nil
}

func (b *Broker) prune(project string) {
	active := func(id string) bool {
		b.mu.Lock()
		defer b.mu.Unlock()
		_, ok := b.active[activeKey(project, id)]
		return ok
	}
	if err := jobrun.Prune(b.opts.Env.Paths, project, b.retention, b.opts.Now(), active); err != nil {
		b.display("%s: prune: %v", project, err)
	}
}
