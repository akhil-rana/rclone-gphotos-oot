package test

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/rclone/rclone/fs/config/configmap"
	"github.com/user/rclone-gphotos-oot/backend/gphotosmobile"
	"github.com/user/rclone-gphotos-oot/backend/gphotosmobile/api"
)

func TestParseCaptionVariants(t *testing.T) {
	tests := []struct {
		name        string
		caption     string
		wantPaths   []string
		wantBinName string
		wantBinSize int64
		wantBinSHA1 string
	}{
		{
			name:      "Unquoted paths key",
			caption:   `{paths:["/Movies"]}`,
			wantPaths: []string{"/Movies"},
		},
		{
			name:      "Quoted paths key",
			caption:   `{"paths":["/Movies", "/Favorites"]}`,
			wantPaths: []string{"/Movies", "/Favorites"},
		},
		{
			name:      "Legacy bracket paths",
			caption:   `[paths:"/Movies", "/Trip"]`,
			wantPaths: []string{"/Movies", "/Trip"},
		},
		{
			name:      "Legacy bracket single path",
			caption:   `[path:"/Movies"]`,
			wantPaths: []string{"/Movies"},
		},
		{
			name:        "Binary details with paths",
			caption:     `{"paths":["/filesTest"],"binaryDetails":{"name":"sample.zip","size":101579628,"sha1":"32e74eb694447f63e839d7fc7e011aa01438ddd4"}}`,
			wantPaths:   []string{"/filesTest"},
			wantBinName: "sample.zip",
			wantBinSize: 101579628,
			wantBinSHA1: "32e74eb694447f63e839d7fc7e011aa01438ddd4",
		},
		{
			name:        "Binary details without paths",
			caption:     `{"binaryDetails":{"name":"sample.zip","size":101579628,"sha1":"32e74eb694447f63e839d7fc7e011aa01438ddd4"}}`,
			wantPaths:   nil,
			wantBinName: "sample.zip",
			wantBinSize: 101579628,
			wantBinSHA1: "32e74eb694447f63e839d7fc7e011aa01438ddd4",
		},
		{
			name:        "Binary details with chunk details",
			caption:     `{"paths":["/Backups"],"binaryDetails":{"name":"data.iso","size":1000000000,"sha1":"32e74eb694447f63e839d7fc7e011aa01438ddd4","chunkDetails":{"chunkIndex":2,"totalChunks":15,"chunkSize":67108864,"offset":134217728}}}`,
			wantPaths:   []string{"/Backups"},
			wantBinName: "data.iso",
			wantBinSize: 1000000000,
			wantBinSHA1: "32e74eb694447f63e839d7fc7e011aa01438ddd4",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotPaths, gotBin := gphotosmobile.ParseCaption(tt.caption)
			if !reflect.DeepEqual(gotPaths, tt.wantPaths) {
				t.Errorf("ParseCaption(%q) gotPaths = %v, want %v", tt.caption, gotPaths, tt.wantPaths)
			}
			if tt.wantBinName != "" {
				if gotBin == nil {
					t.Fatalf("ParseCaption(%q) expected binaryDetails, got nil", tt.caption)
				}
				if gotBin.Name != tt.wantBinName || gotBin.Size != tt.wantBinSize || gotBin.SHA1 != tt.wantBinSHA1 {
					t.Errorf("ParseCaption(%q) gotBin = %+v, want name=%s size=%d sha1=%s", tt.caption, gotBin, tt.wantBinName, tt.wantBinSize, tt.wantBinSHA1)
				}
				if tt.name == "Binary details with chunk details" {
					if gotBin.ChunkDetails == nil {
						t.Fatal("expected ChunkDetails, got nil")
					}
					if gotBin.ChunkDetails.ChunkIndex != 2 || gotBin.ChunkDetails.TotalChunks != 15 || gotBin.ChunkDetails.ChunkSize != 67108864 {
						t.Errorf("unexpected ChunkDetails values: %+v", gotBin.ChunkDetails)
					}
				}
			}
		})
	}
}

