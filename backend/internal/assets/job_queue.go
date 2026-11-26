package assets

import (
	"context"
	"database/sql"
	"errors"
	"math/rand/v2"
	"time"

	"github.com/google/uuid"
)

const (
	assetJobLease         = time.Minute
	processingUnavailable = "PROCESSING_UNAVAILABLE"
)

var errAssetJobLeaseLost = errors.New("asset job lease lost")

type assetJob struct {
	id, kind, assetID, owner string
	attempts, maxAttempts    int
	exhausted                bool
	cleanupStaging           sql.NullString
}

type assetJobFailure struct {
	code      string
	retryable bool
}

func (failure *assetJobFailure) Error() string { return failure.code }

func (job assetJob) processesMedia() bool {
	return job.kind == "PROCESS_UPLOAD" || job.kind == "INGEST_EXTERNAL_URL"
}

type assetJobMutation func(context.Context, *sql.Tx) error

func claimAssetJob(ctx context.Context, db *sql.DB) (*assetJob, error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	owner, err := uuid.NewRandom()
	if err != nil {
		return nil, err
	}
	var job assetJob
	err = tx.QueryRowContext(ctx, `
		WITH candidate AS (
			SELECT id, attempts >= max_attempts AS exhausted
			FROM asset_jobs
			WHERE (status='PENDING' AND scheduled_at <= statement_timestamp())
			   OR (status='RUNNING' AND lease_expires_at <= statement_timestamp())
			ORDER BY scheduled_at, created_at, id
			LIMIT 1 FOR UPDATE SKIP LOCKED
		), claim_time AS (SELECT clock_timestamp() AS at)
		UPDATE asset_jobs AS job SET
			status=CASE WHEN candidate.exhausted THEN 'FAILED' ELSE 'RUNNING' END,
			attempts=job.attempts+CASE WHEN candidate.exhausted THEN 0 ELSE 1 END,
			lease_owner=CASE WHEN NOT candidate.exhausted THEN $1::text END,
			lease_expires_at=CASE WHEN NOT candidate.exhausted THEN claim_time.at+$2*interval '1 millisecond' END,
			failure_code=CASE WHEN candidate.exhausted THEN $3::text END,
			failed_at=CASE WHEN candidate.exhausted THEN claim_time.at END,
			updated_at=claim_time.at
		FROM candidate CROSS JOIN claim_time
		WHERE job.id=candidate.id
		RETURNING job.id, job.type, job.payload->>'assetId', job.attempts, job.max_attempts,
			COALESCE(job.lease_owner,''), candidate.exhausted
	`, owner.String(), assetJobLease.Milliseconds(), processingUnavailable).
		Scan(&job.id, &job.kind, &job.assetID, &job.attempts, &job.maxAttempts, &job.owner, &job.exhausted)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if job.exhausted {
		if err := failMediaAsset(ctx, tx, &job, processingUnavailable); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return &job, nil
}

// Lock the job before any Asset or Workspace locks. Checking the database
// clock after acquiring the row lock prevents a blocked stale writer reviving.
func lockAssetJobLease(ctx context.Context, tx *sql.Tx, job assetJob) error {
	var id string
	err := tx.QueryRowContext(ctx, `
 SELECT id FROM asset_jobs WHERE id=$1 AND status='RUNNING' AND lease_owner=$2
 AND attempts=$3 FOR UPDATE
 `, job.id, job.owner, job.attempts).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return errAssetJobLeaseLost
	}
	if err != nil {
		return err
	}
	return checkAssetJobLease(ctx, tx, job)
}

func checkAssetJobLease(ctx context.Context, tx *sql.Tx, job assetJob) error {
	var valid bool
	if err := tx.QueryRowContext(ctx, "SELECT lease_expires_at > clock_timestamp() FROM asset_jobs WHERE id=$1", job.id).Scan(&valid); err != nil {
		return err
	}
	if !valid {
		return errAssetJobLeaseLost
	}
	return nil
}

