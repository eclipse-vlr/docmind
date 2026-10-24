package ingestion

import (
	"bytes"
	"context"
	"errors"
	"os/exec"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// TextParser uses Poppler in a separate, resource-limited process for untrusted PDFs.
// No shell, user-supplied paths, temporary files, OCR or external URLs are used.
type TextParser struct{}

func (TextParser) Check() error {
	for _, name := range []string{"pdftotext", "prlimit"} {
		if _, err := exec.LookPath(name); err != nil {
			return errors.New("worker requires pdftotext and prlimit")
		}
	}
	return nil
}

func (TextParser) Parse(ctx context.Context, mime string, data []byte) ([]Chunk, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if len(data) == 0 || len(data) > 10<<20 {
		return nil, ErrLimit
	}
	var text string
	pdf := mime == "application/pdf"
	switch mime {
	case "text/plain", "text/markdown":
		if len(data) > MaxTextBytes {
			return nil, ErrLimit
		}
		text = string(data)
	case "application/pdf":
		output := &limitedBuffer{limit: MaxTextBytes}
		cmd := exec.CommandContext(ctx, "prlimit", "--as=536870912", "--cpu=20", "--fsize=0", "--nofile=32", "--", "pdftotext", "-enc", "UTF-8", "-eol", "unix", "-", "-")
		cmd.Stdin = bytes.NewReader(data)
		cmd.Stdout = output
		cmd.Stderr = &limitedBuffer{limit: 4096} // Diagnostics can contain document text; discard them.
		cmd.WaitDelay = time.Second
		err := cmd.Run()
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if output.exceeded {
			return nil, ErrLimit
		}
		if err != nil {
			return nil, ErrInvalid
		}
		text = output.String()
	default:
		return nil, ErrInvalid
	}
	if !utf8.ValidString(text) {
		return nil, ErrInvalid
	}
	for _, r := range text {
		if unicode.IsControl(r) && r != '\n' && r != '\r' && r != '\t' && !(pdf && r == '\f') {
			return nil, ErrInvalid
		}
	}
	pages := []string{text}
	if pdf {
		pages = strings.Split(text, "\f")
	}
	var chunks []Chunk
	for p, page := range pages {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		runes := []rune(strings.TrimSpace(strings.TrimPrefix(page, "\ufeff")))
		for start := 0; start < len(runes); start += ChunkRunes - OverlapRunes {
			end := min(start+ChunkRunes, len(runes))
			part := strings.TrimSpace(string(runes[start:end]))
			if part != "" {
				if len(chunks) >= MaxChunks {
					return nil, ErrLimit
				}
				c := Chunk{Text: part}
				if pdf {
					n := p + 1
					c.Page = &n
				}
				chunks = append(chunks, c)
			}
			if end == len(runes) {
				break
			}
		}
	}
	if len(chunks) == 0 {
		return nil, ErrEmpty
	}
	return chunks, nil
}

type limitedBuffer struct {
	bytes.Buffer
	limit    int
	exceeded bool
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	if len(p) > b.limit-b.Len() {
		b.exceeded = true
		return 0, ErrLimit
	}
	return b.Buffer.Write(p)
}