func TestConfigValidation(t *testing.T) {
	// Test 1: binary_data_chunking=true without support_binary_data=true
	m := configmap.Simple{
		"auth_data":            "fake_auth",
		"binary_data_chunking": "true",
		"support_binary_data":  "false",
	}
	_, err := gphotosmobile.NewFs(context.Background(), "test", "", m)
	if err == nil || !strings.Contains(err.Error(), "cannot be enabled without support_binary_data") {
		t.Fatalf("expected error for chunking without support_binary_data, got: %v", err)
	}

	// Test 2: binary_data_chunk_size too small (< 32M)
	m = configmap.Simple{
		"auth_data":              "fake_auth",
		"support_binary_data":    "true",
		"binary_data_chunking":   "true",
		"binary_data_chunk_size": "10M",
	}
	_, err = gphotosmobile.NewFs(context.Background(), "test", "", m)
	if err == nil || !strings.Contains(err.Error(), "must be between 32M and 512M") {
		t.Fatalf("expected error for chunk size < 32M, got: %v", err)
	}

	// Test 3: binary_data_chunk_size too large (> 512M)
	m = configmap.Simple{
		"auth_data":              "fake_auth",
		"support_binary_data":    "true",
		"binary_data_chunking":   "true",
		"binary_data_chunk_size": "1G",
	}
	_, err = gphotosmobile.NewFs(context.Background(), "test", "", m)
	if err == nil || !strings.Contains(err.Error(), "must be between 32M and 512M") {
		t.Fatalf("expected error for chunk size > 512M, got: %v", err)
	}

	// Test 4: binary_data_chunk_concurrency too low (< 1)
	m = configmap.Simple{
		"auth_data":                     "fake_auth",
		"support_binary_data":           "true",
		"binary_data_chunking":          "true",
		"binary_data_chunk_size":        "64M",
		"binary_data_chunk_concurrency": "0",
	}
	_, err = gphotosmobile.NewFs(context.Background(), "test", "", m)
	if err == nil || !strings.Contains(err.Error(), "must be between 1 and 16") {
		t.Fatalf("expected error for concurrency < 1, got: %v", err)
	}

	// Test 5: binary_data_chunk_concurrency too high (> 16)
	m = configmap.Simple{
		"auth_data":                     "fake_auth",
		"support_binary_data":           "true",
		"binary_data_chunking":          "true",
		"binary_data_chunk_size":        "64M",
		"binary_data_chunk_concurrency": "20",
	}
	_, err = gphotosmobile.NewFs(context.Background(), "test", "", m)
	if err == nil || !strings.Contains(err.Error(), "must be between 1 and 16") {
		t.Fatalf("expected error for concurrency > 16, got: %v", err)
	}

	// Test 6: min_size_needed_for_binary_video too low (< 5M)
	m = configmap.Simple{
		"auth_data":                        "fake_auth",
		"support_binary_data":              "true",
		"min_size_needed_for_binary_video": "2M",
	}
	_, err = gphotosmobile.NewFs(context.Background(), "test", "", m)
	if err == nil || !strings.Contains(err.Error(), "must be between 5M and 50M") {
		t.Fatalf("expected error for min_size < 5M, got: %v", err)
	}

	// Test 7: min_size_needed_for_binary_video too high (> 50M)
	m = configmap.Simple{
		"auth_data":                        "fake_auth",
		"support_binary_data":              "true",
		"min_size_needed_for_binary_video": "100M",
	}
	_, err = gphotosmobile.NewFs(context.Background(), "test", "", m)
	if err == nil || !strings.Contains(err.Error(), "must be between 5M and 50M") {
		t.Fatalf("expected error for min_size > 50M, got: %v", err)
	}

	// Test 8: binary_video_resolution unrecognized name
	m = configmap.Simple{
		"auth_data":               "fake_auth",
		"support_binary_data":     "true",
		"binary_video_resolution": "super10k",
	}
	_, err = gphotosmobile.NewFs(context.Background(), "test", "", m)
	if err == nil || !strings.Contains(err.Error(), "is not recognized") {
		t.Fatalf("expected error for invalid resolution name, got: %v", err)
	}

	// Test 9: binary_video_resolution=4K with binary_data_chunk_size=64M (4K requires ~570M for 1s, rejects!)
	m = configmap.Simple{
		"auth_data":                     "fake_auth",
		"support_binary_data":           "true",
		"binary_data_chunking":          "true",
		"binary_data_chunk_size":        "64M",
		"binary_data_chunk_concurrency": "4",
		"binary_video_resolution":       "4K",
	}
	_, err = gphotosmobile.NewFs(context.Background(), "test", "", m)
	if err == nil || !strings.Contains(err.Error(), "requires at least") {
		t.Fatalf("expected error for 4K with 64M chunk size, got: %v", err)
	}

	// Test 10: upload_timestamp pre-1906 date is rejected
	m = configmap.Simple{
		"auth_data":        "fake_auth",
		"upload_timestamp": "1905-12-31",
	}
	_, err = gphotosmobile.NewFs(context.Background(), "test", "", m)
	if err == nil || !strings.Contains(err.Error(), "cannot be earlier than 1906-01-01") {
		t.Fatalf("expected error for pre-1906 date, got: %v", err)
	}

	// Test 11: upload_timestamp integer earlier than 1906 is rejected
	m = configmap.Simple{
		"auth_data":        "fake_auth",
		"upload_timestamp": "-2019686401",
	}
	_, err = gphotosmobile.NewFs(context.Background(), "test", "", m)
	if err == nil || !strings.Contains(err.Error(), "cannot be earlier than 1906-01-01") {
		t.Fatalf("expected error for timestamp before 1906, got: %v", err)
	}

	// Test 12: upload_timestamp invalid string is rejected
	m = configmap.Simple{
		"auth_data":        "fake_auth",
		"upload_timestamp": "not-a-valid-date",
	}
	_, err = gphotosmobile.NewFs(context.Background(), "test", "", m)
	if err == nil || !strings.Contains(err.Error(), "invalid upload_timestamp") {
		t.Fatalf("expected error for invalid date string, got: %v", err)
	}

	// Test 13: upload_timestamp='oldest' is valid (1906-01-01 00:00:00 UTC)
	m = configmap.Simple{
		"auth_data":        "fake_auth",
		"upload_timestamp": "oldest",
	}
	_, err = gphotosmobile.NewFs(context.Background(), "test", "", m)
	if err != nil {
		t.Fatalf("expected 'oldest' to succeed, got: %v", err)
	}

	// Test 14: upload_timestamp='1906-01-01' is valid
	m = configmap.Simple{
		"auth_data":        "fake_auth",
		"upload_timestamp": "1906-01-01",
	}
	_, err = gphotosmobile.NewFs(context.Background(), "test", "", m)
	if err != nil {
		t.Fatalf("expected '1906-01-01' to succeed, got: %v", err)
	}

	// Test 15: upload_timestamp='epoch' is valid (1970-01-01 00:00:00 UTC)
	m = configmap.Simple{
		"auth_data":        "fake_auth",
		"upload_timestamp": "epoch",
	}
	_, err = gphotosmobile.NewFs(context.Background(), "test", "", m)
	if err != nil {
		t.Fatalf("expected 'epoch' to succeed, got: %v", err)
	}
}

