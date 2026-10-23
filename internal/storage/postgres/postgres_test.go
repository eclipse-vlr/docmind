package postgres

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestOpenDoesNotDiscloseInvalidConnectionString(t *testing.T) {
	_, err := Open(context.Background(), "postgres://user:secret@host:invalid/db", time.Second)
	if err == nil {
		t.Fatal("expected invalid connection string error")
	}
	if strings.Contains(err.Error(), "secret") {
		t.Fatal("error disclosed credentials")
	}
}
