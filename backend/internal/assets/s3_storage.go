package assets

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"log/slog"
	"net/url"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/smithy-go"
)

type S3StorageConfig struct {
	Endpoint        string
	Region          string
	Bucket          string
	AccessKeyID     string
	SecretAccessKey string
	ForcePathStyle  bool
}

// S3AssetStorage owns the production S3-compatible adapter while keeping its
// client and bucket details outside the application service.
type S3AssetStorage struct {
	storage *s3Storage
}

type s3Storage struct {
	client  *s3.Client
	presign *s3.PresignClient
	bucket  string
}

func OpenS3AssetStorage(configuration S3StorageConfig) (*S3AssetStorage, error) {
	endpoint, err := url.Parse(configuration.Endpoint)
	if err != nil || endpoint.Scheme != "https" || endpoint.Host == "" || endpoint.User != nil || endpoint.RawQuery != "" || endpoint.Fragment != "" || (endpoint.Path != "" && endpoint.Path != "/") {
		return nil, errors.New("ASSET_S3_ENDPOINT must be an HTTPS origin")
	}
	if strings.TrimSpace(configuration.Region) == "" || strings.TrimSpace(configuration.Bucket) == "" || configuration.AccessKeyID == "" || configuration.SecretAccessKey == "" {
		return nil, errors.New("S3 asset storage configuration is incomplete")
	}
	awsConfig := aws.Config{
		Region:      configuration.Region,
		Credentials: credentials.NewStaticCredentialsProvider(configuration.AccessKeyID, configuration.SecretAccessKey, ""),
	}
	client := s3.NewFromConfig(awsConfig, func(options *s3.Options) {
		options.BaseEndpoint = aws.String(strings.TrimRight(configuration.Endpoint, "/"))
		options.UsePathStyle = configuration.ForcePathStyle
	})
	storage := &s3Storage{client: client, presign: s3.NewPresignClient(client), bucket: configuration.Bucket}
	return &S3AssetStorage{storage: storage}, nil
}

func (s *S3AssetStorage) NewService(database *sql.DB, clock func() time.Time, logger *slog.Logger) *Service {
	return newService(database, s.storage, clock, logger)
}

func (s *s3Storage) listManagedObjects(ctx context.Context, namespace storageNamespace, cursor string, limit int) (managedStorageObjectPage, error) {
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
	input := &s3.ListObjectsV2Input{Bucket: aws.String(s.bucket), Prefix: aws.String(prefix + "/"), MaxKeys: aws.Int32(int32(limit))}
	if cursor != "" {
		if len(cursor) > 4096 {
			return managedStorageObjectPage{}, errStorageInvalidKey
		}
		input.ContinuationToken = aws.String(cursor)
	}
	output, err := s.client.ListObjectsV2(ctx, input)
	if err != nil {
		return managedStorageObjectPage{}, storageUnavailable(err)
	}
	page := managedStorageObjectPage{}
	for _, object := range output.Contents {
		if object.Key == nil {
			continue
		}
		key := *object.Key
		if _, ok := parseManagedKey(key, namespace); !ok {
			continue
		}
		item := managedStorageObject{key: key}
		if object.Size != nil {
			item.sizeBytes = *object.Size
		}
		if object.LastModified != nil {
			item.lastModified = *object.LastModified
		}
		page.objects = append(page.objects, item)
	}
	if aws.ToBool(output.IsTruncated) {
		if output.NextContinuationToken == nil || *output.NextContinuationToken == "" {
			return managedStorageObjectPage{}, storageUnavailable(errors.New("S3 returned a truncated page without a continuation token"))
		}
		page.nextCursor = *output.NextContinuationToken
	}
	return page, nil
}

func (s *s3Storage) getObjectMetadata(ctx context.Context, key string) (storedObjectMetadata, bool, error) {
	if _, ok := parseManagedKey(key, 0); !ok {
		return storedObjectMetadata{}, false, errStorageInvalidKey
	}
	output, err := s.client.HeadObject(ctx, &s3.HeadObjectInput{Bucket: aws.String(s.bucket), Key: aws.String(key)})
	if isS3Missing(err) {
		return storedObjectMetadata{}, false, nil
	}
	if err != nil {
		return storedObjectMetadata{}, false, storageUnavailable(err)
	}
	return storedObjectMetadata{sizeBytes: aws.ToInt64(output.ContentLength)}, true, nil
}

func (s *s3Storage) openStagingRead(ctx context.Context, key stagingKey) (io.ReadCloser, error) {
	return s.openRead(ctx, key.String())
}

func (s *s3Storage) openPermanentRead(ctx context.Context, key permanentKey) (io.ReadCloser, error) {
	return s.openRead(ctx, key.String())
}

func (s *s3Storage) openRead(ctx context.Context, key string) (io.ReadCloser, error) {
	if _, ok := parseManagedKey(key, 0); !ok {
		return nil, errStorageInvalidKey
	}
	output, err := s.client.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(s.bucket), Key: aws.String(key)})
	if isS3Missing(err) {
		return nil, errStorageNotFound
	}
	if err != nil {
		return nil, storageUnavailable(err)
	}
	return output.Body, nil
}