func TestAlbumFilter(t *testing.T) {
	tempDir := t.TempDir()
	dbPath := filepath.Join(tempDir, "test.db")
	c, err := gphotosmobile.NewCache(dbPath)
	if err != nil {
		t.Fatalf("failed to create cache: %v", err)
	}
	defer c.Close()

	items := []api.MediaItem{
		{MediaKey: "m1", FileName: "albumA_photo.jpg", CollectionID: "AF1QipAlbumA"},
		{MediaKey: "m2", FileName: "albumB_photo.jpg", CollectionID: "AF1QipAlbumB"},
		{MediaKey: "m3", FileName: "root_photo.jpg", CollectionID: ""},
	}
	if err := c.UpsertItems(items); err != nil {
		t.Fatalf("failed to upsert items: %v", err)
	}

	// Unfiltered
	all, err := c.ListAll()
	if err != nil || len(all) != 3 {
		t.Fatalf("expected 3 items unfiltered, got %d", len(all))
	}

	// Filtered by AlbumA
	c.SetAlbumFilter([]string{"AF1QipAlbumA"})
	albumAItems, err := c.ListAll()
	if err != nil || len(albumAItems) != 1 || albumAItems[0].MediaKey != "m1" {
		t.Fatalf("expected only m1 for AlbumA, got: %+v", albumAItems)
	}

	itemA, err := c.GetByFileName("albumA_photo.jpg")
	if err != nil || itemA.MediaKey != "m1" {
		t.Fatalf("expected m1 for albumA_photo.jpg, got: %+v, err: %v", itemA, err)
	}

	_, err = c.GetByFileName("albumB_photo.jpg")
	if !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("expected ErrNoRows for albumB_photo.jpg when filtered to AlbumA, got: %v", err)
	}

	// Filtered by both AlbumA and AlbumB
	c.SetAlbumFilter([]string{"AF1QipAlbumA", "AF1QipAlbumB"})
	bothAlbums, err := c.ListAll()
	if err != nil || len(bothAlbums) != 2 {
		t.Fatalf("expected 2 items for AlbumA+AlbumB, got: %d", len(bothAlbums))
	}

	// Reset filter
	c.SetAlbumFilter(nil)
	allAgain, err := c.ListAll()
	if err != nil || len(allAgain) != 3 {
		t.Fatalf("expected 3 items after clearing filter, got %d", len(allAgain))
	}
}

