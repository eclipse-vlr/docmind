package llm

import (
	"context"
	"errors"
	"math"
	"reflect"
	"strings"
	"testing"
)

func TestFakeDeterministicAndOrdered(t *testing.T) {
	p := Fake{}
	texts := []string{"PostgreSQL database transactions", "cats and dogs", "!!!", "База данных"}
	batch, err := p.Embed(context.Background(), texts)
	if err != nil {
		t.Fatal(err)
	}
	for i, text := range texts {
		one, err := p.Embed(context.Background(), []string{text})
		if err != nil || !reflect.DeepEqual(batch[i], one[0]) || ValidateVector(batch[i]) != nil {
			t.Fatalf("vector %d: inconsistent or invalid: %v", i, err)
		}
		var norm float64
		for _, v := range batch[i] {
			norm += float64(v) * float64(v)
		}
		if math.Abs(norm-1) > 1e-6 {
			t.Fatalf("norm squared=%v", norm)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := p.Embed(ctx, texts); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func TestEmbeddingInputBounds(t *testing.T) {
	for _, texts := range [][]string{nil, {""}, {" \n"}, {"\xff"}, {strings.Repeat("x", MaxInputBytes+1)}, make([]string, BatchSize+1)} {
		if _, err := (Fake{}).Embed(context.Background(), texts); !errors.Is(err, ErrInput) {
			t.Fatalf("invalid input accepted: %v", err)
		}
	}
	if _, err := (Fake{}).Embed(context.Background(), []string{strings.Repeat("x", MaxInputBytes)}); err != nil {
		t.Fatal(err)
	}
}

func TestVectorRejectsInvalidValues(t *testing.T) {
	valid := make([]float32, Dimensions)
	valid[0] = 1
	if err := ValidateVector(valid); err != nil {
		t.Fatal(err)
	}
	for _, value := range []float32{0, float32(math.NaN()), float32(math.Inf(1)), float32(math.Inf(-1))} {
		v := make([]float32, Dimensions)
		v[0] = value
		if !errors.Is(ValidateVector(v), ErrResponse) {
			t.Fatal("invalid vector accepted")
		}
	}
	if !errors.Is(ValidateVector(valid[:Dimensions-1]), ErrResponse) {
		t.Fatal("wrong dimension accepted")
	}
}
