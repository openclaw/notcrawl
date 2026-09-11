package share

import (
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/openclaw/notcrawl/internal/store"
)

func TestSnapshotIntegerRoundTrip(t *testing.T) {
	ctx := context.Background()
	src, err := store.Open(filepath.Join(t.TempDir(), "source.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = src.Close() })
	values := []int64{1<<53 + 1, -(1<<53 + 1), math.MaxInt64, math.MinInt64, 0, 42}
	for i, value := range values {
		id := fmt.Sprintf("block-%d", i)
		if err := src.UpsertBlock(ctx, store.Block{
			ID: id, Type: "text", Text: "fixture", DisplayOrder: value,
			CreatedTime: value, Alive: true, Source: "test", SyncedAt: value,
		}); err != nil {
			t.Fatal(err)
		}
		if _, err := src.DB().ExecContext(ctx, `insert into sync_state
			(source, entity_type, entity_id, cursor, synced_at) values ('test', 'block', ?, 'cursor', ?)`,
			id, value); err != nil {
			t.Fatal(err)
		}
	}
	repo := t.TempDir()
	if _, err := Publish(ctx, src, PublishOptions{RepoPath: repo}); err != nil {
		t.Fatal(err)
	}
	for _, restore := range []bool{false, true} {
		t.Run(fmt.Sprintf("restore=%t", restore), func(t *testing.T) {
			dst, err := store.Open(filepath.Join(t.TempDir(), "destination.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer dst.Close()
			if err := dst.UpsertBlock(ctx, store.Block{
				ID: "local-only", Type: "text", Text: "keep on merge",
				Alive: true, Source: "test", SyncedAt: 7,
			}); err != nil {
				t.Fatal(err)
			}
			if _, err := ImportWithOptions(ctx, dst, repo, ImportOptions{Restore: restore}); err != nil {
				t.Fatal(err)
			}
			for i, want := range values {
				id := fmt.Sprintf("block-%d", i)
				for _, query := range []string{
					`select display_order, typeof(display_order) from blocks where id = ?`,
					`select created_time, typeof(created_time) from blocks where id = ?`,
					`select synced_at, typeof(synced_at) from sync_state where entity_id = ?`,
					`select synced_at, typeof(synced_at) from record_sources where record_id = ?`,
				} {
					var got int64
					var storageType string
					if err := dst.DB().QueryRowContext(ctx, query, id).Scan(&got, &storageType); err != nil {
						t.Errorf("%s: %v", id, err)
					} else if got != want || storageType != "integer" {
						t.Errorf("%s: got %d (%s), want %d (integer); query=%s", id, got, storageType, want, query)
					}
				}
			}
			var localRows int
			if err := dst.DB().QueryRowContext(ctx, `select count(*) from blocks where id = 'local-only'`).Scan(&localRows); err != nil {
				t.Fatal(err)
			}
			if (localRows == 1) == restore {
				t.Fatalf("local rows=%d, restore=%t", localRows, restore)
			}
			repeated, err := ImportWithOptions(ctx, dst, repo, ImportOptions{RetainRevisions: true})
			if err != nil {
				t.Fatal(err)
			}
			if repeated.Revisions != 0 {
				t.Fatalf("unchanged import retained %d revisions", repeated.Revisions)
			}
		})
	}
}

func TestDecodeImportRowNumbers(t *testing.T) {
	row, err := decodeImportRow([]byte(`{
		"large":9007199254740993,"max":9223372036854775807,"min":-9223372036854775808,
		"zero":-0,"decimal":12.75,"exponent":4.2e1,"flag":true,"false":false,
		"null":null,"text":"9007199254740993","raw_json":"{\"n\":9007199254740993}"
	}`))
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]any{
		"large": int64(1<<53 + 1), "max": int64(math.MaxInt64), "min": int64(math.MinInt64),
		"zero": int64(0), "decimal": 12.75, "exponent": float64(42), "flag": true, "false": false,
		"null": nil, "text": "9007199254740993", "raw_json": `{"n":9007199254740993}`,
	}
	if !reflect.DeepEqual(row, want) {
		t.Fatalf("decoded row = %#v, want %#v", row, want)
	}
	for key, expected := range map[string]int64{
		"large": 1<<53 + 1, "max": math.MaxInt64, "min": math.MinInt64,
		"zero": 0, "decimal": 12, "exponent": 42, "flag": 0, "null": 0, "text": 0,
	} {
		if got := rowInt64(row, key); got != expected {
			t.Errorf("%s conversion=%d, want %d", key, got, expected)
		}
	}
}

func TestDecodeImportRowRejectsInvalidNumbersAndTrailingValues(t *testing.T) {
	for _, raw := range []string{
		`{"n":9223372036854775808}`, `{"n":-9223372036854775809}`,
		`{"n":1e400}`, `{"n":-1e400}`, `{"n":1} {}`, `{"n":1} null`,
		`{"n":1} trailing`, `{"n":NaN}`, `{"n":01}`, `{"n":`, `[]`,
	} {
		t.Run(raw, func(t *testing.T) {
			if _, err := decodeImportRow([]byte(raw)); err == nil {
				t.Fatal("expected rejection")
			}
		})
	}
	for _, raw := range []string{`{}`, `null`, " {\"n\":1} \t"} {
		if _, err := decodeImportRow([]byte(raw)); err != nil {
			t.Fatalf("existing valid row %q rejected: %v", raw, err)
		}
	}
}

func TestCanonicalImportRejectsOutOfRangeFloats(t *testing.T) {
	for _, table := range []string{"pages", "blocks", "comments"} {
		fields := []string{"created_time", "last_edited_time", "synced_at", "alive"}
		if table == "blocks" {
			fields = append(fields, "display_order")
		}
		for _, field := range fields {
			for _, value := range []float64{0x1p63, math.Nextafter(-0x1p63, math.Inf(-1))} {
				t.Run(fmt.Sprintf("%s/%s/%g", table, field, value), func(t *testing.T) {
					err := importCanonicalRow(context.Background(), nil, table, map[string]any{field: value})
					if err == nil || err.Error() != "snapshot canonical integer is out of range" {
						t.Fatalf("bounds check = %v", err)
					}
				})
			}
		}
	}
}

func TestSnapshotNumericFailureRollsBack(t *testing.T) {
	for _, tc := range []struct {
		name, table, field, value, suffix string
		mergeOnly                         bool
	}{
		{name: "integer-overflow", table: "blocks", field: "display_order", value: "9223372036854775808"},
		{name: "integer-underflow", table: "record_sources", field: "synced_at", value: "-9223372036854775809"},
		{name: "decimal-overflow", table: "blocks", field: "display_order", value: "1e400"},
		{name: "canonical-upper-bound", table: "blocks", field: "display_order", value: "9223372036854775808.0", mergeOnly: true},
		{name: "canonical-lower-bound", table: "blocks", field: "display_order", value: "-9223372036854777856.0", mergeOnly: true},
		{name: "trailing-value", table: "blocks", field: "display_order", value: "42", suffix: " {}"},
		{name: "trailing-garbage", table: "blocks", field: "display_order", value: "42", suffix: " garbage"},
	} {
		for _, restore := range []bool{false, true} {
			if restore && tc.mergeOnly {
				continue
			}
			t.Run(fmt.Sprintf("%s/restore=%t", tc.name, restore), func(t *testing.T) {
				ctx := context.Background()
				src, md := snapshotStoreForTest(t, ctx, "Source", "source fixture")
				repo := t.TempDir()
				if _, err := Publish(ctx, src, PublishOptions{RepoPath: repo, MarkdownDir: md}); err != nil {
					t.Fatal(err)
				}
				rewriteSnapshotNumber(t, repo, tc.table, tc.field, tc.value, tc.suffix)
				dst, _ := snapshotStoreForTest(t, ctx, "Destination", "private-fixture-marker")
				_, err := ImportWithOptions(ctx, dst, repo, ImportOptions{Restore: restore, RetainRevisions: true})
				if err == nil {
					t.Fatal("expected import rejection")
				}
				if strings.Contains(err.Error(), "private-fixture-marker") || strings.Contains(err.Error(), tc.value) {
					t.Fatalf("numeric error includes fixture content: %v", err)
				}
				var text string
				if err := dst.DB().QueryRowContext(ctx, `select text from blocks where id = 'block1'`).Scan(&text); err != nil {
					t.Fatal(err)
				}
				if text != "private-fixture-marker" {
					t.Fatalf("failed import changed destination: %q", text)
				}
				var revisions int
				if err := dst.DB().QueryRowContext(ctx, `select count(*) from record_revisions`).Scan(&revisions); err != nil {
					t.Fatal(err)
				}
				if revisions != 0 {
					t.Fatalf("failed import retained %d revisions", revisions)
				}
			})
		}
	}
}

func TestSnapshotDecimalImportCompatibility(t *testing.T) {
	for _, tc := range []struct {
		value       string
		merged      int64
		restored    float64
		restoreType string
	}{
		{"12.75", 12, 12.75, "real"},
		{"-12.75", -12, -12.75, "real"},
		{"4.2e1", 42, 42, "integer"},
		{"-9223372036854775808.0", math.MinInt64, -0x1p63, "real"},
		{"9223372036854774784.0", 9223372036854774784, 9223372036854774784, "integer"},
	} {
		for _, restore := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/restore=%t", tc.value, restore), func(t *testing.T) {
				ctx := context.Background()
				src, md := snapshotStoreForTest(t, ctx, "Source", "fixture")
				if _, err := src.DB().ExecContext(ctx, `insert into sync_state
					(source, entity_type, entity_id, synced_at) values ('test', 'block', 'block1', 42)`); err != nil {
					t.Fatal(err)
				}
				repo := t.TempDir()
				if _, err := Publish(ctx, src, PublishOptions{RepoPath: repo, MarkdownDir: md}); err != nil {
					t.Fatal(err)
				}
				query := `select display_order, typeof(display_order) from blocks where id = 'block1'`
				if restore {
					rewriteSnapshotNumber(t, repo, "sync_state", "synced_at", tc.value, "")
					query = `select synced_at, typeof(synced_at) from sync_state where entity_id = 'block1'`
				} else {
					rewriteSnapshotNumber(t, repo, "blocks", "display_order", tc.value, "")
				}
				dst, err := store.Open(filepath.Join(t.TempDir(), "destination.db"))
				if err != nil {
					t.Fatal(err)
				}
				defer dst.Close()
				if _, err := ImportWithOptions(ctx, dst, repo, ImportOptions{Restore: restore}); err != nil {
					t.Fatal(err)
				}
				var got any
				var storageType string
				if err := dst.DB().QueryRowContext(ctx, query).Scan(&got, &storageType); err != nil {
					t.Fatal(err)
				}
				if restore {
					want := any(tc.restored)
					if tc.restoreType == "integer" {
						want = tc.merged
					}
					if got != want || storageType != tc.restoreType {
						t.Fatalf("restore got %v (%s), want %v (%s)", got, storageType, want, tc.restoreType)
					}
				} else if got != tc.merged || storageType != "integer" {
					t.Fatalf("merge got %v (%s), want %d (integer)", got, storageType, tc.merged)
				}
			})
		}
	}
}