func TestAlbumSpillover(t *testing.T) {
	tempDir := t.TempDir()
	dbPath := filepath.Join(tempDir, "spillover.db")
	c, err := gphotosmobile.NewCache(dbPath)
	if err != nil {
		t.Fatalf("failed to create cache: %v", err)
	}
	defer c.Close()

	// Album1 has 2 items, Album2 has 0 items
	items := []api.MediaItem{
		{MediaKey: "m1", FileName: "photo1.jpg", CollectionID: "Album1"},
		{MediaKey: "m2", FileName: "photo2.jpg", CollectionID: "Album1"},
	}
	if err := c.UpsertItems(items); err != nil {
		t.Fatalf("failed to upsert items: %v", err)
	}

	albumList := []string{"Album1", "Album2"}

	// Max items per album: 3
	// Total capacity: Album1 has 1 slot left, Album2 has 3 slots left -> Total free = 4
	// Needed: 3 items -> Total free (4) >= 3 -> CheckTotalAlbumCapacity succeeds!
	if err := c.CheckTotalAlbumCapacity(albumList, 3, 3); err != nil {
		t.Fatalf("expected capacity check to succeed, got: %v", err)
	}

	// Active album should pick Album1 first (it has 2 items, limit 3)
	target, err := c.GetActiveUploadAlbum(albumList, 3)
	if err != nil || target != "Album1" {
		t.Fatalf("expected Album1, got %q (err: %v)", target, err)
	}

	// Now fill Album1 completely (add 1 item to Album1)
	if err := c.UpsertItems([]api.MediaItem{{MediaKey: "m1_3", FileName: "photo1_3.jpg", CollectionID: "Album1"}}); err != nil {
		t.Fatalf("failed to upsert: %v", err)
	}

	// Active album should now spill over to Album2!
	target, err = c.GetActiveUploadAlbum(albumList, 3)
	if err != nil || target != "Album2" {
		t.Fatalf("expected spillover to Album2, got %q (err: %v)", target, err)
	}

	// Now fill Album2 completely (add 3 items to Album2)
	itemsAlbum2 := []api.MediaItem{
		{MediaKey: "m3", FileName: "photo3.jpg", CollectionID: "Album2"},
		{MediaKey: "m4", FileName: "photo4.jpg", CollectionID: "Album2"},
		{MediaKey: "m5", FileName: "photo5.jpg", CollectionID: "Album2"},
	}
	if err := c.UpsertItems(itemsAlbum2); err != nil {
		t.Fatalf("failed to upsert items: %v", err)
	}

	// Now both albums are completely full!
	// CheckTotalAlbumCapacity should fail with rich error showing both albums full:
	err = c.CheckTotalAlbumCapacity(albumList, 3, 1)
	if err == nil || !strings.Contains(err.Error(), "insufficient album capacity") {
		t.Fatalf("expected insufficient capacity error, got: %v", err)
	}

	// GetActiveUploadAlbum should fail with detailed error:
	_, err = c.GetActiveUploadAlbum(albumList, 3)
	if err == nil || !strings.Contains(err.Error(), "all 2 configured albums are full") {
		t.Fatalf("expected all albums full error, got: %v", err)
	}
}
