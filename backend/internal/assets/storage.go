package assets

import (
	"context"
	"errors"
	"io"
	"time"
)

var (
	errStorageInvalidKey   = errors.New("invalid private storage key")
	errStorageObjectExists = errors.New("private object already exists")
	errStorageNotFound     = errors.New("private object not found")
	errStorageUnavailable  = errors.New("private storage unavailable")
)

type storedObjectMetadata struct {
	sizeBytes int64
}

type privateReadOptions struct {
	contentType        string
	contentDisposition string
	rangeAllowed       bool
}

type storageNamespace uint8

const (
	stagingNamespace storageNamespace = iota + 1
	permanentNamespace
)

type managedStorageObject struct {
	key          string
	sizeBytes    int64
	lastModified time.Time
}

type managedStorageObjectPage struct {
	objects    []managedStorageObject
	nextCursor string
}

// assetStorage is the private boundary for quarantined and published objects.
// Implementations must never expose keys as public authorization evidence.
type assetStorage interface {
	listManagedObjects(context.Context, storageNamespace, string, int) (managedStorageObjectPage, error)
	getObjectMetadata(context.Context, string) (storedObjectMetadata, bool, error)
	openStagingRead(context.Context, stagingKey) (io.ReadCloser, error)
	putStagingFromStream(context.Context, stagingKey, io.Reader) error
	// Conditional publication returns errStorageObjectExists without changing existing bytes.
	putPermanentFromStream(context.Context, permanentKey, io.Reader) error
	openPermanentRead(context.Context, permanentKey) (io.ReadCloser, error)
	copyStagingToPermanent(context.Context, stagingKey, permanentKey) error
	deleteStagingObject(context.Context, stagingKey) error
	deletePermanentObject(context.Context, permanentKey) error
	createStagingPutCapability(stagingKey, time.Time, int64) (string, error)
	createPrivateReadCapability(context.Context, permanentKey, time.Time, privateReadOptions) (string, error)
}
