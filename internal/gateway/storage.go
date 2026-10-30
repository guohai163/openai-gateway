package gateway

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"time"

	"github.com/google/uuid"
	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

type ObjectStorage interface {
	Put(context.Context, string, []byte) error
	Get(context.Context, string) ([]byte, error)
	Delete(context.Context, string) error
}
type s3Storage struct {
	client *minio.Client
	bucket string
}

func newS3Storage(c S3Config) (ObjectStorage, error) {
	u, err := url.Parse(c.Endpoint)
	if err != nil {
		return nil, err
	}
	lookup := minio.BucketLookupDNS
	if c.PathStyle {
		lookup = minio.BucketLookupPath
	}
	client, err := minio.New(u.Host, &minio.Options{Creds: credentials.NewStaticV4(c.AccessKey, c.SecretKey, c.SessionToken), Secure: u.Scheme == "https", Region: c.Region, BucketLookup: lookup, MaxRetries: 1})
	if err != nil {
		return nil, err
	}
	return &s3Storage{client: client, bucket: c.Bucket}, nil
}
func (s *s3Storage) Put(ctx context.Context, key string, data []byte) error {
	_, err := s.client.PutObject(ctx, s.bucket, key, bytes.NewReader(data), int64(len(data)), minio.PutObjectOptions{ContentType: "application/json", DisableMultipart: true})
	return err
}
func (s *s3Storage) Get(ctx context.Context, key string) ([]byte, error) {
	obj, err := s.client.GetObject(ctx, s.bucket, key, minio.GetObjectOptions{})
	if err != nil {
		return nil, err
	}
	defer obj.Close()
	data, err := io.ReadAll(io.LimitReader(obj, maxBody+1))
	if err == nil && len(data) > maxBody {
		return nil, errors.New("object too large")
	}
	return data, err
}
func (s *s3Storage) Delete(ctx context.Context, key string) error {
	return s.client.RemoveObject(ctx, s.bucket, key, minio.RemoveObjectOptions{})
}

func (a *App) storageTest(w http.ResponseWriter, r *http.Request) {
	if a.Storage == nil {
		writeError(w, 404, "not_found")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	key := "_gateway-tests/" + uuid.NewString() + ".json"
	payload := []byte(`{"test":"ai-gateway-storage"}`)
	if err := a.Storage.Put(ctx, key, payload); err != nil {
		writeError(w, 502, "storage_write_failed")
		return
	}
	defer func() {
		cleanup, c := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer c()
		_ = a.Storage.Delete(cleanup, key)
	}()
	data, err := a.Storage.Get(ctx, key)
	if err != nil || !bytes.Equal(data, payload) {
		writeError(w, 502, "storage_read_failed")
		return
	}
	a.audit(ctx, "test", "s3", "")
	writeJSON(w, 200, map[string]any{"ok": true, "write": true, "read": true})
}
