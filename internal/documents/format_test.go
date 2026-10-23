package documents

import (
	"bytes"
	"errors"
	"testing"
)

func TestValidateUpload(t *testing.T) {
	for _, tc := range []struct {
		name, filename string
		body           []byte
		mime           string
		err            error
	}{
		{"txt", "a.txt", []byte("Привет\n"), "text/plain", nil},
		{"markdown", "a.MD", []byte("# hello"), "text/markdown", nil},
		{"path only metadata", `C:\fakepath\a.txt`, []byte("text"), "text/plain", nil},
		{"pdf", "a.pdf", []byte("%PDF-1.7\n1 0 obj\n<<>>\nendobj\n%%EOF\n"), "application/pdf", nil},
		{"empty", "a.txt", nil, "", ErrInvalid},
		{"invalid name", "a\x00.txt", []byte("text"), "", ErrInvalid},
		{"binary txt", "a.txt", []byte{0, 1, 2}, "", ErrUnsupported},
		{"invalid utf8", "a.md", []byte{0xff}, "", ErrUnsupported},
		{"forged pdf", "a.pdf", []byte("hello"), "", ErrUnsupported},
		{"broken pdf trailer", "a.pdf", []byte("%PDF-1.7\nhello"), "", ErrUnsupported},
		{"pdf named txt", "a.txt", []byte("%PDF-1.7\n%%EOF"), "", ErrUnsupported},
		{"unsupported extension", "a.exe", []byte("text"), "", ErrUnsupported},
		{"exact limit", "a.txt", bytes.Repeat([]byte("a"), MaxFileSize), "text/plain", nil},
		{"above limit", "a.txt", bytes.Repeat([]byte("a"), MaxFileSize+1), "", ErrTooLarge},
	} {
		t.Run(tc.name, func(t *testing.T) {
			name, mime, err := ValidateUpload(tc.filename, tc.body)
			if !errors.Is(err, tc.err) || mime != tc.mime {
				t.Fatalf("name=%q mime=%q err=%v", name, mime, err)
			}
			if err == nil && tc.name == "path only metadata" && name != "a.txt" {
				t.Fatal(name)
			}
		})
	}
}

func TestIDs(t *testing.T) {
	a, b := NewID(), NewID()
	if a == b || !ValidID(a) || !ValidID(b) {
		t.Fatal("invalid random IDs")
	}
	for _, id := range []string{"", "../escape", a + "/file", "00000000000000000000000000000000000000", "00000000-0000-0000-0000-0000000000--"} {
		if ValidID(id) {
			t.Fatal("invalid ID accepted")
		}
	}
}
