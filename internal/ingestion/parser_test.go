package ingestion

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestTextChunking(t *testing.T) {
	text := strings.Repeat("Я", 1800) + "конец"
	chunks, err := (TextParser{}).Parse(context.Background(), "text/markdown", []byte(text))
	if err != nil {
		t.Fatal(err)
	}
	if len(chunks) != 3 {
		t.Fatalf("chunks=%d", len(chunks))
	}
	for _, c := range chunks {
		if c.Page != nil || !utf8.ValidString(c.Text) || len([]rune(c.Text)) > ChunkRunes {
			t.Fatalf("invalid chunk: %+v", c)
		}
	}
	runes := []rune(text)
	if chunks[0].Text != string(runes[:1000]) || chunks[1].Text != string(runes[800:1800]) || chunks[2].Text != string(runes[1600:]) {
		t.Fatal("overlap or tail lost")
	}
}

func TestParserFailures(t *testing.T) {
	for _, tc := range []struct {
		name, mime string
		data       []byte
		want       error
	}{
		{"whitespace", "text/plain", []byte(" \t\r\n"), ErrEmpty},
		{"invalid UTF8", "text/plain", []byte{255}, ErrInvalid},
		{"binary", "text/plain", []byte("a\x00b"), ErrInvalid},
		{"oversized text", "text/plain", []byte(strings.Repeat("a", MaxTextBytes+1)), ErrLimit},
		{"unsupported", "image/png", []byte("a"), ErrInvalid},
		{"broken PDF", "application/pdf", []byte("%PDF-1.7\nnot a pdf\n%%EOF"), ErrInvalid},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := (TextParser{}).Parse(context.Background(), tc.mime, tc.data)
			if !errors.Is(err, tc.want) {
				t.Fatalf("got %v want %v", err, tc.want)
			}
		})
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := (TextParser{}).Parse(ctx, "text/plain", []byte("a")); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

// Real PDFs with an xref table exercise the external parser, page boundaries and empty text layers.
func TestPDFPages(t *testing.T) {
	p := TextParser{}
	if err := p.Check(); err != nil {
		t.Fatal(err)
	}
	chunks, err := p.Parse(context.Background(), "application/pdf", testPDF("first page", "second page"))
	if err != nil || len(chunks) != 2 {
		t.Fatalf("chunks=%v error=%v", chunks, err)
	}
	for i, want := range []string{"first page", "second page"} {
		if chunks[i].Page == nil || *chunks[i].Page != i+1 || strings.TrimSpace(chunks[i].Text) != want {
			t.Fatalf("page %d: %+v", i, chunks[i])
		}
	}
	if _, err := p.Parse(context.Background(), "application/pdf", testPDF("")); !errors.Is(err, ErrEmpty) {
		t.Fatalf("empty PDF: %v", err)
	}
}

func testPDF(pages ...string) []byte {
	objects := []string{"<< /Type /Catalog /Pages 2 0 R >>", "", "<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>"}
	var kids []string
	for _, text := range pages {
		n := len(objects) + 1
		kids = append(kids, fmt.Sprintf("%d 0 R", n))
		stream := fmt.Sprintf("BT /F1 12 Tf 72 720 Td (%s) Tj ET", text)
		objects = append(objects, fmt.Sprintf("<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Resources << /Font << /F1 3 0 R >> >> /Contents %d 0 R >>", n+1), fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(stream), stream))
	}
	objects[1] = fmt.Sprintf("<< /Type /Pages /Kids [%s] /Count %d >>", strings.Join(kids, " "), len(pages))
	var out strings.Builder
	out.WriteString("%PDF-1.4\n")
	offsets := make([]int, len(objects))
	for i, obj := range objects {
		offsets[i] = out.Len()
		fmt.Fprintf(&out, "%d 0 obj\n%s\nendobj\n", i+1, obj)
	}
	xref := out.Len()
	fmt.Fprintf(&out, "xref\n0 %d\n0000000000 65535 f \n", len(objects)+1)
	for _, offset := range offsets {
		fmt.Fprintf(&out, "%010d 00000 n \n", offset)
	}
	fmt.Fprintf(&out, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(objects)+1, xref)
	return []byte(out.String())
}

func TestLimitedBuffer(t *testing.T) {
	b := &limitedBuffer{limit: 3}
	if _, err := b.Write([]byte("abc")); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Write([]byte("d")); !errors.Is(err, ErrLimit) || !b.exceeded || b.String() != "abc" {
		t.Fatal("output limit not enforced")
	}
}
