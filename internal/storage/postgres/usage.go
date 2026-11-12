package postgres

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"docmind/internal/usage"
	"github.com/jackc/pgx/v5"
)

func (s *Store) ObserveUsage(ctx context.Context, id int64, model, requestID string, duration time.Duration, status int) error {
	if len(model) > 256 || len(requestID) > 200 || status < 0 || status > 599 {
		return usage.ErrUnavailable
	}
	_, err := s.pool.Exec(ctx, `UPDATE ai_usage_calls SET model=$2,request_id=$3,duration_ms=$4,http_status=$5,prompt_version=CASE WHEN kind='chat' THEN 'rag-v1' ELSE 'chunk-v1' END WHERE id=$1`, id, model, requestID, max(duration.Milliseconds(), 0), status)
	return err
}

func (s *Store) AIStats(ctx context.Context) (string, error) {
	var pending, unknown, recorded, errorsCount, latency, retries, checkpoints, queued, oldest int64
	err := s.pool.QueryRow(ctx, `SELECT count(*) FILTER(WHERE status='pending'),count(*) FILTER(WHERE status='unknown'),count(*) FILTER(WHERE status='recorded'),count(*) FILTER(WHERE http_status<>200 AND http_status<>0),COALESCE(sum(duration_ms),0) FROM ai_usage_calls`).Scan(&pending, &unknown, &recorded, &errorsCount, &latency)
	if err != nil {
		return "", err
	}
	err = s.pool.QueryRow(ctx, `SELECT COALESCE(sum(GREATEST(attempts-1,0)),0),count(*) FILTER(WHERE status='queued'),COALESCE(max(EXTRACT(epoch FROM clock_timestamp()-created_at)) FILTER(WHERE status='queued'),0)::bigint FROM ingestion_jobs`).Scan(&retries, &queued, &oldest)
	if err != nil {
		return "", err
	}
	if err = s.pool.QueryRow(ctx, `SELECT count(*) FROM embedding_checkpoints`).Scan(&checkpoints); err != nil {
		return "", err
	}
	var out strings.Builder
	for _, v := range []struct {
		name  string
		value int64
	}{{"calls_pending", pending}, {"calls_unknown", unknown}, {"calls_recorded", recorded}, {"provider_errors", errorsCount}, {"provider_duration_milliseconds_sum", latency}, {"ingestion_retries", retries}, {"embedding_checkpoints", checkpoints}, {"ingestion_queued", queued}, {"ingestion_oldest_seconds", oldest}} {
		fmt.Fprintf(&out, "docmind_ai_%s %d\n", v.name, v.value)
	}
	return out.String(), nil
}

// ensureUsageDay serializes admissions across API, worker and CLI. Lowering a
// limit takes effect immediately; raising it takes effect on the next UTC day.
func ensureUsageDay(ctx context.Context, tx pgx.Tx, limit int64) (string, error) {
	var day string
	err := tx.QueryRow(ctx, `INSERT INTO ai_usage_days(day,limit_nanorub)
 VALUES ((clock_timestamp() AT TIME ZONE 'UTC')::date,$1)
 ON CONFLICT(day) DO UPDATE SET limit_nanorub=LEAST(ai_usage_days.limit_nanorub,EXCLUDED.limit_nanorub)
 RETURNING day::text`, limit).Scan(&day)
	return day, err
}

