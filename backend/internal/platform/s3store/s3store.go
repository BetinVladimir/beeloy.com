// Package s3store is the object storage abstraction used for content (articles, translations, UI dictionaries).
package s3store

import (
	"bytes"
	"context"
	"crypto/md5"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/smithy-go"
)

var (
	ErrNotFound = errors.New("s3store: object not found")
	ErrTooLarge = errors.New("s3store: object too large")
	// ErrConflict: a conditional write lost (the key exists, or the ETag changed).
	ErrConflict = errors.New("s3store: conflict")
)

type Object struct {
	Key          string
	ETag         string
	Size         int64
	LastModified time.Time
}

// ObjectStore keys are relative to the configured prefix (callers use "articles/…", "i18n/…").
type ObjectStore interface {
	// Get reads at most maxSize bytes; a larger object gives ErrTooLarge, a missing one ErrNotFound.
	Get(ctx context.Context, key string, maxSize int64) ([]byte, error)
	// Put writes (or replaces) an object. Only for mutable pointers (manifests, i18n manifest), never for revision files.
	Put(ctx context.Context, key string, data []byte, contentType string) error
	// List calls fn for every object under prefix in key order; the first error from fn stops the listing.
	List(ctx context.Context, prefix string, fn func(Object) error) error
	Exists(ctx context.Context, key string) (bool, error)
	// PutIfAbsent atomically creates the object; an existing key gives ErrConflict (immutability of revision files).
	PutIfAbsent(ctx context.Context, key string, data []byte, contentType string) error
	// GetWithETag reads an object together with its ETag (for compare-and-swap updates).
	GetWithETag(ctx context.Context, key string, maxSize int64) (data []byte, etag string, err error)
	// PutIfMatch atomically replaces the object only if its ETag is still etag; otherwise ErrConflict.
	PutIfMatch(ctx context.Context, key string, data []byte, contentType, etag string) error
}

type Config struct {
	Endpoint, Region, Bucket, AccessKey, SecretKey, Prefix string
}

type S3Store struct {
	client *s3.Client
	bucket string
	prefix string
}

func New(ctx context.Context, c Config) (*S3Store, error) {
	cfg, err := awsconfig.LoadDefaultConfig(ctx, awsconfig.WithRegion(c.Region),
		awsconfig.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(c.AccessKey, c.SecretKey, "")))
	if err != nil {
		return nil, err
	}
	cl := s3.NewFromConfig(cfg, func(o *s3.Options) {
		if c.Endpoint != "" {
			o.BaseEndpoint = aws.String(c.Endpoint)
		}
		o.UsePathStyle = true
	})
	return &S3Store{client: cl, bucket: c.Bucket, prefix: c.Prefix}, nil
}

func (s *S3Store) full(key string) string { return s.prefix + key }

func isNotFound(err error) bool {
	var ae smithy.APIError
	if errors.As(err, &ae) {
		switch ae.ErrorCode() {
		case "NoSuchKey", "NotFound", "404":
			return true
		}
	}
	return false
}

func (s *S3Store) Get(ctx context.Context, key string, maxSize int64) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	out, err := s.client.GetObject(ctx, &s3.GetObjectInput{Bucket: &s.bucket, Key: aws.String(s.full(key))})
	if err != nil {
		if isNotFound(err) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("s3store: get %s: %w", key, err)
	}
	defer out.Body.Close()
	b, err := io.ReadAll(io.LimitReader(out.Body, maxSize+1))
	if err != nil {
		return nil, fmt.Errorf("s3store: read %s: %w", key, err)
	}
	if int64(len(b)) > maxSize {
		return nil, ErrTooLarge
	}
	return b, nil
}

func (s *S3Store) Put(ctx context.Context, key string, data []byte, contentType string) error {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	_, err := s.client.PutObject(ctx, &s3.PutObjectInput{Bucket: &s.bucket, Key: aws.String(s.full(key)), Body: bytes.NewReader(data), ContentType: aws.String(contentType)})
	if err != nil {
		return fmt.Errorf("s3store: put %s: %w", key, err)
	}
	return nil
}

func isPrecondition(err error) bool {
	var ae smithy.APIError
	if errors.As(err, &ae) {
		switch ae.ErrorCode() {
		case "PreconditionFailed", "ConditionalRequestConflict", "412", "409":
			return true
		}
	}
	return false
}

func (s *S3Store) PutIfAbsent(ctx context.Context, key string, data []byte, contentType string) error {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	_, err := s.client.PutObject(ctx, &s3.PutObjectInput{Bucket: &s.bucket, Key: aws.String(s.full(key)), Body: bytes.NewReader(data),
		ContentType: aws.String(contentType), IfNoneMatch: aws.String("*")})
	if err != nil {
		if isPrecondition(err) {
			return ErrConflict
		}
		return fmt.Errorf("s3store: put-if-absent %s: %w", key, err)
	}
	return nil
}

func (s *S3Store) GetWithETag(ctx context.Context, key string, maxSize int64) ([]byte, string, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	out, err := s.client.GetObject(ctx, &s3.GetObjectInput{Bucket: &s.bucket, Key: aws.String(s.full(key))})
	if err != nil {
		if isNotFound(err) {
			return nil, "", ErrNotFound
		}
		return nil, "", fmt.Errorf("s3store: get %s: %w", key, err)
	}
	defer out.Body.Close()
	b, err := io.ReadAll(io.LimitReader(out.Body, maxSize+1))
	if err != nil {
		return nil, "", fmt.Errorf("s3store: read %s: %w", key, err)
	}
	if int64(len(b)) > maxSize {
		return nil, "", ErrTooLarge
	}
	return b, aws.ToString(out.ETag), nil
}

