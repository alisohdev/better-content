package assets

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"path"
	"sort"
	"strconv"
	"strings"
	"time"
)

const maxManagedStoragePage = 500

type filesystemStorage struct {
	root               *os.Root
	baseURL            string
	secret             []byte
	clock              func() time.Time
	publicationTempDir string
}

// DevelopmentFilesystemStorage keeps the concrete adapter private while
// exposing only its HTTP capability consumer and lifecycle to API bootstrap.
type DevelopmentFilesystemStorage struct {
	storage *filesystemStorage
	handler *filesystemCapabilityHandler
}

func OpenDevelopmentFilesystemStorage(rootDirectory, baseURL string, secret []byte) (*DevelopmentFilesystemStorage, error) {
	if len(secret) < 32 {
		return nil, errors.New("development storage secret must contain at least 32 bytes")
	}
	mac := hmac.New(sha256.New, secret)
	_, _ = io.WriteString(mac, "better-content/filesystem-capability/v1")
	storage, err := newFilesystemStorage(rootDirectory, baseURL, mac.Sum(nil))
	if err != nil {
		return nil, err
	}
	handler, err := newFilesystemCapabilityHandler(storage, true)
	if err != nil {
		_ = storage.close()
		return nil, err
	}
	return &DevelopmentFilesystemStorage{storage: storage, handler: handler}, nil
}

func (s *DevelopmentFilesystemStorage) Handler() http.Handler { return s.handler }

func (s *DevelopmentFilesystemStorage) Close() error { return s.storage.close() }

func newFilesystemStorage(rootDirectory, baseURL string, secret []byte) (*filesystemStorage, error) {
	if len(secret) < 32 {
		return nil, errors.New("filesystem capability secret must be at least 32 bytes")
	}
	parsedURL, err := url.Parse(strings.TrimRight(baseURL, "/"))
	if err != nil || (parsedURL.Scheme != "http" && parsedURL.Scheme != "https") || parsedURL.Host == "" || parsedURL.User != nil || parsedURL.RawQuery != "" || parsedURL.Fragment != "" {
		return nil, errors.New("filesystem capability base URL must be an absolute HTTP URL without credentials or query")
	}
	root, err := os.OpenRoot(rootDirectory)
	if err != nil {
		return nil, fmt.Errorf("open private filesystem root: %w", err)
	}
	return &filesystemStorage{root: root, baseURL: strings.TrimRight(baseURL, "/"), secret: append([]byte(nil), secret...), clock: time.Now}, nil
}

func (s *filesystemStorage) close() error { return s.root.Close() }

func (s *filesystemStorage) listManagedObjects(ctx context.Context, namespace storageNamespace, cursor string, limit int) (managedStorageObjectPage, error) {
	if err := ctx.Err(); err != nil {
		return managedStorageObjectPage{}, err
	}
	prefix, err := namespacePrefix(namespace)
	if err != nil {
		return managedStorageObjectPage{}, errStorageInvalidKey
	}
	if limit < 1 {
		limit = 100
	}
	if limit > maxManagedStoragePage {
		limit = maxManagedStoragePage
	}
	keys := make([]string, 0)
	err = fs.WalkDir(s.root.FS(), prefix, func(name string, entry fs.DirEntry, walkErr error) error {
		if errors.Is(walkErr, fs.ErrNotExist) {
			return nil
		}
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		if !entry.Type().IsRegular() {
			return nil
		}
		key := filepathSlash(name)
		if key <= cursor {
			return nil
		}
		if _, ok := parseManagedKey(key, namespace); !ok {
			return nil
		}
		keys = append(keys, key)
		return nil
	})
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return managedStorageObjectPage{}, storageUnavailable(err)
	}
	sort.Strings(keys)
	page := managedStorageObjectPage{}
	more := len(keys) > limit
	if more {
		keys = keys[:limit]
	}
	for _, key := range keys {
		info, statErr := s.root.Stat(key)
		if errors.Is(statErr, fs.ErrNotExist) {
			continue
		}
		if statErr != nil {
			return managedStorageObjectPage{}, storageUnavailable(statErr)
		}
		page.objects = append(page.objects, managedStorageObject{key: key, sizeBytes: info.Size(), lastModified: info.ModTime()})
	}
	if more && len(keys) > 0 {
		page.nextCursor = keys[len(keys)-1]
	}
	return page, nil
}

