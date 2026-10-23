package documents

import (
	"bytes"
	"path"
	"strings"
	"unicode"
	"unicode/utf8"
)

// ValidateUpload ignores the untrusted MIME header and checks actual bytes.
func ValidateUpload(filename string, body []byte) (string, string, error) {
	name := path.Base(strings.ReplaceAll(filename, "\\", "/"))
	if name == "." || name == "/" || len(name) == 0 || len(name) > 255 || !utf8.ValidString(name) {
		return "", "", ErrInvalid
	}
	for _, r := range name {
		if unicode.IsControl(r) {
			return "", "", ErrInvalid
		}
	}
	if len(body) > MaxFileSize {
		return "", "", ErrTooLarge
	}
	if len(body) == 0 {
		return "", "", ErrInvalid
	}
	ext := strings.ToLower(path.Ext(name))
	if ext != ".pdf" && ext != ".txt" && ext != ".md" {
		return "", "", ErrUnsupported
	}
	if ext == ".pdf" {
		headerOK := len(body) >= 8 && (bytes.HasPrefix(body, []byte("%PDF-1.")) && body[7] >= '0' && body[7] <= '7' || bytes.HasPrefix(body, []byte("%PDF-2.0")))
		tail := body
		if len(tail) > 1024 {
			tail = tail[len(tail)-1024:]
		}
		if !headerOK || !bytes.HasSuffix(bytes.TrimSpace(tail), []byte("%%EOF")) {
			return "", "", ErrUnsupported
		}
		// Full structural validation/extraction belongs to the ingestion parser.
		return name, "application/pdf", nil
	}
	if !utf8.Valid(body) || bytes.HasPrefix(body, []byte("%PDF-")) {
		return "", "", ErrUnsupported
	}
	for _, r := range string(body) {
		if unicode.IsControl(r) && r != '\n' && r != '\r' && r != '\t' {
			return "", "", ErrUnsupported
		}
	}
	mime := "text/plain"
	if ext == ".md" {
		mime = "text/markdown"
	}
	return name, mime, nil
}