func (s *S3Store) PutIfMatch(ctx context.Context, key string, data []byte, contentType, etag string) error {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	_, err := s.client.PutObject(ctx, &s3.PutObjectInput{Bucket: &s.bucket, Key: aws.String(s.full(key)), Body: bytes.NewReader(data),
		ContentType: aws.String(contentType), IfMatch: aws.String(etag)})
	if err != nil {
		if isPrecondition(err) || isNotFound(err) {
			return ErrConflict
		}
		return fmt.Errorf("s3store: put-if-match %s: %w", key, err)
	}
	return nil
}

func (s *S3Store) Exists(ctx context.Context, key string) (bool, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	_, err := s.client.HeadObject(ctx, &s3.HeadObjectInput{Bucket: &s.bucket, Key: aws.String(s.full(key))})
	if err != nil {
		if isNotFound(err) {
			return false, nil
		}
		return false, fmt.Errorf("s3store: head %s: %w", key, err)
	}
	return true, nil
}

func (s *S3Store) List(ctx context.Context, prefix string, fn func(Object) error) error {
	p := s3.NewListObjectsV2Paginator(s.client, &s3.ListObjectsV2Input{Bucket: &s.bucket, Prefix: aws.String(s.full(prefix))})
	for p.HasMorePages() {
		page, err := p.NextPage(ctx)
		if err != nil {
			return fmt.Errorf("s3store: list %s: %w", prefix, err)
		}
		for _, o := range page.Contents {
			obj := Object{Key: strings.TrimPrefix(aws.ToString(o.Key), s.prefix), ETag: strings.Trim(aws.ToString(o.ETag), `"`), Size: aws.ToInt64(o.Size)}
			if o.LastModified != nil {
				obj.LastModified = *o.LastModified
			}
			if err := fn(obj); err != nil {
				return err
			}
		}
	}
	return nil
}

// MemStore is an in-memory ObjectStore for tests.
type MemStore struct {
	mu   sync.Mutex
	objs map[string]memObj
	// FailPut, when set, makes Put return it (to simulate S3 outages).
	FailPut error
}

type memObj struct {
	data []byte
	ct   string
	mod  time.Time
}

func NewMem() *MemStore { return &MemStore{objs: map[string]memObj{}} }

func (m *MemStore) Get(_ context.Context, key string, maxSize int64) ([]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	o, ok := m.objs[key]
	if !ok {
		return nil, ErrNotFound
	}
	if int64(len(o.data)) > maxSize {
		return nil, ErrTooLarge
	}
	return append([]byte(nil), o.data...), nil
}

func (m *MemStore) Put(_ context.Context, key string, data []byte, contentType string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.FailPut != nil {
		return m.FailPut
	}
	m.objs[key] = memObj{data: append([]byte(nil), data...), ct: contentType, mod: time.Now()}
	return nil
}

func memETag(b []byte) string { sum := md5.Sum(b); return `"` + hex.EncodeToString(sum[:]) + `"` }

func (m *MemStore) PutIfAbsent(_ context.Context, key string, data []byte, contentType string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.FailPut != nil {
		return m.FailPut
	}
	if _, ok := m.objs[key]; ok {
		return ErrConflict
	}
	m.objs[key] = memObj{data: append([]byte(nil), data...), ct: contentType, mod: time.Now()}
	return nil
}

func (m *MemStore) GetWithETag(_ context.Context, key string, maxSize int64) ([]byte, string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	o, ok := m.objs[key]
	if !ok {
		return nil, "", ErrNotFound
	}
	if int64(len(o.data)) > maxSize {
		return nil, "", ErrTooLarge
	}
	return append([]byte(nil), o.data...), memETag(o.data), nil
}

func (m *MemStore) PutIfMatch(_ context.Context, key string, data []byte, contentType, etag string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.FailPut != nil {
		return m.FailPut
	}
	o, ok := m.objs[key]
	if !ok || memETag(o.data) != etag {
		return ErrConflict
	}
	m.objs[key] = memObj{data: append([]byte(nil), data...), ct: contentType, mod: time.Now()}
	return nil
}

func (m *MemStore) Exists(_ context.Context, key string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, ok := m.objs[key]
	return ok, nil
}

func (m *MemStore) Delete(key string) { m.mu.Lock(); delete(m.objs, key); m.mu.Unlock() }

func (m *MemStore) List(_ context.Context, prefix string, fn func(Object) error) error {
	m.mu.Lock()
	keys := make([]string, 0, len(m.objs))
	for k := range m.objs {
		if strings.HasPrefix(k, prefix) {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	list := make([]Object, 0, len(keys))
	for _, k := range keys {
		o := m.objs[k]
		sum := md5.Sum(o.data)
		list = append(list, Object{Key: k, ETag: hex.EncodeToString(sum[:]), Size: int64(len(o.data)), LastModified: o.mod})
	}
	m.mu.Unlock()
	for _, o := range list {
		if err := fn(o); err != nil {
			return err
		}
	}
	return nil
}
