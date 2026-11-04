// Package testutil starts disposable PostgreSQL and Redis containers for integration tests.
// One container per test process is shared; every test gets its own database / Redis DB index.
package testutil

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"beeloy.com/backend/internal/platform/migrate"
	"beeloy.com/backend/internal/platform/s3store"
	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	goredis "github.com/redis/go-redis/v9"
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	tcredis "github.com/testcontainers/testcontainers-go/modules/redis"
	"github.com/testcontainers/testcontainers-go/wait"
)

var (
	pgOnce   sync.Once
	pgErr    error
	pgAdmin  string // URL of the "postgres" database
	pgBase   string // URL prefix without database name
	pgTmpl   = "tmpl"
	pgSeq    atomic.Int64
	rdOnce   sync.Once
	rdErr    error
	rdURL    string
	rdSeq    atomic.Int64
	ctrs     []testcontainers.Container
	ctrsLock sync.Mutex
)

// MigrationsDir finds the repository migrations directory by walking up from the working directory.
func MigrationsDir() string {
	d, _ := os.Getwd()
	for i := 0; i < 8; i++ {
		p := filepath.Join(d, "migrations")
		if st, err := os.Stat(p); err == nil && st.IsDir() {
			if m, _ := filepath.Glob(filepath.Join(p, "*.up.sql")); len(m) > 0 {
				return p
			}
		}
		d = filepath.Dir(d)
	}
	panic("testutil: migrations directory not found")
}

func startPG() {
	ctx := context.Background()
	c, err := tcpostgres.Run(ctx, "postgres:17-alpine",
		tcpostgres.WithDatabase("postgres"), tcpostgres.WithUsername("test"), tcpostgres.WithPassword("test"),
		tcpostgres.BasicWaitStrategies())
	if err != nil {
		pgErr = err
		return
	}
	ctrsLock.Lock()
	ctrs = append(ctrs, c)
	ctrsLock.Unlock()
	host, _ := c.Host(ctx)
	port, _ := c.MappedPort(ctx, "5432/tcp")
	pgBase = fmt.Sprintf("postgres://test:test@%s:%s/", host, port.Port())
	pgAdmin = pgBase + "postgres?sslmode=disable"
	adm, err := pgxpool.New(ctx, pgAdmin)
	if err != nil {
		pgErr = err
		return
	}
	defer adm.Close()
	if _, err := adm.Exec(ctx, "CREATE DATABASE "+pgTmpl); err != nil {
		pgErr = err
		return
	}
	pgErr = migrate.Up(pgBase+pgTmpl+"?sslmode=disable", MigrationsDir())
}

// PGURL returns the URL of a fresh, fully migrated database.
func PGURL(t testing.TB) string {
	t.Helper()
	pgOnce.Do(startPG)
	if pgErr != nil {
		t.Fatalf("postgres container: %v", pgErr)
	}
	ctx := context.Background()
	adm, err := pgxpool.New(ctx, pgAdmin)
	if err != nil {
		t.Fatal(err)
	}
	defer adm.Close()
	name := fmt.Sprintf("t_%d_%d", os.Getpid(), pgSeq.Add(1))
	if _, err := adm.Exec(ctx, fmt.Sprintf("CREATE DATABASE %s TEMPLATE %s", name, pgTmpl)); err != nil {
		t.Fatal(err)
	}
	return pgBase + name + "?sslmode=disable"
}

// EmptyPGURL returns the URL of a fresh database WITHOUT migrations.
func EmptyPGURL(t testing.TB) string {
	t.Helper()
	pgOnce.Do(startPG)
	if pgErr != nil {
		t.Fatalf("postgres container: %v", pgErr)
	}
	ctx := context.Background()
	adm, err := pgxpool.New(ctx, pgAdmin)
	if err != nil {
		t.Fatal(err)
	}
	defer adm.Close()
	name := fmt.Sprintf("e_%d_%d", os.Getpid(), pgSeq.Add(1))
	if _, err := adm.Exec(ctx, "CREATE DATABASE "+name); err != nil {
		t.Fatal(err)
	}
	return pgBase + name + "?sslmode=disable"
}

