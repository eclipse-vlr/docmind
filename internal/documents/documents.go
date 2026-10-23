package documents

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"
)

const MaxFileSize = 10 << 20

var (
	ErrInvalid       = errors.New("invalid document")
	ErrUnsupported   = errors.New("unsupported document format")
	ErrTooLarge      = errors.New("document too large")
	ErrNotFound      = errors.New("document not found")
	ErrUnavailable   = errors.New("document service unavailable")
	ErrCommitUnknown = errors.New("transaction outcome unknown")
)

type Document struct {
	ID           string    `json:"id"`
	OriginalName string    `json:"original_name"`
	MIMEType     string    `json:"mime_type"`
	SizeBytes    int64     `json:"size_bytes"`
	StorageKey   string    `json:"-"`
	Status       string    `json:"status"`
	ErrorMessage string    `json:"error_message,omitempty"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

func NewID() string {
	var b [16]byte
	_, _ = rand.Read(b[:]) // crypto/rand.Read either fills the buffer or terminates the process.
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[:4], b[4:6], b[6:8], b[8:10], b[10:])
}

func ValidID(id string) bool {
	if len(id) != 36 || id[8] != '-' || id[13] != '-' || id[18] != '-' || id[23] != '-' || id != strings.ToLower(id) {
		return false
	}
	decoded, err := hex.DecodeString(strings.ReplaceAll(id, "-", ""))
	return err == nil && len(decoded) == 16
}
