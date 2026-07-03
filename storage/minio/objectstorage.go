package miniostorage

import (
	"context"
	"io"

	minio "github.com/minio/minio-go/v7"

	"github.com/slam0504/go-ddd-core/pkg/errorsx"
	"github.com/slam0504/go-ddd-core/ports/storage"
)

var _ storage.ObjectStorage = (*Storage)(nil)

// Put stores body under key, unconditionally overwriting (last-writer-wins).
// The declared size is handed to the backend as-is; a backend-reported
// mismatch surfaces as an error (never silent truncation).
func (s *Storage) Put(ctx context.Context, key string, body io.Reader, size int64, opts storage.PutOptions) error {
	if err := validateKey(key); err != nil {
		return err
	}
	if size < 0 {
		return errorsx.New(errorsx.CodeInvalidArgument, "miniostorage: negative size")
	}
	_, err := s.client.PutObject(ctx, s.bucket, key, body, size, minio.PutObjectOptions{
		ContentType:  opts.ContentType,
		UserMetadata: opts.Metadata,
	})
	return mapError("put "+key, err)
}

// Get returns the object's content and info; the caller must close the
// reader. minio-go's GetObject is lazy, so Stat() is forced here to make a
// missing key surface as ErrNotFound at call time, not at first Read.
func (s *Storage) Get(ctx context.Context, key string) (io.ReadCloser, storage.ObjectInfo, error) {
	if err := validateKey(key); err != nil {
		return nil, storage.ObjectInfo{}, err
	}
	obj, err := s.client.GetObject(ctx, s.bucket, key, minio.GetObjectOptions{})
	if err != nil {
		return nil, storage.ObjectInfo{}, mapError("get "+key, err)
	}
	st, err := obj.Stat()
	if err != nil {
		_ = obj.Close()
		return nil, storage.ObjectInfo{}, mapError("get "+key, err)
	}
	return obj, toObjectInfo(st), nil
}

// Stat returns the object's info without content.
func (s *Storage) Stat(ctx context.Context, key string) (storage.ObjectInfo, error) {
	if err := validateKey(key); err != nil {
		return storage.ObjectInfo{}, err
	}
	st, err := s.client.StatObject(ctx, s.bucket, key, minio.StatObjectOptions{})
	if err != nil {
		return storage.ObjectInfo{}, mapError("stat "+key, err)
	}
	return toObjectInfo(st), nil
}

// Delete removes the object; deleting an absent key succeeds (S3 semantics
// pass the contract's idempotency requirement through unchanged).
func (s *Storage) Delete(ctx context.Context, key string) error {
	if err := validateKey(key); err != nil {
		return err
	}
	return mapError("delete "+key, s.client.RemoveObject(ctx, s.bucket, key, minio.RemoveObjectOptions{}))
}

// List returns one lexically-ordered page. NextToken is the last returned
// key (adapter-owned cursor); the next page resumes via StartAfter. A
// Limit+1 lookahead decides whether a next page exists. Any string is a
// structurally valid cursor, so no malformed-token branch exists here.
func (s *Storage) List(ctx context.Context, opts storage.ListOptions) (storage.ListPage, error) {
	if opts.Limit <= 0 {
		return storage.ListPage{}, errorsx.New(errorsx.CodeInvalidArgument, "miniostorage: non-positive limit")
	}
	// Own the iteration lifetime: minio-go stops listing when ctx ends.
	lctx, cancel := context.WithCancel(ctx)
	defer cancel()

	page := storage.ListPage{}
	for oi := range s.client.ListObjectsIter(lctx, s.bucket, minio.ListObjectsOptions{
		Prefix:     opts.Prefix,
		Recursive:  true,
		StartAfter: opts.Token,
	}) {
		if oi.Err != nil {
			return storage.ListPage{}, mapError("list", oi.Err)
		}
		if len(page.Objects) == opts.Limit { // Limit+1th object: lookahead hit
			page.NextToken = page.Objects[opts.Limit-1].Key
			return page, nil
		}
		page.Objects = append(page.Objects, storage.ObjectInfo{
			Key:         oi.Key,
			Size:        oi.Size,
			ContentType: oi.ContentType,
			ETag:        oi.ETag,
			UpdatedAt:   oi.LastModified,
			// Metadata deliberately nil: the contract populates it on
			// Get/Stat only.
		})
	}
	if err := ctx.Err(); err != nil && len(page.Objects) < opts.Limit {
		// Iterator may end early because ctx was cancelled mid-walk.
		return storage.ListPage{}, errorsx.Wrap(errorsx.CodeUnavailable, "miniostorage: list interrupted", err)
	}
	return page, nil
}

func toObjectInfo(oi minio.ObjectInfo) storage.ObjectInfo {
	return storage.ObjectInfo{
		Key:         oi.Key,
		Size:        oi.Size,
		ContentType: oi.ContentType,
		ETag:        oi.ETag,
		UpdatedAt:   oi.LastModified,
		Metadata:    canonicalMetadata(oi.Metadata, oi.UserMetadata),
	}
}
