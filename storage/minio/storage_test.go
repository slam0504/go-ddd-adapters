package miniostorage

import (
	"testing"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"

	"github.com/slam0504/go-ddd-core/pkg/errorsx"
)

// testClient returns a real *minio.Client pointing at a dead endpoint. It
// never gets dialed by guard tests: argument validation must fire before
// any network use (contract precedence).
func testClient(t *testing.T) *minio.Client {
	t.Helper()
	c, err := minio.New("127.0.0.1:1", &minio.Options{
		Creds: credentials.NewStaticV4("x", "y", ""), Secure: false,
	})
	if err != nil {
		t.Fatalf("minio.New: %v", err)
	}
	return c
}

func TestNew_NilClientRejected(t *testing.T) {
	_, err := New(nil, "bucket")
	if errorsx.CodeOf(err) != errorsx.CodeInvalidArgument {
		t.Fatalf("New(nil client) code = %v, want CodeInvalidArgument", errorsx.CodeOf(err))
	}
}

func TestNew_EmptyBucketRejected(t *testing.T) {
	_, err := New(testClient(t), "")
	if errorsx.CodeOf(err) != errorsx.CodeInvalidArgument {
		t.Fatalf("New(empty bucket) code = %v, want CodeInvalidArgument", errorsx.CodeOf(err))
	}
}
