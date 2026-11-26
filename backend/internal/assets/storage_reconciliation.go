package assets

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

const (
	storageReconciliationSafetyWindow = 24 * time.Hour
	maxStorageReconciliationPage      = 500
)

type ManagedObjectReconciliationResult struct {
	NextCursor string
	Deleted    int
	Retained   int
}

type ReadyIntegrityResult struct {
	Processed   int
	NextAfterID string
}

// ReconcileManagedObjectPage deletes only old objects that remain unowned at
// both database checks. Listing is candidate discovery, never ownership proof.
func (s *Service) ReconcileManagedObjectPage(ctx context.Context, namespace storageNamespace, cursor string, limit int, now time.Time) (ManagedObjectReconciliationResult, error) {
	return s.reconcileManagedObjectPage(ctx, namespace, cursor, limit, now, nil)
}

func (s *Service) reconcileManagedObjectPage(ctx context.Context, namespace storageNamespace, cursor string, limit int, now time.Time, beforeDelete func(context.Context) error) (ManagedObjectReconciliationResult, error) {
	if namespace != stagingNamespace && namespace != permanentNamespace {
		return ManagedObjectReconciliationResult{}, errStorageInvalidKey
	}
	page, err := s.storage.listManagedObjects(ctx, namespace, cursor, limit)
	if err != nil {
		return ManagedObjectReconciliationResult{}, ErrStorageUnavailable
	}
	if now.IsZero() {
		now = s.clock()
	}
	result := ManagedObjectReconciliationResult{NextCursor: page.nextCursor}
	cutoff := now.UTC().Add(-storageReconciliationSafetyWindow)
	for _, object := range page.objects {
		if object.lastModified.After(cutoff) {
			result.Retained++
			continue
		}
		key, ok := parseManagedKey(object.key, namespace)
		if !ok {
			result.Retained++
			continue
		}
		owned, err := s.managedObjectOwned(ctx, namespace, key)
		if err != nil {
			return result, err
		}
		if owned {
			result.Retained++
			continue
		}
		owned, err = s.managedObjectOwned(ctx, namespace, key)
		if err != nil {
			return result, err
		}
		if owned {
			result.Retained++
			continue
		}
		if beforeDelete != nil {
			if err := beforeDelete(ctx); err != nil {
				return result, err
			}
		}
		if namespace == stagingNamespace {
			staging, _ := parseStagingKey(key)
			err = deleteMissingStorageObject(func() error { return s.storage.deleteStagingObject(ctx, staging) })
		} else {
			permanent, _ := parsePermanentKey(key)
			err = deleteMissingStorageObject(func() error { return s.storage.deletePermanentObject(ctx, permanent) })
		}
		if err != nil {
			return result, ErrStorageUnavailable
		}
		result.Deleted++
		if s.logger != nil {
			s.logger.InfoContext(ctx, "assets.orphan_deleted", "module", "assets", "operation", "storageReconciliation")
		}
	}
	if result.Retained > 0 && s.logger != nil {
		s.logger.WarnContext(ctx, "assets.orphan_retained", "module", "assets", "operation", "storageReconciliation", "count", result.Retained)
	}
	return result, nil
}

func (s *Service) managedObjectOwned(ctx context.Context, namespace storageNamespace, key string) (bool, error) {
	column := "staging_key"
	if namespace == permanentNamespace {
		column = "permanent_key"
	}
	var owned bool
	query := `SELECT EXISTS (SELECT 1 FROM assets WHERE ` + column + ` = $1)`
	if err := s.database.QueryRowContext(ctx, query, key).Scan(&owned); err != nil {
		return false, errors.New("could not check managed object ownership")
	}
	return owned, nil
}

// ReconcileReadyObjectIntegrity observes missing READY media without changing
// lifecycle state; only the normal Asset workflow may repair or fail an Asset.
func (s *Service) ReconcileReadyObjectIntegrity(ctx context.Context, afterID string, limit int) (ReadyIntegrityResult, error) {
	if limit < 1 {
		limit = 100
	}
	if limit > maxStorageReconciliationPage {
		limit = maxStorageReconciliationPage
	}
	query := `
		SELECT id::text, permanent_key
		FROM assets
		WHERE status = 'READY' AND ($1 = '' OR id::text > $1)
		ORDER BY id::text ASC
		LIMIT $2
	`
	rows, err := s.database.QueryContext(ctx, query, afterID, limit)
	if err != nil {
		return ReadyIntegrityResult{}, errors.New("could not list READY Assets for reconciliation")
	}
	type readyObject struct {
		id  string
		key sql.NullString
	}
	assets := make([]readyObject, 0, limit)
	for rows.Next() {
		var asset readyObject
		if err := rows.Scan(&asset.id, &asset.key); err != nil {
			rows.Close()
			return ReadyIntegrityResult{}, errors.New("could not read READY Asset reconciliation row")
		}
		assets = append(assets, asset)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return ReadyIntegrityResult{}, errors.New("could not read READY Asset reconciliation page")
	}
	if err := rows.Close(); err != nil {
		return ReadyIntegrityResult{}, errors.New("could not close READY Asset reconciliation rows")
	}
	for _, asset := range assets {
		missing := !asset.key.Valid
		if asset.key.Valid {
			key, err := parsePermanentKey(asset.key.String)
			if err != nil {
				missing = true
			} else {
				_, found, err := s.storage.getObjectMetadata(ctx, key.String())
				if err != nil {
					return ReadyIntegrityResult{}, ErrStorageUnavailable
				}
				missing = !found
			}
		}
		if missing && s.logger != nil {
			s.logger.WarnContext(ctx, "assets.ready_object_missing", "module", "assets", "operation", "readyIntegrity", "entityId", asset.id)
		}
	}
	result := ReadyIntegrityResult{Processed: len(assets)}
	if len(assets) == limit {
		result.NextAfterID = assets[len(assets)-1].id
	}
	return result, nil
}
