// -------------------------------------------------------------------------------
// Shared S3 Store - Unit Tests
//
// Project: Nomad Temporal Jobs / Author: Alex Freidah
//
// Exercises the destructive eviction/listing logic against a fake s3api (no
// real S3), plus the pure oldestObject selector.
// -------------------------------------------------------------------------------

package shared

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
)

// fakeS3 satisfies s3api: it returns canned list pages in order and records
// deletions.
type fakeS3 struct {
	pages     []*s3.ListObjectsV2Output
	idx       int
	listErr   error
	deleteErr error
	deleted   []string
}

func (f *fakeS3) ListObjectsV2(_ context.Context, _ *s3.ListObjectsV2Input, _ ...func(*s3.Options)) (*s3.ListObjectsV2Output, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	if f.idx >= len(f.pages) {
		return &s3.ListObjectsV2Output{}, nil
	}
	p := f.pages[f.idx]
	f.idx++
	return p, nil
}

func (f *fakeS3) DeleteObject(_ context.Context, in *s3.DeleteObjectInput, _ ...func(*s3.Options)) (*s3.DeleteObjectOutput, error) {
	if f.deleteErr != nil {
		return nil, f.deleteErr
	}
	f.deleted = append(f.deleted, aws.ToString(in.Key))
	return &s3.DeleteObjectOutput{}, nil
}

func obj(key string, mod time.Time) s3types.Object {
	return s3types.Object{Key: aws.String(key), LastModified: aws.Time(mod)}
}

func TestOldestObject(t *testing.T) {
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	objs := []s3types.Object{
		obj("a", base.Add(2*time.Hour)),
		obj("b", base), // oldest
		obj("c", base.Add(time.Hour)),
	}

	t.Run("returns oldest", func(t *testing.T) {
		got, ok := oldestObject(objs, "")
		if !ok || aws.ToString(got.Key) != "b" {
			t.Fatalf("got %q ok=%v, want b", aws.ToString(got.Key), ok)
		}
	})
	t.Run("skips skipKey", func(t *testing.T) {
		got, ok := oldestObject(objs, "b")
		if !ok || aws.ToString(got.Key) != "c" {
			t.Fatalf("got %q ok=%v, want c", aws.ToString(got.Key), ok)
		}
	})
	t.Run("empty is not found", func(t *testing.T) {
		if _, ok := oldestObject(nil, ""); ok {
			t.Fatal("expected ok=false for empty input")
		}
	})
	t.Run("ignores nil LastModified", func(t *testing.T) {
		mixed := []s3types.Object{{Key: aws.String("x")}, obj("y", base)}
		got, ok := oldestObject(mixed, "")
		if !ok || aws.ToString(got.Key) != "y" {
			t.Fatalf("got %q ok=%v, want y", aws.ToString(got.Key), ok)
		}
	})
}

func TestS3Store_DeleteOldest(t *testing.T) {
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	fake := &fakeS3{pages: []*s3.ListObjectsV2Output{{Contents: []s3types.Object{
		obj("backups/p/new", base.Add(time.Hour)),
		obj("backups/p/old", base),
		obj("backups/p/", base.Add(-time.Hour)), // marker -- must be ignored
	}}}}
	store := &S3Store{api: fake, bucket: "b"}

	evicted, err := store.DeleteOldest(context.Background(), "backups/p", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if evicted != "backups/p/old" {
		t.Errorf("evicted %q, want backups/p/old", evicted)
	}
	if len(fake.deleted) != 1 || fake.deleted[0] != "backups/p/old" {
		t.Errorf("deleted = %v, want [backups/p/old]", fake.deleted)
	}
}

func TestS3Store_DeleteOldest_NothingToEvict(t *testing.T) {
	fake := &fakeS3{pages: []*s3.ListObjectsV2Output{{Contents: []s3types.Object{
		obj("backups/p/only", time.Now()),
	}}}}
	store := &S3Store{api: fake, bucket: "b"}

	if _, err := store.DeleteOldest(context.Background(), "backups/p", "backups/p/only"); err == nil {
		t.Fatal("expected an error when the only object is skipped")
	}
	if len(fake.deleted) != 0 {
		t.Errorf("nothing should be deleted, got %v", fake.deleted)
	}
}

func TestS3Store_DeleteOldest_ListError(t *testing.T) {
	fake := &fakeS3{listErr: errors.New("boom")}
	store := &S3Store{api: fake, bucket: "b"}
	if _, err := store.DeleteOldest(context.Background(), "backups/p", ""); err == nil {
		t.Fatal("expected error to propagate from list")
	}
}

func TestS3Store_ListObjects_PaginatesAndSkipsMarkers(t *testing.T) {
	base := time.Now()
	fake := &fakeS3{pages: []*s3.ListObjectsV2Output{
		{
			Contents:              []s3types.Object{obj("backups/a", base), obj("backups/dir/", base)},
			IsTruncated:           aws.Bool(true),
			NextContinuationToken: aws.String("t1"),
		},
		{
			Contents: []s3types.Object{obj("backups/b", base)},
		},
	}}
	store := &S3Store{api: fake, bucket: "b"}

	objs, err := store.ListObjects(context.Background(), "backups/")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(objs) != 2 {
		t.Fatalf("got %d objects, want 2 (markers skipped, pages merged)", len(objs))
	}
}