func (s *s3Storage) putStagingFromStream(ctx context.Context, key stagingKey, source io.Reader) error {
	if !key.valid() {
		return errStorageInvalidKey
	}
	_, err := s.client.PutObject(ctx, &s3.PutObjectInput{Bucket: aws.String(s.bucket), Key: aws.String(key.String()), Body: source, IfNoneMatch: aws.String("*")})
	if isS3Precondition(err) {
		return errStorageObjectExists
	}
	if err != nil {
		return storageUnavailable(err)
	}
	return nil
}

func (s *s3Storage) putPermanentFromStream(ctx context.Context, key permanentKey, source io.Reader) error {
	if !key.valid() {
		return errStorageInvalidKey
	}
	_, err := s.client.PutObject(ctx, &s3.PutObjectInput{Bucket: aws.String(s.bucket), Key: aws.String(key.String()), Body: source, IfNoneMatch: aws.String("*")})
	if isS3Precondition(err) {
		return errStorageObjectExists
	}
	if err != nil {
		return storageUnavailable(err)
	}
	return nil
}

func (s *s3Storage) copyStagingToPermanent(ctx context.Context, source stagingKey, destination permanentKey) error {
	if !source.valid() || !destination.valid() {
		return errStorageInvalidKey
	}
	copySource := s.bucket + "/" + strings.ReplaceAll(url.PathEscape(source.String()), "+", "%20")
	_, err := s.client.CopyObject(ctx, &s3.CopyObjectInput{Bucket: aws.String(s.bucket), Key: aws.String(destination.String()), CopySource: aws.String(copySource), IfNoneMatch: aws.String("*")})
	if isS3Precondition(err) {
		return errStorageObjectExists
	}
	if isS3Missing(err) {
		return errStorageNotFound
	}
	if err != nil {
		return storageUnavailable(err)
	}
	return nil
}

func (s *s3Storage) deleteStagingObject(ctx context.Context, key stagingKey) error {
	return s.delete(ctx, key.String())
}

func (s *s3Storage) deletePermanentObject(ctx context.Context, key permanentKey) error {
	return s.delete(ctx, key.String())
}

func (s *s3Storage) delete(ctx context.Context, key string) error {
	if _, ok := parseManagedKey(key, 0); !ok {
		return errStorageInvalidKey
	}
	_, err := s.client.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: aws.String(s.bucket), Key: aws.String(key)})
	if err != nil {
		return storageUnavailable(err)
	}
	return nil
}

func (s *s3Storage) createStagingPutCapability(key stagingKey, expiresAt time.Time, contentLength int64) (string, error) {
	if !key.valid() || contentLength < 1 || !expiresAt.After(time.Now()) || expiresAt.After(time.Now().Add(maxFilesystemCapabilityLifetime)) {
		return "", errStorageInvalidKey
	}
	request, err := s.presign.PresignPutObject(context.Background(), &s3.PutObjectInput{Bucket: aws.String(s.bucket), Key: aws.String(key.String()), ContentLength: aws.Int64(contentLength)}, s3.WithPresignExpires(time.Until(expiresAt)))
	if err != nil {
		return "", storageUnavailable(err)
	}
	return request.URL, nil
}

func (s *s3Storage) createPrivateReadCapability(ctx context.Context, key permanentKey, expiresAt time.Time, options privateReadOptions) (string, error) {
	if !key.valid() || !expiresAt.After(time.Now()) || expiresAt.After(time.Now().Add(maxFilesystemCapabilityLifetime)) {
		return "", errStorageInvalidKey
	}
	if _, found, err := s.getObjectMetadata(ctx, key.String()); err != nil {
		return "", err
	} else if !found {
		return "", errStorageNotFound
	}
	input := &s3.GetObjectInput{Bucket: aws.String(s.bucket), Key: aws.String(key.String()), ResponseCacheControl: aws.String("private, no-store"), ResponseContentType: aws.String(options.contentType)}
	if options.contentDisposition != "" {
		input.ResponseContentDisposition = aws.String(options.contentDisposition)
	}
	if options.rangeAllowed {
		input.ResponseContentType = aws.String(options.contentType)
	}
	request, err := s.presign.PresignGetObject(ctx, input, s3.WithPresignExpires(time.Until(expiresAt)))
	if err != nil {
		return "", storageUnavailable(err)
	}
	return request.URL, nil
}

func isS3Missing(err error) bool {
	var apiError smithy.APIError
	if !errors.As(err, &apiError) {
		return false
	}
	return apiError.ErrorCode() == "NotFound" || apiError.ErrorCode() == "NoSuchKey" || apiError.ErrorCode() == "404"
}

func isS3Precondition(err error) bool {
	var apiError smithy.APIError
	return errors.As(err, &apiError) && (apiError.ErrorCode() == "PreconditionFailed" || apiError.ErrorCode() == "412")
}

var _ assetStorage = (*s3Storage)(nil)
