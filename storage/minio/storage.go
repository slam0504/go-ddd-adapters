package miniostorage

import (
	minio "github.com/minio/minio-go/v7"

	"github.com/slam0504/go-ddd-core/pkg/errorsx"
)

// Storage implements storage.ObjectStorage + storage.PresignedURLer over a
// caller-configured *minio.Client and a single existing bucket.
type Storage struct {
	client *minio.Client
	bucket string
}

// Option customises a Storage. Reserved: v1 ships no options.
type Option func(*Storage)

// New wraps client and bucket. The bucket must already exist — New does not
// create it and does not contact the backend.
func New(client *minio.Client, bucket string, opts ...Option) (*Storage, error) {
	if client == nil {
		return nil, errorsx.New(errorsx.CodeInvalidArgument, "miniostorage: nil client")
	}
	if bucket == "" {
		return nil, errorsx.New(errorsx.CodeInvalidArgument, "miniostorage: empty bucket")
	}
	s := &Storage{client: client, bucket: bucket}
	for _, opt := range opts {
		opt(s)
	}
	return s, nil
}

func validateKey(key string) error {
	if key == "" {
		return errorsx.New(errorsx.CodeInvalidArgument, "miniostorage: empty key")
	}
	return nil
}
