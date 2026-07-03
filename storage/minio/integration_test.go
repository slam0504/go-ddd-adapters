//go:build integration

package miniostorage

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	minio "github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
	tcminio "github.com/testcontainers/testcontainers-go/modules/minio"

	"github.com/slam0504/go-ddd-core/pkg/errorsx"
	"github.com/slam0504/go-ddd-core/ports/storage"
	"github.com/slam0504/go-ddd-core/ports/storage/storagetest"
)

var bucketSeq atomic.Int64

// startMinio launches one MinIO container per test function and returns a
// connected client.
func startMinio(t *testing.T) *minio.Client {
	t.Helper()
	ctx := context.Background()
	c, err := tcminio.Run(ctx, "minio/minio:RELEASE.2024-01-16T16-07-38Z")
	if err != nil {
		t.Fatalf("start minio container: %v", err)
	}
	t.Cleanup(func() { _ = c.Terminate(context.Background()) })
	endpoint, err := c.ConnectionString(ctx)
	if err != nil {
		t.Fatalf("connection string: %v", err)
	}
	client, err := minio.New(endpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(c.Username, c.Password, ""),
		Secure: false,
	})
	if err != nil {
		t.Fatalf("minio client: %v", err)
	}
	return client
}

// freshBucket creates a new bucket (the adapter's isolation unit — New does
// not create buckets, the harness owns provisioning). Bucket names must be
// at least 3 characters per MinIO/S3 naming rules, so we use a "tst-" prefix.
func freshBucket(t *testing.T, client *minio.Client) string {
	t.Helper()
	name := fmt.Sprintf("tst-%d", bucketSeq.Add(1))
	if err := client.MakeBucket(context.Background(), name, minio.MakeBucketOptions{}); err != nil {
		t.Fatalf("make bucket %s: %v", name, err)
	}
	return name
}

func TestRunContract(t *testing.T) {
	client := startMinio(t)
	storagetest.RunContract(t, func(t *testing.T) storage.ObjectStorage {
		s, err := New(client, freshBucket(t, client))
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		return s
	})
}

func TestPresign_EndToEnd(t *testing.T) {
	client := startMinio(t)
	s, err := New(client, freshBucket(t, client))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx := context.Background()
	body := []byte("presigned payload")

	putURL, err := s.PresignPut(ctx, "p/obj", time.Minute)
	if err != nil {
		t.Fatalf("PresignPut: %v", err)
	}
	req, _ := http.NewRequestWithContext(ctx, http.MethodPut, putURL, bytes.NewReader(body))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("HTTP PUT via presigned URL: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("presigned PUT status = %d, want 200", resp.StatusCode)
	}

	getURL, err := s.PresignGet(ctx, "p/obj", time.Minute)
	if err != nil {
		t.Fatalf("PresignGet: %v", err)
	}
	gresp, err := http.Get(getURL) //nolint:noctx
	if err != nil {
		t.Fatalf("HTTP GET via presigned URL: %v", err)
	}
	defer func() { _ = gresp.Body.Close() }()
	got, _ := io.ReadAll(gresp.Body)
	if !bytes.Equal(got, body) {
		t.Fatalf("presigned GET body = %q, want %q", got, body)
	}
}

func TestList_MultiPageWalkAgainstRealBackend(t *testing.T) {
	client := startMinio(t)
	s, err := New(client, freshBucket(t, client))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx := context.Background()
	const n = 25
	want := map[string]bool{}
	for i := 0; i < n; i++ {
		key := fmt.Sprintf("walk/%03d", i)
		if err := s.Put(ctx, key, strings.NewReader("v"), 1, storage.PutOptions{}); err != nil {
			t.Fatalf("Put %s: %v", key, err)
		}
		want[key] = true
	}
	got := map[string]bool{}
	token, pages := "", 0
	for {
		pages++
		if pages > 10 {
			t.Fatal("walk did not terminate")
		}
		page, err := s.List(ctx, storage.ListOptions{Prefix: "walk/", Token: token, Limit: 10})
		if err != nil {
			t.Fatalf("List page %d: %v", pages, err)
		}
		for _, o := range page.Objects {
			if got[o.Key] {
				t.Fatalf("duplicate %q", o.Key)
			}
			got[o.Key] = true
		}
		if page.NextToken == "" {
			break
		}
		token = page.NextToken
	}
	if len(got) != n {
		t.Fatalf("walk yielded %d keys, want %d", len(got), n)
	}
	if pages != 3 {
		t.Fatalf("pages = %d, want 3 (25 objects / Limit 10)", pages)
	}
}

func TestNoSuchBucket_IsUnavailableNotErrNotFound(t *testing.T) {
	client := startMinio(t)
	s, err := New(client, "no-such-bucket-xyz") // deliberately not created
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	_, err = s.Stat(context.Background(), "k")
	if errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("missing BUCKET must not read as missing OBJECT: %v", err)
	}
	if errorsx.CodeOf(err) != errorsx.CodeUnavailable {
		t.Fatalf("Stat vs missing bucket code = %v, want CodeUnavailable", errorsx.CodeOf(err))
	}
}

func TestMetadata_MixedCaseKeysComeBackLowercase(t *testing.T) {
	client := startMinio(t)
	s, err := New(client, freshBucket(t, client))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx := context.Background()
	err = s.Put(ctx, "m", strings.NewReader("v"), 1, storage.PutOptions{
		Metadata: map[string]string{"X-Custom-Key": "val"},
	})
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	info, err := s.Stat(ctx, "m")
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if info.Metadata["x-custom-key"] != "val" {
		t.Fatalf("Metadata = %v, want lowercase key x-custom-key=val", info.Metadata)
	}
	if _, mixed := info.Metadata["X-Custom-Key"]; mixed {
		t.Fatalf("Metadata leaked a non-canonicalized mixed-case key: %v", info.Metadata)
	}
}