func TestSnapshotIntegerRevisionPayload(t *testing.T) {
	ctx := context.Background()
	src, md := snapshotStoreForTest(t, ctx, "Source", "incoming")
	repo := t.TempDir()
	if _, err := Publish(ctx, src, PublishOptions{RepoPath: repo, MarkdownDir: md}); err != nil {
		t.Fatal(err)
	}
	for _, restore := range []bool{false, true} {
		t.Run(fmt.Sprintf("restore=%t", restore), func(t *testing.T) {
			dst, _ := snapshotStoreForTest(t, ctx, "Destination", "previous")
			const previous int64 = 1<<53 + 1
			if _, err := dst.DB().ExecContext(ctx, `update blocks set display_order = ? where id = 'block1'`, previous); err != nil {
				t.Fatal(err)
			}
			if _, err := ImportWithOptions(ctx, dst, repo, ImportOptions{Restore: restore, RetainRevisions: true}); err != nil {
				t.Fatal(err)
			}
			var payload string
			if err := dst.DB().QueryRowContext(ctx, `select payload_json from record_revisions where record_table = 'blocks'`).Scan(&payload); err != nil {
				t.Fatal(err)
			}
			var row struct {
				DisplayOrder int64 `json:"display_order"`
			}
			if err := json.Unmarshal([]byte(payload), &row); err != nil {
				t.Fatal(err)
			}
			if row.DisplayOrder != previous {
				t.Fatalf("revision integer=%d, want %d", row.DisplayOrder, previous)
			}
		})
	}
}

func rewriteSnapshotNumber(t *testing.T, repo, table, field, value, suffix string) {
	t.Helper()
	path := filepath.Join(repo, "data", table+".jsonl.gz")
	in, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	gz, err := gzip.NewReader(in)
	if err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(gz)
	if err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	if err := in.Close(); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	var row map[string]json.RawMessage
	if err := json.Unmarshal([]byte(lines[0]), &row); err != nil {
		t.Fatal(err)
	}
	row[field] = json.RawMessage(value)
	encoded, err := json.Marshal(row)
	if err != nil {
		t.Fatal(err)
	}
	lines[0] = string(encoded) + suffix
	out, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	writer := gzip.NewWriter(out)
	if _, err := io.WriteString(writer, strings.Join(lines, "\n")+"\n"); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := out.Close(); err != nil {
		t.Fatal(err)
	}
}
