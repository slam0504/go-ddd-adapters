package miniostorage

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"

	"github.com/slam0504/go-ddd-core/pkg/errorsx"
	"github.com/slam0504/go-ddd-core/ports/storage"
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

func TestGuards_FireBeforeNetwork(t *testing.T) {
	// The client points at 127.0.0.1:1; if a guard is missing, the call
	// dials and fails with a transport error instead of CodeInvalidArgument.
	s, err := New(testClient(t), "bucket")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx := context.Background()

	if code := errorsx.CodeOf(s.Put(ctx, "", strings.NewReader(""), 0, storage.PutOptions{})); code != errorsx.CodeInvalidArgument {
		t.Fatalf("Put empty key code = %v", code)
	}
	if code := errorsx.CodeOf(s.Put(ctx, "k", strings.NewReader("x"), -1, storage.PutOptions{})); code != errorsx.CodeInvalidArgument {
		t.Fatalf("Put negative size code = %v", code)
	}
	if _, _, err := s.Get(ctx, ""); errorsx.CodeOf(err) != errorsx.CodeInvalidArgument {
		t.Fatalf("Get empty key code = %v", errorsx.CodeOf(err))
	}
	if _, err := s.Stat(ctx, ""); errorsx.CodeOf(err) != errorsx.CodeInvalidArgument {
		t.Fatalf("Stat empty key code = %v", errorsx.CodeOf(err))
	}
	if code := errorsx.CodeOf(s.Delete(ctx, "")); code != errorsx.CodeInvalidArgument {
		t.Fatalf("Delete empty key code = %v", code)
	}
	if _, err := s.List(ctx, storage.ListOptions{Limit: 0}); errorsx.CodeOf(err) != errorsx.CodeInvalidArgument {
		t.Fatalf("List limit 0 code = %v", errorsx.CodeOf(err))
	}
	if _, err := s.PresignGet(ctx, "k", 0); errorsx.CodeOf(err) != errorsx.CodeInvalidArgument {
		t.Fatalf("PresignGet expires 0 code = %v", errorsx.CodeOf(err))
	}
	if _, err := s.PresignPut(ctx, "k", -time.Second); errorsx.CodeOf(err) != errorsx.CodeInvalidArgument {
		t.Fatalf("PresignPut negative expires code = %v", errorsx.CodeOf(err))
	}
	if _, err := s.PresignGet(ctx, "", time.Minute); errorsx.CodeOf(err) != errorsx.CodeInvalidArgument {
		t.Fatalf("PresignGet empty key code = %v", errorsx.CodeOf(err))
	}
	if _, err := s.PresignPut(ctx, "", time.Minute); errorsx.CodeOf(err) != errorsx.CodeInvalidArgument {
		t.Fatalf("PresignPut empty key code = %v", errorsx.CodeOf(err))
	}
}

func TestPresign_ExpiryOverSevenDaysIsInvalidArgument(t *testing.T) {
	s, err := New(testClient(t), "bucket")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	_, err = s.PresignGet(context.Background(), "k", 8*24*time.Hour)
	if errorsx.CodeOf(err) != errorsx.CodeInvalidArgument {
		t.Fatalf("PresignGet 8-day expiry code = %v, want CodeInvalidArgument", errorsx.CodeOf(err))
	}
}