func (s *filesystemStorage) getObjectMetadata(ctx context.Context, key string) (storedObjectMetadata, bool, error) {
	if err := ctx.Err(); err != nil {
		return storedObjectMetadata{}, false, err
	}
	if _, ok := parseManagedKey(key, 0); !ok {
		return storedObjectMetadata{}, false, errStorageInvalidKey
	}
	info, err := s.root.Stat(key)
	if errors.Is(err, fs.ErrNotExist) {
		return storedObjectMetadata{}, false, nil
	}
	if err != nil {
		return storedObjectMetadata{}, false, storageUnavailable(err)
	}
	if !info.Mode().IsRegular() {
		return storedObjectMetadata{}, false, nil
	}
	return storedObjectMetadata{sizeBytes: info.Size()}, true, nil
}

func (s *filesystemStorage) openStagingRead(ctx context.Context, key stagingKey) (io.ReadCloser, error) {
	return s.openRead(ctx, key.String())
}

func (s *filesystemStorage) openPermanentRead(ctx context.Context, key permanentKey) (io.ReadCloser, error) {
	return s.openRead(ctx, key.String())
}

func (s *filesystemStorage) openRead(ctx context.Context, key string) (io.ReadCloser, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if _, ok := parseManagedKey(key, 0); !ok {
		return nil, errStorageInvalidKey
	}
	file, err := s.root.Open(key)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, errStorageNotFound
	}
	if err != nil {
		return nil, storageUnavailable(err)
	}
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		_ = file.Close()
		return nil, errStorageNotFound
	}
	return file, nil
}

func (s *filesystemStorage) putStagingFromStream(ctx context.Context, key stagingKey, source io.Reader) error {
	return s.putImmutable(ctx, key.String(), source, false)
}

func (s *filesystemStorage) putPermanentFromStream(ctx context.Context, key permanentKey, source io.Reader) error {
	return s.putImmutable(ctx, key.String(), source, true)
}

func (s *filesystemStorage) putImmutable(ctx context.Context, key string, source io.Reader, permanent bool) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if _, ok := parseManagedKey(key, 0); !ok {
		return errStorageInvalidKey
	}
	if err := s.root.MkdirAll(path.Dir(key), 0o700); err != nil {
		return storageUnavailable(err)
	}
	temporary := key + ".tmp-" + randomUUIDString()
	if s.publicationTempDir != "" {
		temporary = path.Join(s.publicationTempDir, path.Base(temporary))
	}
	file, err := s.root.OpenFile(temporary, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return storageUnavailable(err)
	}
	_, copyErr := copyWithContext(ctx, file, source)
	closeErr := file.Close()
	if copyErr != nil || closeErr != nil {
		_ = s.root.Remove(temporary)
		if copyErr != nil {
			return storageUnavailable(copyErr)
		}
		return storageUnavailable(closeErr)
	}
	defer s.root.Remove(temporary)
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := s.root.Link(temporary, key); err != nil {
		if errors.Is(err, fs.ErrExist) && permanent {
			return errStorageObjectExists
		}
		return storageUnavailable(err)
	}
	return nil
}

func (s *filesystemStorage) copyStagingToPermanent(ctx context.Context, source stagingKey, destination permanentKey) error {
	reader, err := s.openStagingRead(ctx, source)
	if err != nil {
		return err
	}
	defer reader.Close()
	return s.putPermanentFromStream(ctx, destination, reader)
}

func (s *filesystemStorage) deleteStagingObject(ctx context.Context, key stagingKey) error {
	return s.deleteObject(ctx, key.String())
}

func (s *filesystemStorage) deletePermanentObject(ctx context.Context, key permanentKey) error {
	return s.deleteObject(ctx, key.String())
}

func (s *filesystemStorage) deleteObject(ctx context.Context, key string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if _, ok := parseManagedKey(key, 0); !ok {
		return errStorageInvalidKey
	}
	err := s.root.Remove(key)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return storageUnavailable(err)
	}
	return nil
}