func (s *Store) ReserveUsage(ctx context.Context, limit int64, request usage.Request) (int64, error) {
	if limit <= 0 || limit > 1_000_000*usage.NanoRub || request.Reserved <= 0 || request.Reserved > 1_000_000*usage.NanoRub || request.InputRate <= 0 || request.InputRate > 1_000_000_000 || request.OutputRate < 0 || request.OutputRate > 1_000_000_000 {
		return 0, usage.ErrUnavailable
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer rollback(tx)
	day, err := ensureUsageDay(ctx, tx, limit)
	if err != nil {
		return 0, err
	}
	result, err := tx.Exec(ctx, `UPDATE ai_usage_days SET reserved_nanorub=reserved_nanorub+$2
 WHERE day=$1::date AND estimated_nanorub+reserved_nanorub<=limit_nanorub-$2
 AND NOT EXISTS (SELECT 1 FROM ai_usage_calls WHERE day=$1::date AND estimated_nanorub>reserved_nanorub)`, day, request.Reserved)
	if err != nil {
		return 0, err
	}
	if result.RowsAffected() != 1 {
		// Persist any lowered policy, including when this request itself is denied.
		if err := tx.Commit(ctx); err != nil {
			return 0, err
		}
		return 0, usage.ErrBudget
	}
	var id int64
	err = tx.QueryRow(ctx, `INSERT INTO ai_usage_calls(day,kind,reserved_nanorub,input_rate,output_rate)
 VALUES($1::date,$2,$3,$4,$5) RETURNING id`, day, request.Kind, request.Reserved, request.InputRate, request.OutputRate).Scan(&id)
	if err != nil {
		return 0, err
	}
	if err = tx.Commit(ctx); err != nil {
		return 0, err
	}
	return id, nil
}

func (s *Store) FinishUsage(ctx context.Context, id int64, tokens *usage.Tokens) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer rollback(tx)
	var day, status string
	var reserved, inputRate, outputRate int64
	// Lock order matches admission: day, then individual request. Read the
	// immutable day first without a lock, then lock it before locking the call.
	if err = tx.QueryRow(ctx, `SELECT day::text FROM ai_usage_calls WHERE id=$1`, id).Scan(&day); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `SELECT 1 FROM ai_usage_days WHERE day=$1::date FOR UPDATE`, day); err != nil {
		return err
	}
	err = tx.QueryRow(ctx, `SELECT status,reserved_nanorub,input_rate,output_rate FROM ai_usage_calls WHERE id=$1 FOR UPDATE`, id).Scan(&status, &reserved, &inputRate, &outputRate)
	if err != nil {
		return err
	}
	if status != "pending" {
		return nil
	}
	if tokens == nil {
		if _, err = tx.Exec(ctx, `UPDATE ai_usage_calls SET status='unknown',finished_at=clock_timestamp() WHERE id=$1`, id); err != nil {
			return err
		}
	} else {
		if tokens.Input < 0 || tokens.Output < 0 || tokens.Input > usage.MaxTokens || tokens.Output > usage.MaxTokens {
			return errors.New("invalid token accounting")
		}
		cost := tokens.Input*inputRate + tokens.Output*outputRate
		if _, err = tx.Exec(ctx, `UPDATE ai_usage_calls SET status='recorded',input_tokens=$2,output_tokens=$3,estimated_nanorub=$4,finished_at=clock_timestamp() WHERE id=$1`, id, tokens.Input, tokens.Output, cost); err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, `UPDATE ai_usage_days SET reserved_nanorub=reserved_nanorub-$2,estimated_nanorub=estimated_nanorub+$3 WHERE day=$1::date`, day, reserved, cost); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

func (s *Store) UsageSnapshot(ctx context.Context, limit int64) (usage.Snapshot, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return usage.Snapshot{}, err
	}
	defer rollback(tx)
	day, err := ensureUsageDay(ctx, tx, limit)
	if err != nil {
		return usage.Snapshot{}, err
	}
	var result usage.Snapshot
	result.Day = day
	err = tx.QueryRow(ctx, `SELECT limit_nanorub,estimated_nanorub,reserved_nanorub FROM ai_usage_days WHERE day=$1::date`, day).Scan(&result.Limit, &result.Estimated, &result.Reserved)
	if err != nil {
		return result, err
	}
	err = tx.QueryRow(ctx, `SELECT count(*),count(*) FILTER(WHERE status='recorded'),count(*) FILTER(WHERE status='unknown'),count(*) FILTER(WHERE status='pending'),
 COALESCE(sum(input_tokens) FILTER(WHERE kind='chat'),0),COALESCE(sum(output_tokens),0),COALESCE(sum(input_tokens) FILTER(WHERE kind<>'chat'),0),
 count(*) FILTER(WHERE estimated_nanorub>reserved_nanorub) FROM ai_usage_calls WHERE day=$1::date`, day).Scan(&result.Calls, &result.Recorded, &result.Unknown, &result.Pending, &result.InputTokens, &result.OutputTokens, &result.EmbeddingTokens, &result.Overruns)
	if err != nil {
		return result, err
	}
	return result, tx.Commit(ctx)
}
