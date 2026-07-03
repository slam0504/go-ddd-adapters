// Package miniostorage implements ports/storage.ObjectStorage and
// storage.PresignedURLer over an S3-compatible backend via minio-go v7
// (MinIO, AWS S3, and other S3-compatible services).
//
// The bucket is the isolation unit and must already exist: New does not
// create it (bucket provisioning is a deployment concern). A missing bucket
// surfaces as errorsx.CodeUnavailable — deliberately NOT storage.ErrNotFound,
// which is reserved for missing objects.
//
// List pagination: ListPage.NextToken is an adapter-owned cursor (the last
// returned key, resumed via the backend's StartAfter listing), not an S3
// continuation token. Pages are assembled with a Limit+1 lookahead. The
// backend page cap does not limit callers: the adapter iterates internally.
package miniostorage
