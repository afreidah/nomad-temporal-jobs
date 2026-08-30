// -------------------------------------------------------------------------------
// Shared S3 Store - Multipart Upload, Listing, Deletion, and Quota Eviction
//
// Project: Nomad Temporal Jobs / Author: Alex Freidah
//
// S3Store is the shared S3 client for backup-style workloads: heartbeat-wrapped
// multipart upload, object listing, deletion, and oldest-object eviction. It is
// a concrete exported type; each worker declares its own narrow interface over
// the subset it uses (accept interfaces, return structs). A worker needing more
// adds methods here and widens only its own consumer interface.
//
// The store reaches the AWS SDK through the small s3api interface, which a fake
// satisfies in tests.
// -------------------------------------------------------------------------------

package s3store

import (
	"context"
	"fmt"
	"io"
	"net/url"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/feature/s3/manager"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"

	"munchbox/temporal-workers/shared"
)

const (
	s3UploadPartSize          = 16 * 1024 * 1024 // 16 MB parts
	s3UploadConcurrency       = 4                // parallel part uploads
	s3UploadHeartbeatInterval = 30 * time.Second
)

// S3Config configures an S3Store.
type S3Config struct {
	Endpoint  string
	Bucket    string
	AccessKey string
	SecretKey string
}

// s3api is the narrow AWS SDK surface S3Store depends on for listing and
// deleting. *s3.Client satisfies it; tests pass a fake.
type s3api interface {
	ListObjectsV2(ctx context.Context, in *s3.ListObjectsV2Input, optFns ...func(*s3.Options)) (*s3.ListObjectsV2Output, error)
	DeleteObject(ctx context.Context, in *s3.DeleteObjectInput, optFns ...func(*s3.Options)) (*s3.DeleteObjectOutput, error)
}

// uploadFunc uploads one object, abstracting the multipart manager.Uploader so
// a fake can stand in for it in tests.
type uploadFunc func(ctx context.Context, in *s3.PutObjectInput) error

// S3Store owns S3 access for a worker: multipart upload, object listing,
// deletion, retention cleanup, and quota eviction. It holds the bucket so
// callers pass only keys and prefixes.
type S3Store struct {
	api    s3api
	bucket string
	upload uploadFunc
}

// NewS3Store builds an S3Store with a path-style client and a heartbeat-wrapped
// multipart uploader (a stalled upload trips the activity HeartbeatTimeout).
func NewS3Store(cfg S3Config) *S3Store {
	endpoint := cfg.Endpoint
	client := s3.New(s3.Options{
		BaseEndpoint: &endpoint,
		Region:       "us-east-1", // required by SDK but ignored by s3-orchestrator
		Credentials:  credentials.NewStaticCredentialsProvider(cfg.AccessKey, cfg.SecretKey, ""),
		UsePathStyle: true,
	})
	// s3/manager is deprecated in favor of feature/s3/transfermanager; migrating
	// is tracked separately, and the manager API still works against s3-orchestrator.
	//nolint:staticcheck // SA1019: pending transfermanager migration
	uploader := manager.NewUploader(client, func(u *manager.Uploader) {
		u.PartSize = s3UploadPartSize
		u.Concurrency = s3UploadConcurrency
	})
	return &S3Store{
		api:    client,
		bucket: cfg.Bucket,
		upload: func(ctx context.Context, in *s3.PutObjectInput) error {
			_, err := shared.WithHeartbeat(ctx, s3UploadHeartbeatInterval, func() (struct{}, error) {
				_, e := uploader.Upload(ctx, in) //nolint:staticcheck // SA1019: pending transfermanager migration
				return struct{}{}, e
			})
			return err
		},
	}
}

// Put uploads body to key in the store's bucket via multipart upload, applying
// tags to the object when any are given. Tags survive the multipart path: the
// uploader copies Tagging into CreateMultipartUploadInput and s3-orchestrator
// applies the tags when the upload completes.
func (s *S3Store) Put(ctx context.Context, key string, body io.Reader, tags map[string]string) error {
	in := &s3.PutObjectInput{Bucket: &s.bucket, Key: &key, Body: body}
	if tagging := encodeTags(tags); tagging != "" {
		in.Tagging = &tagging
	}
	return s.upload(ctx, in)
}

// encodeTags renders tags in the query-string form S3 expects for Tagging
// ("k=v&k2=v2"). url.Values sorts by key, so the same tag set always encodes
// to the same string regardless of Go's map iteration order.
func encodeTags(tags map[string]string) string {
	if len(tags) == 0 {
		return ""
	}
	vals := make(url.Values, len(tags))
	for k, v := range tags {
		vals.Set(k, v)
	}
	return vals.Encode()
}

// ListObjects returns all non-marker objects under prefix, paginated.
func (s *S3Store) ListObjects(ctx context.Context, prefix string) ([]s3types.Object, error) {
	var objs []s3types.Object
	paginator := s3.NewListObjectsV2Paginator(s.api, &s3.ListObjectsV2Input{
		Bucket: &s.bucket,
		Prefix: &prefix,
	})
	for paginator.HasMorePages() {
		page, err := paginator.NextPage(ctx)
		if err != nil {
			return nil, err
		}
		for _, obj := range page.Contents {
			if !strings.HasSuffix(aws.ToString(obj.Key), "/") {
				objs = append(objs, obj)
			}
		}
	}
	return objs, nil
}

// DeleteObject removes key from the store's bucket.
func (s *S3Store) DeleteObject(ctx context.Context, key string) error {
	_, err := s.api.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: &s.bucket, Key: &key})
	return err
}

// DeleteOldest evicts the oldest object under prefix, skipping skipKey, and
// returns the evicted key. Returns an error when there is nothing to evict.
func (s *S3Store) DeleteOldest(ctx context.Context, prefix, skipKey string) (string, error) {
	objs, err := s.ListObjects(ctx, prefix+"/")
	if err != nil {
		return "", fmt.Errorf("list objects under %s: %w", prefix, err)
	}
	oldest, ok := oldestObject(objs, skipKey)
	if !ok {
		return "", fmt.Errorf("no objects to evict under %s", prefix)
	}
	key := aws.ToString(oldest.Key)
	if err := s.DeleteObject(ctx, key); err != nil {
		return "", err
	}
	return key, nil
}

// oldestObject returns the oldest object in objs, excluding skipKey and any
// object without a LastModified. ok is false when none qualify.
func oldestObject(objs []s3types.Object, skipKey string) (s3types.Object, bool) {
	var oldest s3types.Object
	found := false
	for _, obj := range objs {
		if aws.ToString(obj.Key) == skipKey || obj.LastModified == nil {
			continue
		}
		if !found || obj.LastModified.Before(*oldest.LastModified) {
			oldest = obj
			found = true
		}
	}
	return oldest, found
}
