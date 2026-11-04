package s3store_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"beeloy.com/backend/internal/platform/s3store"
	"beeloy.com/backend/internal/testutil"
)

func TestMain(m *testing.M) {
	code := m.Run()
	testutil.Cleanup()
	os.Exit(code)
}

// contract is run against both implementations.
func contract(t *testing.T, st s3store.ObjectStore) {
	ctx := context.Background()
	if _, err := st.Get(ctx, "nope", 10); !errors.Is(err, s3store.ErrNotFound) {
		t.Fatalf("missing key: %v", err)
	}
	if ok, err := st.Exists(ctx, "a/1.json"); err != nil || ok {
		t.Fatalf("exists before put: %v %v", ok, err)
	}
	for i := 3; i >= 1; i-- {
		if err := st.Put(ctx, fmt.Sprintf("a/%d.json", i), []byte(strings.Repeat("x", i)), "application/json"); err != nil {
			t.Fatal(err)
		}
	}
	_ = st.Put(ctx, "b/1.json", []byte("other"), "application/json")
	if b, err := st.Get(ctx, "a/2.json", 10); err != nil || string(b) != "xx" {
		t.Fatalf("get: %q %v", b, err)
	}
	if _, err := st.Get(ctx, "a/3.json", 2); !errors.Is(err, s3store.ErrTooLarge) {
		t.Fatalf("too large: %v", err)
	}
	if ok, _ := st.Exists(ctx, "a/1.json"); !ok {
		t.Fatal("exists after put")
	}
	_ = st.Put(ctx, "a/1.json", []byte("replaced"), "application/json") // overwrite allowed at this layer
	if b, _ := st.Get(ctx, "a/1.json", 20); string(b) != "replaced" {
		t.Fatal("overwrite")
	}
	var keys []string
	if err := st.List(ctx, "a/", func(o s3store.Object) error {
		if o.ETag == "" || o.Size == 0 {
			t.Errorf("object info: %+v", o)
		}
		keys = append(keys, o.Key)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if strings.Join(keys, ",") != "a/1.json,a/2.json,a/3.json" {
		t.Fatalf("list: %v", keys)
	}
	// conditional writes: exactly one of many concurrent creators wins and the winner's bytes stay
	var wg sync.WaitGroup
	var won int32
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			switch err := st.PutIfAbsent(ctx, "rev/1.json", []byte(fmt.Sprintf("writer-%d", i)), "application/json"); {
			case err == nil:
				atomic.AddInt32(&won, 1)
			case !errors.Is(err, s3store.ErrConflict):
				t.Errorf("put-if-absent: %v", err)
			}
		}(i)
	}
	wg.Wait()
	if won != 1 {
		t.Fatalf("put-if-absent winners: %d", won)
	}
	if err := st.PutIfAbsent(ctx, "a/2.json", []byte("zz"), "application/json"); !errors.Is(err, s3store.ErrConflict) {
		t.Fatalf("existing key must conflict: %v", err)
	}
	if b, _ := st.Get(ctx, "a/2.json", 10); string(b) != "xx" {
		t.Fatal("put-if-absent must not change an existing object")
	}
	// compare-and-swap
	_ = st.Put(ctx, "m.json", []byte("v1"), "application/json")
	_, etag, err := st.GetWithETag(ctx, "m.json", 10)
	if err != nil || etag == "" {
		t.Fatalf("etag: %q %v", etag, err)
	}
	if err := st.PutIfMatch(ctx, "m.json", []byte("v2"), "application/json", etag); err != nil {
		t.Fatalf("cas: %v", err)
	}
	if err := st.PutIfMatch(ctx, "m.json", []byte("v3"), "application/json", etag); !errors.Is(err, s3store.ErrConflict) {
		t.Fatalf("stale etag must conflict: %v", err)
	}
	if b, _ := st.Get(ctx, "m.json", 10); string(b) != "v2" {
		t.Fatalf("after cas: %s", b)
	}
	stop := errors.New("stop")
	n := 0
	if err := st.List(ctx, "a/", func(o s3store.Object) error { n++; return stop }); !errors.Is(err, stop) || n != 1 {
		t.Fatalf("list must stop on fn error: %v %d", err, n)
	}
}

func TestMemStore(t *testing.T) { contract(t, s3store.NewMem()) }

func TestS3StoreCompatible(t *testing.T) {
	st := testutil.S3(t, "pfx/")
	contract(t, st)
}