// A nil mutation renews the lease. Other mutations are committed only while
// the same attempt owns the job, even if they waited for an Asset lock.
func withAssetJobLease(ctx context.Context, db *sql.DB, job assetJob, mutate assetJobMutation) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := lockAssetJobLease(ctx, tx, job); err != nil {
		return err
	}
	if mutate == nil {
		result, err := tx.ExecContext(ctx, `
  WITH now AS (SELECT clock_timestamp() AS at)
  UPDATE asset_jobs SET lease_expires_at=now.at+$2*interval '1 millisecond',updated_at=now.at
  FROM now WHERE id=$1 AND lease_expires_at > now.at
  `, job.id, assetJobLease.Milliseconds())
		if err != nil {
			return err
		}
		if count, err := result.RowsAffected(); err != nil {
			return err
		} else if count != 1 {
			return errAssetJobLeaseLost
		}
	} else {
		if err := mutate(ctx, tx); err != nil {
			return err
		}
		if err := checkAssetJobLease(ctx, tx, job); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func finalizeAssetJob(ctx context.Context, db *sql.DB, job *assetJob, failure *assetJobFailure, mutate assetJobMutation) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := lockAssetJobLease(ctx, tx, *job); err != nil {
		return err
	}
	if mutate != nil {
		if err := mutate(ctx, tx); err != nil {
			return err
		}
	}
	if failure != nil && (!failure.retryable || job.attempts >= job.maxAttempts) {
		if err := failMediaAsset(ctx, tx, job, failure.code); err != nil {
			return err
		}
	}
	if err := checkAssetJobLease(ctx, tx, *job); err != nil {
		return err
	}
	if err := recordAssetJobResult(ctx, tx, *job, failure); err != nil {
		return err
	}
	return tx.Commit()
}

func failMediaAsset(ctx context.Context, tx *sql.Tx, job *assetJob, code string) error {
	assetID, err := uuid.Parse(job.assetID)
	if !job.processesMedia() || err != nil || !validUUID(assetID) {
		return nil
	}
	// Preserve the permanent reservation for explicit deletion. Detach staging
	// so a failed best-effort delete can be recovered by orphan reconciliation.
	err = tx.QueryRowContext(ctx, `
 WITH current AS (
 SELECT staging_key FROM assets WHERE id=$1 AND status IN ('PENDING','PROCESSING') FOR UPDATE
 )
 UPDATE assets SET status='FAILED',failure_code=$2,staging_key=NULL,updated_at=clock_timestamp()
 FROM current WHERE id=$1 RETURNING current.staging_key
 `, assetID, code).Scan(&job.cleanupStaging)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	return err
}

func recordAssetJobResult(ctx context.Context, tx *sql.Tx, job assetJob, failure *assetJobFailure) error {
	status := "COMPLETED"
	var code any
	var backoff time.Duration
	if failure != nil {
		code = failure.code
		status = "FAILED"
		if failure.retryable && job.attempts < job.maxAttempts {
			status = "PENDING"
			base := min(time.Second<<max(0, job.attempts-1), time.Hour)
			backoff = min(base+time.Duration(rand.Int64N(base.Milliseconds()/5))*time.Millisecond, time.Hour)
		}
	}
	result, err := tx.ExecContext(ctx, `
 WITH now AS (SELECT clock_timestamp() AS at)
 UPDATE asset_jobs SET status=$2, failure_code=$3, lease_owner=NULL, lease_expires_at=NULL,
    scheduled_at=CASE WHEN $2='PENDING' THEN now.at+$4*interval '1 millisecond' ELSE scheduled_at END,
    completed_at=CASE WHEN $2='COMPLETED' THEN now.at END,
    failed_at=CASE WHEN $2='FAILED' THEN now.at END, updated_at=now.at
 FROM now WHERE id=$1 AND lease_expires_at > now.at
 `, job.id, status, code, backoff.Milliseconds())

	if err != nil {
		return err
	}
	if count, err := result.RowsAffected(); err != nil {
		return err
	} else if count != 1 {
		return errAssetJobLeaseLost
	}
	return nil
}
