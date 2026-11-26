package assets

import (
	"context"
	"database/sql"
	"errors"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"bettercontent/backend/internal/platform/postgres"

	"github.com/google/uuid"
)

func openWorkerTestDatabase(t *testing.T) *sql.DB {
	t.Helper()
	admin := openAssetTestDatabase(t)
	// Other integration packages exercise the real API and can enqueue jobs
	// concurrently. Clone the reviewed queue table into an isolated schema so
	// these worker tests claim only their fixtures.
	schema := "worker_test_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if _, err := admin.Exec("CREATE SCHEMA " + schema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := admin.Exec("DROP SCHEMA " + schema + " CASCADE"); err != nil {
			t.Error(err)
		}
	})
	if _, err := admin.Exec("CREATE TABLE " + schema + ".asset_jobs (LIKE public.asset_jobs INCLUDING ALL)"); err != nil {
		t.Fatal(err)
	}
	dsn, err := url.Parse(os.Getenv("TEST_DATABASE_URL"))
	if err != nil {
		t.Fatal("could not configure isolated worker queue")
	}
	query := dsn.Query()
	query.Set("search_path", schema+",public")
	dsn.RawQuery = query.Encode()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	db, err := postgres.Open(ctx, dsn.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func addWorkerJob(t *testing.T, db *sql.DB, kind, assetID string, maxAttempts int) string {
	t.Helper()
	id := uuid.NewString()
	_, err := db.Exec(`
 INSERT INTO asset_jobs (id,type,dedupe_key,payload,max_attempts,scheduled_at)
 VALUES ($1,$2,$3,jsonb_build_object('assetId',$4::text),$5,clock_timestamp())
 `, id, kind, kind+":"+assetID, assetID, maxAttempts)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func TestAssetJobLeaseLifecycle(t *testing.T) {
	db := openWorkerTestDatabase(t)
	ctx := context.Background()
	id := addWorkerJob(t, db, "RECONCILE_STORAGE", uuid.NewString(), 3)
	results := make(chan *assetJob, 2)
	var workers sync.WaitGroup
	for range 2 {
		workers.Go(func() {
			job, err := claimAssetJob(ctx, db)
			if err != nil {
				t.Error(err)
			}
			results <- job
		})
	}
	workers.Wait()
	close(results)
	var first *assetJob
	for job := range results {
		if job != nil {
			if first != nil || job.id != id || job.attempts != 1 {
				t.Fatalf("unexpected concurrent claim: %+v", job)
			}
			first = job
		}
	}
	if first == nil {
		t.Fatal("job was not claimed")
	}
	var previous, renewed time.Time
	if err := db.QueryRow("SELECT lease_expires_at FROM asset_jobs WHERE id=$1", id).Scan(&previous); err != nil {
		t.Fatal(err)
	}
	if err := withAssetJobLease(ctx, db, *first, nil); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow("SELECT lease_expires_at FROM asset_jobs WHERE id=$1", id).Scan(&renewed); err != nil || !renewed.After(previous) {
		t.Fatalf("lease was not renewed: %v, %v", renewed, err)
	}
	expireWorkerJob(t, db, id)
	if err := withAssetJobLease(ctx, db, *first, nil); !errors.Is(err, errAssetJobLeaseLost) {
		t.Fatalf("expired lease renewed: %v", err)
	}

	expiredMutation := func(context.Context, *sql.Tx) error {
		t.Error("expired lease reached the Asset mutation")
		return nil
	}
	if err := finalizeAssetJob(ctx, db, first, nil, expiredMutation); !errors.Is(err, errAssetJobLeaseLost) {
		t.Fatalf("expired completion: %v", err)
	}
	if err := finalizeAssetJob(ctx, db, first, &assetJobFailure{code: processingUnavailable}, nil); !errors.Is(err, errAssetJobLeaseLost) {
		t.Fatalf("expired failure: %v", err)
	}

	second, err := claimAssetJob(ctx, db)
	if err != nil || second == nil || second.id != id || second.attempts != 2 || second.owner == first.owner {
		t.Fatalf("reclaim: %+v %v", second, err)
	}
	if err := finalizeAssetJob(ctx, db, first, nil, nil); !errors.Is(err, errAssetJobLeaseLost) {
		t.Fatalf("stale completion: %v", err)
	}
	if err := finalizeAssetJob(ctx, db, second, &assetJobFailure{code: "PROCESSING_UNAVAILABLE", retryable: true}, nil); err != nil {
		t.Fatal(err)
	}
	var scheduled float64
	var status string
	if err := db.QueryRow("SELECT status, extract(epoch FROM scheduled_at-updated_at) FROM asset_jobs WHERE id=$1", id).Scan(&status, &scheduled); err != nil || status != "PENDING" || scheduled < 2 || scheduled >= 2.4 {
		t.Fatalf("retry = %s, %f, %v", status, scheduled, err)
	}
	if job, err := claimAssetJob(ctx, db); err != nil || job != nil {
		t.Fatalf("early retry claimed: %+v %v", job, err)
	}
	if _, err := db.Exec("UPDATE asset_jobs SET scheduled_at=clock_timestamp() WHERE id=$1", id); err != nil {
		t.Fatal(err)
	}
	third, err := claimAssetJob(ctx, db)
	if err != nil || third == nil || third.attempts != 3 {
		t.Fatalf("last claim: %+v %v", third, err)
	}
	expireWorkerJob(t, db, id)
	recovered, err := claimAssetJob(ctx, db)
	if err != nil || recovered == nil || !recovered.exhausted {
		t.Fatalf("exhaustion recovery: %+v %v", recovered, err)
	}
	if err := db.QueryRow("SELECT status FROM asset_jobs WHERE id=$1 AND lease_owner IS NULL AND failed_at IS NOT NULL AND failure_code='PROCESSING_UNAVAILABLE'", id).Scan(&status); err != nil || status != "FAILED" {
		t.Fatalf("exhausted status = %s, %v", status, err)
	}
}

func expireWorkerJob(t *testing.T, db *sql.DB, id string) {
	t.Helper()
	if _, err := db.Exec("UPDATE asset_jobs SET lease_expires_at=clock_timestamp()-interval '1 second' WHERE id=$1", id); err != nil {
		t.Fatal(err)
	}
}
