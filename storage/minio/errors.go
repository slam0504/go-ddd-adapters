package miniostorage

import (
	"fmt"
	"net/http"
	"strings"

	minio "github.com/minio/minio-go/v7"

	"github.com/slam0504/go-ddd-core/pkg/errorsx"
	"github.com/slam0504/go-ddd-core/ports/storage"
)

// mapError translates a minio-go error into the contract's error model:
// NoSuchKey wraps the uncoded storage.ErrNotFound sentinel; NoSuchBucket is
// CodeUnavailable (a deployment/config failure, never ErrNotFound); every
// other failure is errorsx-coded and never CodeUnknown.
// nolint:unused
func mapError(op string, err error) error {
	if err == nil {
		return nil
	}
	resp := minio.ToErrorResponse(err)
	switch resp.Code {
	case "NoSuchKey":
		return fmt.Errorf("miniostorage: %s: %w", op, storage.ErrNotFound)
	case "NoSuchBucket":
		return errorsx.Wrap(errorsx.CodeUnavailable, "miniostorage: "+op+": bucket does not exist", err)
	}
	return errorsx.Wrap(errorsx.CodeUnavailable, "miniostorage: "+op, err)
}

// canonicalMetadata merges user metadata from the x-amz-meta-* response
// headers (portable S3 surface) and minio-go's UserMetadata map (MinIO
// servers), canonicalizing keys to lowercase per the contract's portable
// metadata rule. Returns nil when no user metadata is present.
// nolint:unused
func canonicalMetadata(header http.Header, userMeta map[string]string) map[string]string {
	out := map[string]string{}
	const prefix = "X-Amz-Meta-"
	for k, vs := range header {
		if len(vs) > 0 && strings.HasPrefix(k, prefix) {
			out[strings.ToLower(strings.TrimPrefix(k, prefix))] = vs[0]
		}
	}
	for k, v := range userMeta {
		out[strings.ToLower(k)] = v
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
