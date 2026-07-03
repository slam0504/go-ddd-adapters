package miniostorage

import (
	"context"
	"time"

	"github.com/slam0504/go-ddd-core/pkg/errorsx"
	"github.com/slam0504/go-ddd-core/ports/storage"
)

var _ storage.PresignedURLer = (*Storage)(nil)

// PresignGet returns a time-limited unauthenticated download URL.
func (s *Storage) PresignGet(ctx context.Context, key string, expires time.Duration) (string, error) {
	if err := validatePresign(key, expires); err != nil {
		return "", err
	}
	u, err := s.client.PresignedGetObject(ctx, s.bucket, key, expires, nil)
	if err != nil {
		return "", mapError("presign get "+key, err)
	}
	return u.String(), nil
}

// PresignPut returns a time-limited unauthenticated upload URL.
func (s *Storage) PresignPut(ctx context.Context, key string, expires time.Duration) (string, error) {
	if err := validatePresign(key, expires); err != nil {
		return "", err
	}
	u, err := s.client.PresignedPutObject(ctx, s.bucket, key, expires)
	if err != nil {
		return "", mapError("presign put "+key, err)
	}
	return u.String(), nil
}

func validatePresign(key string, expires time.Duration) error {
	if err := validateKey(key); err != nil {
		return err
	}
	if expires <= 0 {
		return errorsx.New(errorsx.CodeInvalidArgument, "miniostorage: non-positive expires")
	}
	return nil
}