// PG returns a pool on a fresh migrated database; it is closed at test end.
func PG(t testing.TB) *pgxpool.Pool {
	t.Helper()
	pool, err := pgxpool.New(context.Background(), PGURL(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	return pool
}

func startRedis() {
	ctx := context.Background()
	c, err := tcredis.Run(ctx, "redis:7-alpine")
	if err != nil {
		rdErr = err
		return
	}
	ctrsLock.Lock()
	ctrs = append(ctrs, c)
	ctrsLock.Unlock()
	rdURL, rdErr = c.ConnectionString(ctx)
}

// RedisURL returns a Redis URL with a database index unique for the test (0..15).
func RedisURL(t testing.TB) string {
	t.Helper()
	rdOnce.Do(startRedis)
	if rdErr != nil {
		t.Fatalf("redis container: %v", rdErr)
	}
	return fmt.Sprintf("%s/%d", rdURL, rdSeq.Add(1)%15+1)
}

// Redis returns a client on a clean DB index; it is flushed at start and closed at end.
func Redis(t testing.TB) *goredis.Client {
	t.Helper()
	opt, err := goredis.ParseURL(RedisURL(t))
	if err != nil {
		t.Fatal(err)
	}
	c := goredis.NewClient(opt)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := c.FlushDB(ctx).Err(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

// Cleanup terminates shared containers; call from TestMain after m.Run().
func Cleanup() {
	ctrsLock.Lock()
	defer ctrsLock.Unlock()
	for _, c := range ctrs {
		_ = testcontainers.TerminateContainer(c)
	}
	ctrs = nil
}

// InsertUser inserts an active user directly (no identity import: avoids cycles) and returns its id.
func InsertUser(t testing.TB, q interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}, nickname string) uuid.UUID {
	t.Helper()
	id := uuid.Must(uuid.NewV7())
	if _, err := q.Exec(context.Background(), `INSERT INTO users (id, nickname) VALUES ($1,$2)`, id, nickname); err != nil {
		t.Fatal(err)
	}
	return id
}

var (
	s3Once sync.Once
	s3Err  error
	s3Conf s3store.Config
	s3Seq  atomic.Int64
)

func startS3() {
	ctx := context.Background()
	// TEST_S3_ENDPOINT (+ TEST_S3_ACCESS_KEY / TEST_S3_SECRET_KEY) runs the suite against an existing S3-compatible server, e.g. the MinIO of compose.dev.yml.
	if ep := os.Getenv("TEST_S3_ENDPOINT"); ep != "" {
		s3Conf = s3store.Config{Endpoint: ep, Region: "us-east-1", AccessKey: os.Getenv("TEST_S3_ACCESS_KEY"), SecretKey: os.Getenv("TEST_S3_SECRET_KEY")}
		return
	}
	c, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			Image:        "adobe/s3mock:latest",
			ExposedPorts: []string{"9090/tcp"},
			Env:          map[string]string{"COM_ADOBE_TESTING_S3MOCK_STORE_RETAIN_FILES_ON_EXIT": "false"},
			WaitingFor:   wait.ForListeningPort("9090/tcp").WithStartupTimeout(2 * time.Minute),
		},
		Started: true,
	})
	if err != nil {
		s3Err = err
		return
	}
	ctrsLock.Lock()
	ctrs = append(ctrs, c)
	ctrsLock.Unlock()
	host, _ := c.Host(ctx)
	port, _ := c.MappedPort(ctx, "9090/tcp")
	s3Conf = s3store.Config{Endpoint: fmt.Sprintf("http://%s:%s", host, port.Port()), Region: "us-east-1", AccessKey: "test", SecretKey: "test"}
}

// S3Config returns the settings of a fresh bucket on a shared S3-compatible container (adobe/s3mock).
func S3Config(t testing.TB, prefix string) s3store.Config {
	t.Helper()
	s3Once.Do(startS3)
	if s3Err != nil {
		t.Fatalf("s3 container: %v", s3Err)
	}
	cfg := s3Conf
	cfg.Bucket = fmt.Sprintf("b%d-%d", os.Getpid(), s3Seq.Add(1))
	cfg.Prefix = prefix
	ac, err := awsconfig.LoadDefaultConfig(context.Background(), awsconfig.WithRegion(cfg.Region),
		awsconfig.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(cfg.AccessKey, cfg.SecretKey, "")))
	if err != nil {
		t.Fatal(err)
	}
	cl := awss3.NewFromConfig(ac, func(o *awss3.Options) { o.BaseEndpoint = aws.String(cfg.Endpoint); o.UsePathStyle = true })
	if _, err := cl.CreateBucket(context.Background(), &awss3.CreateBucketInput{Bucket: &cfg.Bucket}); err != nil {
		t.Fatal(err)
	}
	return cfg
}

// S3 returns an S3Store on a fresh bucket.
func S3(t testing.TB, prefix string) *s3store.S3Store {
	t.Helper()
	st, err := s3store.New(context.Background(), S3Config(t, prefix))
	if err != nil {
		t.Fatal(err)
	}
	return st
}