func (s *filesystemStorage) createStagingPutCapability(key stagingKey, expiresAt time.Time, contentLength int64) (string, error) {
	now := s.clock()
	if key.String() == "" || contentLength < 1 || !expiresAt.After(now) || expiresAt.After(now.Add(maxFilesystemCapabilityLifetime)) {
		return "", errStorageInvalidKey
	}
	fields := []string{"PUT", key.String(), strconv.FormatInt(expiresAt.Unix(), 10), strconv.FormatInt(contentLength, 10)}
	signature := s.sign(fields...)
	return s.capabilityURL("/_dev/assets/staging/"+url.PathEscape(strings.TrimPrefix(key.String(), "staging/")), url.Values{"expires": {fields[2]}, "size": {fields[3]}, "signature": {signature}}), nil
}

func (s *filesystemStorage) createPrivateReadCapability(ctx context.Context, key permanentKey, expiresAt time.Time, options privateReadOptions) (string, error) {
	now := s.clock()
	if !expiresAt.After(now) || expiresAt.After(now.Add(maxFilesystemCapabilityLifetime)) {
		return "", errStorageInvalidKey
	}
	if _, found, err := s.getObjectMetadata(ctx, key.String()); err != nil {
		return "", err
	} else if !found {
		return "", errStorageNotFound
	}
	expiry := strconv.FormatInt(expiresAt.Unix(), 10)
	signature := s.sign("GET", key.String(), expiry, options.contentType, options.contentDisposition, strconv.FormatBool(options.rangeAllowed))
	return s.capabilityURL("/_dev/assets/private/"+url.PathEscape(strings.TrimPrefix(key.String(), "permanent/")), url.Values{"expires": {expiry}, "type": {options.contentType}, "disposition": {options.contentDisposition}, "range": {strconv.FormatBool(options.rangeAllowed)}, "signature": {signature}}), nil
}

func (s *filesystemStorage) verifyCapability(signature string, fields ...string) bool {
	want, err := hex.DecodeString(s.sign(fields...))
	got, decodeErr := hex.DecodeString(signature)
	return err == nil && decodeErr == nil && hmac.Equal(got, want)
}

func (s *filesystemStorage) sign(fields ...string) string {
	mac := hmac.New(sha256.New, s.secret)
	for _, field := range fields {
		_, _ = io.WriteString(mac, strconv.Itoa(len(field))+":"+field+"\n")
	}
	return hex.EncodeToString(mac.Sum(nil))
}

func (s *filesystemStorage) capabilityURL(route string, query url.Values) string {
	return s.baseURL + route + "?" + query.Encode()
}

func namespacePrefix(namespace storageNamespace) (string, error) {
	switch namespace {
	case stagingNamespace:
		return "staging", nil
	case permanentNamespace:
		return "permanent", nil
	default:
		return "", errStorageInvalidKey
	}
}

func parseManagedKey(value string, namespace storageNamespace) (string, bool) {
	if namespace == stagingNamespace {
		_, err := parseStagingKey(value)
		return value, err == nil
	}
	if namespace == permanentNamespace {
		_, err := parsePermanentKey(value)
		return value, err == nil
	}
	if _, err := parseStagingKey(value); err == nil {
		return value, true
	}
	_, err := parsePermanentKey(value)
	return value, err == nil
}

func filepathSlash(value string) string { return strings.ReplaceAll(value, "\\", "/") }

func copyWithContext(ctx context.Context, destination io.Writer, source io.Reader) (int64, error) {
	buffer := make([]byte, 64*1024)
	var total int64
	for {
		if err := ctx.Err(); err != nil {
			return total, err
		}
		count, readErr := source.Read(buffer)
		if count > 0 {
			written, writeErr := destination.Write(buffer[:count])
			total += int64(written)
			if writeErr != nil {
				return total, writeErr
			}
			if written != count {
				return total, io.ErrShortWrite
			}
		}
		if readErr == io.EOF {
			return total, nil
		}
		if readErr != nil {
			return total, readErr
		}
	}
}

func randomUUIDString() string {
	key, err := newStagingKey()
	if err != nil {
		panic("generate private storage temporary name: " + err.Error())
	}
	return strings.TrimPrefix(key.String(), "staging/")
}

func storageUnavailable(err error) error { return fmt.Errorf("%w: %v", errStorageUnavailable, err) }
