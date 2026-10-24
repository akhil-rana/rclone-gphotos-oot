package gphotosmobile

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/rclone/rclone/fs"
	"github.com/rclone/rclone/fs/accounting"
	"github.com/rclone/rclone/fs/config"
	"github.com/rclone/rclone/fs/config/configmap"
	"github.com/rclone/rclone/fs/config/configstruct"
	"github.com/rclone/rclone/fs/hash"
	"github.com/rclone/rclone/lib/atexit"
	"github.com/rclone/rclone/lib/encoder"
	"github.com/user/rclone-gphotos-oot/backend/gphotosmobile/api"
	"github.com/user/rclone-gphotos-oot/backend/gphotosmobile/binary"
)

var (
	errCantRmdir = errors.New("can't remove virtual directory")
)

type captionPayload struct {
	Paths         []string              `json:"paths"`
	BinaryDetails *binary.BinaryDetails `json:"binaryDetails,omitempty"`
}

// ParseCaption parses virtual paths and binaryDetails from a Google Photos caption.
func ParseCaption(caption string) ([]string, *binary.BinaryDetails) {
	return parseCaption(caption)
}

func parseCaption(caption string) ([]string, *binary.BinaryDetails) {
	if caption == "" {
		return nil, nil
	}

	var p captionPayload
	if err := json.Unmarshal([]byte(caption), &p); err == nil {
		return p.Paths, p.BinaryDetails
	}

	// Normalize unquoted JSON keys: {paths: -> {"paths":, binaryDetails: -> "binaryDetails":
	normalized := caption
	normalized = strings.ReplaceAll(normalized, "{paths:", "{\"paths\":")
	normalized = strings.ReplaceAll(normalized, ",paths:", ",\"paths\":")
	normalized = strings.ReplaceAll(normalized, ",binaryDetails:", ",\"binaryDetails\":")
	normalized = strings.ReplaceAll(normalized, "{binaryDetails:", "{\"binaryDetails\":")

	start := strings.Index(normalized, "{")
	end := strings.LastIndex(normalized, "}")
	if start != -1 && end != -1 && end > start {
		if err := json.Unmarshal([]byte(normalized[start:end+1]), &p); err == nil {
			return p.Paths, p.BinaryDetails
		}
	}

	// Fallback extraction of paths from any format: {paths:[...]}, [paths:...], [path:...]
	var paths []string
	extractPaths := func(prefix string, closeChar string) bool {
		idx := strings.Index(caption, prefix)
		if idx != -1 {
			endIdx := strings.Index(caption[idx:], closeChar)
			if endIdx != -1 {
				raw := caption[idx+len(prefix) : idx+endIdx]
				for _, part := range strings.Split(raw, ",") {
					part = strings.Trim(strings.TrimSpace(part), "\"'")
					if part != "" {
						paths = append(paths, part)
					}
				}
				return true
			}
		}
		return false
	}

	if extractPaths("{paths:[", "]") || extractPaths("[paths:", "]") || extractPaths("[path:", "]") || extractPaths("paths:[", "]") {
		return paths, nil
	}

	return nil, nil
}

func buildCaption(paths []string, binDetails *binary.BinaryDetails) string {
	payload := captionPayload{
		Paths:         paths,
		BinaryDetails: binDetails,
	}
	b, err := json.Marshal(payload)
	if err != nil {
		return ""
	}
	return string(b)
}

var systemMetadataInfo = map[string]fs.MetadataHelp{
	"description": {
		Help:    "Caption/description of the media item in Google Photos.",
		Type:    "string",
		Example: "Sunset at the beach",
	},
	"media-type": {
		Help:     "Media type: photo or video.",
		Type:     "string",
		Example:  "photo",
		ReadOnly: true,
	},
	"width": {
		Help:     "Width in pixels.",
		Type:     "int",
		Example:  "4032",
		ReadOnly: true,
	},
	"height": {
		Help:     "Height in pixels.",
		Type:     "int",
		Example:  "3024",
		ReadOnly: true,
	},
	"duration": {
		Help:     "Duration in milliseconds (videos only).",
		Type:     "int",
		Example:  "15000",
		ReadOnly: true,
	},
	"latitude": {
		Help:     "GPS latitude of the media item.",
		Type:     "float",
		Example:  "37.7749",
		ReadOnly: true,
	},
	"longitude": {
		Help:     "GPS longitude of the media item.",
		Type:     "float",
		Example:  "-122.4194",
		ReadOnly: true,
	},
	"location-name": {
		Help:     "Reverse-geocoded location name.",
		Type:     "string",
		Example:  "San Francisco, CA",
		ReadOnly: true,
	},
	"camera-make": {
		Help:     "Camera manufacturer from EXIF.",
		Type:     "string",
		Example:  "Google",
		ReadOnly: true,
	},
	"camera-model": {
		Help:     "Camera model from EXIF.",
		Type:     "string",
		Example:  "Pixel 9a",
		ReadOnly: true,
	},
	"aperture": {
		Help:     "Aperture f-number from EXIF.",
		Type:     "float",
		Example:  "1.8",
		ReadOnly: true,
	},
	"shutter-speed": {
		Help:     "Shutter speed in seconds from EXIF.",
		Type:     "float",
		Example:  "0.001",
		ReadOnly: true,
	},
	"iso": {
		Help:     "ISO sensitivity from EXIF.",
		Type:     "int",
		Example:  "100",
		ReadOnly: true,
	},
	"focal-length": {
		Help:     "Focal length in mm from EXIF.",
		Type:     "float",
		Example:  "4.38",
		ReadOnly: true,
	},
	"taken-at": {
		Help:     "Time the photo/video was taken (UTC).",
		Type:     "RFC 3339",
		Example:  "2006-01-02T15:04:05Z",
		ReadOnly: true,
	},
	"uploaded-at": {
		Help:     "Time the item was uploaded to Google Photos (UTC).",
		Type:     "RFC 3339",
		Example:  "2006-01-02T15:04:05Z",
		ReadOnly: true,
	},
	"is-favorite": {
		Help:     "Whether the item is marked as favorite.",
		Type:     "bool",
		Example:  "true",
		ReadOnly: true,
	},
	"is-archived": {
		Help:     "Whether the item is archived.",
		Type:     "bool",
		Example:  "false",
		ReadOnly: true,
	},
	"origin": {
		Help:     "Origin of the item: self, partner, or shared.",
		Type:     "string",
		Example:  "self",
		ReadOnly: true,
	},
}

func init() {
	fs.Register(&fs.RegInfo{
		Name:        "gphotosmobile",
		Description: "Google Photos (Mobile API - full access)",
		NewFs:       NewFs,
		MetadataInfo: &fs.MetadataInfo{
			System: systemMetadataInfo,
			Help:   `Metadata is returned for all media items. The "description" (caption) can be written; all other fields are read-only.`,
		},
		CommandHelp: []fs.CommandHelp{{
			Name:  "set-description",
			Short: "Set the description (caption) of a media item.",
			Long: `Set the description (caption) of a media item in Google Photos.
The description is the text shown under the item in the Google Photos app.

Usage:

    rclone backend set-description remote:media/photo.jpg -o description="Sunset at the beach"

To clear the description:

    rclone backend set-description remote:media/photo.jpg -o description=""
`,
			Opts: map[string]string{
				"description": "The description text to set (empty to clear)",
			},
		}},
		Config: func(ctx context.Context, name string, m configmap.Mapper, configIn fs.ConfigIn) (*fs.ConfigOut, error) {

			// Helper to exchange and finish
			exchangeAndSave := func(token string) (*fs.ConfigOut, error) {
				if !strings.HasPrefix(token, "oauth2_4/") {
					return nil, fmt.Errorf("Invalid cookie format. The 'oauth_token' should start with 'oauth2_4/'.")
				}
				fs.Infof(nil, "Exchanging oauth_token for Android credentials...")
				newAuthData, err := exchangeOAuthToken(ctx, token)
				if err != nil {
					return nil, fmt.Errorf("Failed to exchange oauth_token: %v", err)
				}
				m.Set("auth_data", newAuthData)

				// Verification check
				deviceModel, _ := m.Get("device_model")
				deviceMake, _ := m.Get("device_make")
				unlimited, _ := m.Get("unlimited_upload")
				if unlimited == "true" {
					deviceModel = "Pixel XL"
					deviceMake = "Google"
				}
				api := NewMobileAPI(ctx, newAuthData, deviceModel, deviceMake, 0, 0)
				_, err = api.bearerToken(ctx)
				if err != nil {
					return nil, fmt.Errorf("Authentication verification failed: %v", err)
				}

				email := parseEmail(newAuthData)
				fs.Infof(nil, "Successfully authenticated as %s", email)
				return nil, nil
			}

			switch configIn.State {
			case "":
				authData, _ := m.Get("auth_data")
				if authData == "" {
					return nil, nil
				}

				if strings.Contains(authData, "androidId=") {
					email := parseEmail(authData)
					fs.Infof(nil, "Successfully authenticated as %s", email)
					return nil, nil
				}

				if authData == "auto" {
					fs.Infof(nil, "Starting automated browser login...")
					token, err := automatedBrowserLogin(ctx)
					if err != nil {
						return nil, fmt.Errorf("Auto-login failed: %v", err)
					}
					fs.Infof(nil, "Successfully grabbed oauth_token!")
					return exchangeAndSave(token)

				} else if authData == "manual" {
					return &fs.ConfigOut{
						State: "do_manual",
						Option: &fs.Option{
							Name:     "oauth_token_prompt",
							Help:     "=== MANUAL LOGIN ===\n1. Open this link: https://accounts.google.com/EmbeddedSetup\n2. Sign in to your Google Account and click 'I agree'.\n3. The page will eventually show a loading spinner forever. This is normal!\n4. Press F12 to open Developer Tools.\n5. Go to the 'Application' (Chrome) or 'Storage' (Firefox) tab.\n6. Expand 'Cookies' -> 'https://accounts.google.com'.\n7. Find 'oauth_token', copy its Value, and paste it below.",
							Required: true,
						},
					}, nil
				}

				// If they somehow typed a token directly into the first prompt
				return exchangeAndSave(authData)

			case "do_manual":
				return exchangeAndSave(configIn.Result)
			}
			return nil, fmt.Errorf("unknown config state %q", configIn.State)
		},
		Options: []fs.Option{{
			Name: "auth_data",
			Help: "Google Photos mobile API auth data.",
			Examples: []fs.OptionExample{{
				Value: "auto",
				Help:  "Automated Browser Login (Linux Desktop only - requires Chrome/Brave/Chromium)",
			}, {
				Value: "manual",
				Help:  "Manual Login (Paste oauth_token from Developer Tools)",
			}},
			Required:  true,
			Sensitive: true,
		}, {
			Name:     "cache_db_path",
			Help:     "Path to SQLite cache database.\n\nThe media library index is cached locally in SQLite for fast listing.\nThe default location is inside rclone's cache directory\n(see --cache-dir) at gphotosmobile/<remote-name>.db.\n\nThe initial sync downloads the full library index and may take a few\nminutes for large libraries. Subsequent runs use fast incremental sync.\nIf this file is deleted, rclone will re-sync the full library on next run.",
			Default:  "",
			Advanced: true,
		}, {
			Name:     "device_model",
			Help:     "Device model to report to Google Photos.\n\nThis is included in the user agent and upload metadata.\nLeave empty for default (Pixel 9a).",
			Default:  "",
			Advanced: true,
		}, {
			Name:     "device_make",
			Help:     "Device manufacturer to report to Google Photos.\n\nThis is included in upload metadata.\nLeave empty for default (Google).",
			Default:  "",
			Advanced: true,
		}, {
			Name:    "unlimited_upload",
			Help:    "Spoof Google Pixel 1 XL for unlimited original quality uploads.",
			Default: false,
		}, {
			Name:    "list_mode",
			Help:    "Format to list files. Choose how files and directories are organized.",
			Default: "flat",
			Examples: []fs.OptionExample{{
				Value: "flat",
				Help:  "All photos and videos in the root directory (default)",
			}, {
				Value: "calendar",
				Help:  "Nested date hierarchy (YYYY/YYYY-MM/YYYY-MM-DD/filename)",
			}, {
				Value: "month",
				Help:  "Grouped by year and month (YYYY/YYYY-MM/filename)",
			}, {
				Value: "year",
				Help:  "Grouped strictly by year (YYYY/filename)",
			}, {
				Value: "description_path",
				Help:  "Virtual directories backed by caption tags {paths:[\"/dir\"]}",
			}},
		}, {
			Name:     "apk_version",
			Help:     "Google Photos APK version code to impersonate.\n\nThis is sent in the user agent and protobuf requests.\nUpdate if Google blocks older versions.",
			Default:  int64(49029607),
			Advanced: true,
		}, {
			Name:     "android_api",
			Help:     "Android API level to report.\n\n35 corresponds to Android 15, which ships with the default\ndevice model (Pixel 9a). Included in upload metadata.",
			Default:  int64(35),
			Advanced: true,
		}, {
			Name:     "download_cache",
			Help:     "Enable download cache for opened files.\n\nGoogle Photos download URLs do not support HTTP Range requests.\nWhen enabled, each opened file is downloaded once to a local temp\nfile and shared across concurrent readers, with instant seeking.\nThis is useful for rclone mount where the VFS layer needs to seek.\n\nWhen disabled (the default), Open() returns the raw HTTP stream\ndirectly with no temp files. Use --vfs-cache-mode full for mount.",
			Default:  false,
			Advanced: true,
		}, {
			Name:     "support_binary_data",
			Help:     "[BETA] Enable storing arbitrary binary data (archives, documents, executables) by encoding them into lossless uncompressed RGB24 media (PNG/MKV).\n\nWhen disabled (the default), only native photo and video files are supported.",
			Default:  false,
			Advanced: true,
		}, {
			Name:     "encode_all_as_binary",
			Help:     "When support_binary_data is enabled, force binary RGB24 encoding for all files, including native photos and videos (.jpg, .mp4, etc.).\n\nWhen disabled (the default), native media files are uploaded directly without binary encoding.",
			Default:  false,
			Advanced: true,
		}, {
			Name:     "binary_data_chunking",
			Help:     "Enable splitting large binary files into smaller media chunks for fast random seeking (virtual HTTP Range support).\n\nRequires support_binary_data to be enabled.",
			Default:  false,
			Advanced: true,
		}, {
			Name:     "binary_data_chunk_size",
			Help:     "Chunk size for binary data chunking. Must be between 32M and 512M.\n\nRecommended: 64M. Smaller chunks improve seeking speed at the cost of more individual uploads.",
			Default:  fs.SizeSuffix(64 * 1024 * 1024),
			Advanced: true,
		}, {
			Name:     "binary_data_chunk_concurrency",
			Help:     "Concurrency for uploading binary data chunks in parallel. Must be between 1 and 16.\n\nDefault: 4. Higher values saturate fast network connections during chunked uploads.",
			Default:  4,
			Advanced: true,
		}, {
			Name:     "binary_video_resolution",
			Help:     "Target video resolution for binary MKV containers (from 240p to 4096p).\n\nOptions:\n  auto    - Automatically choose the best resolution that guarantees at least 1s duration and minimizes padding (default)\n  240p    - 426x240 (min ~7MB for 1s)\n  360p    - 640x360 (min ~16MB for 1s)\n  480p    - 854x480 (min ~29MB for 1s)\n  720p    - 1280x720 (min ~64MB for 1s)\n  1080p   - 1920x1080 (min ~143MB for 1s)\n  1440p   - 2560x1440 (min ~254MB for 1s)\n  4K      - 3840x2160 (min ~570MB for 1s)\n  4096p   - 4096x2160 (min ~608MB for 1s)\n\nNote: Videos must be at least 1 second long. If chunking is enabled, the resolution must fit within binary_data_chunk_size.",
			Default:  "auto",
			Advanced: true,
		}, {
			Name:     "min_size_needed_for_binary_video",
			Help:     "Minimum file size to encode as an MKV video instead of a PNG image. Must be between 5M and 50M.\n\nFiles smaller than this threshold are stored as lossless uncompressed PNG images; larger files are stored as MKV videos.\n\nDefault: 10M.",
			Default:  fs.SizeSuffix(10 * 1024 * 1024),
			Advanced: true,
		}, {
			Name:     "album_ids",
			Help:     "Comma-separated list of Google Photos Album IDs to scope this remote to (e.g. AF1Qip1,AF1Qip2).\n\nLeave empty to access the entire library.\n\nWhen configured, 'rclone ls' lists items from all listed albums. Uploads fill each album sequentially up to max_items_per_album before spilling over to the next album in the list.\n\nTo find an Album ID, open the album at photos.google.com and copy the ID from the URL: photos.google.com/album/<ALBUM_ID>",
			Default:  "",
			Advanced: false,
		}, {
			Name:     "max_items_per_album",
			Help:     "Maximum number of items allowed in an album before spilling over to the next album in album_ids. Google Photos hard limit is 20,000.\n\nDefault: 20000.",
			Default:  20000,
			Advanced: true,
		}, {
			Name:     config.ConfigEncoding,
			Help:     config.ConfigEncodingHelp,
			Advanced: true,
			Default: (encoder.Base |
				encoder.EncodeCrLf |
				encoder.EncodeInvalidUtf8),
		}},
	})
}

// Options defines the configuration for this backend
type Options struct {
	AuthData                    string               `config:"auth_data"`
	CacheDBPath                 string               `config:"cache_db_path"`
	DeviceModel                 string               `config:"device_model"`
	DeviceMake                  string               `config:"device_make"`
	UnlimitedUpload             bool                 `config:"unlimited_upload"`
	ListMode                    string               `config:"list_mode"`
	APKVersion                  int64                `config:"apk_version"`
	AndroidAPI                  int64                `config:"android_api"`
	DownloadCache               bool                 `config:"download_cache"`
	SupportBinaryData           bool                 `config:"support_binary_data"`
	EncodeAllAsBinary           bool                 `config:"encode_all_as_binary"`
	BinaryDataChunking          bool                 `config:"binary_data_chunking"`
	BinaryDataChunkSize         fs.SizeSuffix        `config:"binary_data_chunk_size"`
	BinaryDataChunkConcurrency  int                  `config:"binary_data_chunk_concurrency"`
	BinaryVideoResolution       string               `config:"binary_video_resolution"`
	MinSizeNeededForBinaryVideo fs.SizeSuffix        `config:"min_size_needed_for_binary_video"`
	AlbumIDs                    string               `config:"album_ids"`
	AlbumID                     string               `config:"album_id"` // Backward compatibility alias
	MaxItemsPerAlbum            int                  `config:"max_items_per_album"`
	Enc                         encoder.MultiEncoder `config:"encoding"`
}

// Fs represents a remote Google Photos storage via mobile API
type Fs struct {
	name      string          // name of this remote
	root      string          // the path we are working on
	opt       Options         // parsed options
	features  *fs.Features    // optional features
	api       *MobileAPI      // mobile API client (includes pacer for rate limiting/retry)
	cache     *Cache          // SQLite cache
	albumIDs  []string        // parsed scoped album IDs
	cacheMu   sync.Mutex      // protects cache operations
	startTime time.Time       // time Fs was started
	dlCache   *downloadCache  // shared download cache for temp files
	dlExit    atexit.FnHandle // removes download cache temp files at exit
	forceSync bool            // force next sync regardless of throttle
}

type Object struct {
	fs         *Fs                   // parent Fs
	remote     string                // remote path
	media      api.MediaItem         // cached media item info
	hasMedia   bool                  // whether media info is populated
	binDetails *binary.BinaryDetails // binary metadata from caption if present
}

// Fs interface methods

func (f *Fs) Name() string { return f.name }

func (f *Fs) Root() string { return f.root }

func (f *Fs) String() string {
	return fmt.Sprintf("Google Photos Mobile path %q", f.root)
}

func (f *Fs) Features() *fs.Features { return f.features }

// NewFs constructs an Fs from the path
func NewFs(ctx context.Context, name, root string, m configmap.Mapper) (fs.Fs, error) {
	opt := new(Options)
	err := configstruct.Set(m, opt)
	if err != nil {
		return nil, fmt.Errorf("couldn't parse config: %w", err)
	}

	if opt.BinaryDataChunking {
		if !opt.SupportBinaryData {
			return nil, errors.New("invalid configuration: binary_data_chunking cannot be enabled without support_binary_data")
		}
		minChunk := fs.SizeSuffix(32 * 1024 * 1024)
		maxChunk := fs.SizeSuffix(512 * 1024 * 1024)
		if opt.BinaryDataChunkSize < minChunk || opt.BinaryDataChunkSize > maxChunk {
			return nil, fmt.Errorf("invalid configuration: binary_data_chunk_size %v is out of range: must be between 32M and 512M", opt.BinaryDataChunkSize)
		}
		if opt.BinaryDataChunkConcurrency < 1 || opt.BinaryDataChunkConcurrency > 16 {
			return nil, fmt.Errorf("invalid configuration: binary_data_chunk_concurrency %d is out of range: must be between 1 and 16", opt.BinaryDataChunkConcurrency)
		}
	}

	minVideoMin := fs.SizeSuffix(5 * 1024 * 1024)
	minVideoMax := fs.SizeSuffix(50 * 1024 * 1024)
	if opt.MinSizeNeededForBinaryVideo == 0 {
		opt.MinSizeNeededForBinaryVideo = fs.SizeSuffix(10 * 1024 * 1024)
	}
	if opt.MinSizeNeededForBinaryVideo < minVideoMin || opt.MinSizeNeededForBinaryVideo > minVideoMax {
		return nil, fmt.Errorf("invalid configuration: min_size_needed_for_binary_video %v is out of range: must be between 5M and 50M", opt.MinSizeNeededForBinaryVideo)
	}

	resName := strings.ToLower(strings.TrimSpace(opt.BinaryVideoResolution))
	if resName == "" {
		resName = "auto"
	}
	opt.BinaryVideoResolution = resName

	if resName != "auto" {
		targetRes, ok := binary.LookupResolution(resName)
		if !ok {
			return nil, fmt.Errorf("invalid configuration: binary_video_resolution %q is not recognized (valid: auto, 240p, 360p, 480p, 720p, 1080p, 1440p, 4K, 4096p)", opt.BinaryVideoResolution)
		}
		minBytes1Sec := targetRes.MinBytesFor1Sec()

		if opt.BinaryDataChunking {
			if int64(opt.BinaryDataChunkSize) < minBytes1Sec {
				return nil, fmt.Errorf("invalid configuration: binary_video_resolution %q requires at least %v for 1 second of video at 24fps, but binary_data_chunk_size is %v. Choose a lower resolution (e.g. 240p, 360p, 480p) or increase chunk size",
					opt.BinaryVideoResolution, fs.SizeSuffix(minBytes1Sec), opt.BinaryDataChunkSize)
			}
		}
	}

	authData := opt.AuthData
	if authData == "" {
		return nil, errors.New("auth_data is required - set it via config or GP_AUTH_DATA env var")
	}

	root = strings.Trim(path.Clean(root), "/")
	if root == "." || root == "/" {
		root = ""
	}

	if opt.UnlimitedUpload {
		opt.DeviceModel = "Pixel XL"
		opt.DeviceMake = "Google"
		opt.AndroidAPI = 28
	}
	api := NewMobileAPI(ctx, authData, opt.DeviceModel, opt.DeviceMake, opt.APKVersion, opt.AndroidAPI)

	cachePath := opt.CacheDBPath
	if cachePath == "" {
		cachePath = defaultCachePath(name)
	}

	cache, err := NewCache(cachePath)
	if err != nil {
		return nil, fmt.Errorf("couldn't open cache database: %w", err)
	}
	if opt.AlbumIDs == "" && opt.AlbumID != "" {
		opt.AlbumIDs = opt.AlbumID
	}
	var parsedAlbumIDs []string
	if opt.AlbumIDs != "" {
		for _, id := range strings.Split(opt.AlbumIDs, ",") {
			id = strings.TrimSpace(id)
			if idx := strings.Index(id, "AF1Qip"); idx >= 0 {
				id = id[idx:]
			}
			id = strings.TrimRight(id, "/? \t\r\n")
			if id != "" {
				parsedAlbumIDs = append(parsedAlbumIDs, id)
			}
		}
	}
	if opt.MaxItemsPerAlbum <= 0 {
		opt.MaxItemsPerAlbum = 20000
	}
	if len(parsedAlbumIDs) > 0 {
		cache.SetAlbumFilter(parsedAlbumIDs)
	}

	f := &Fs{
		name:      name,
		root:      root,
		opt:       *opt,
		api:       api,
		cache:     cache,
		albumIDs:  parsedAlbumIDs,
		startTime: time.Now(),
	}
	if opt.DownloadCache {
		f.dlCache = newDownloadCache(api)
		f.dlExit = atexit.Register(f.dlCache.shutdown)
	}

	f.features = (&fs.Features{
		ReadMimeType:   false,
		WriteMimeType:  false,
		DuplicateFiles: true,
		ReadMetadata:   true,
		WriteMetadata:  true,
	}).Fill(ctx, f)

	if root != "" {
		_, leaf := path.Split(root)
		_, err := f.NewObject(ctx, leaf)
		if err == nil {
			f.root = path.Dir(root)
			if f.root == "." {
				f.root = ""
			}
			return f, fs.ErrorIsFile
		}
	}

	return f, nil
}

func (f *Fs) Precision() time.Duration { return fs.ModTimeNotSupported }

func (f *Fs) Hashes() hash.Set { return hash.Set(hash.SHA1) }

func (f *Fs) getVirtualPaths(item *api.MediaItem, fileName string) []string {
	var itemPaths []string
	tm := time.UnixMilli(item.UTCTimestamp).UTC()

	parsedPaths, binDetails := parseCaption(item.Caption)
	effectiveFileName := fileName
	if f.opt.SupportBinaryData && binDetails != nil && binDetails.Name != "" {
		effectiveFileName = binDetails.Name
	}

	switch strings.ToLower(f.opt.ListMode) {
	case "year":
		itemPaths = append(itemPaths, fmt.Sprintf("%04d/%s", tm.Year(), effectiveFileName))
	case "month", "year_month":
		itemPaths = append(itemPaths, fmt.Sprintf("%04d/%04d-%02d/%s", tm.Year(), tm.Year(), tm.Month(), effectiveFileName))
	case "calendar", "calender", "day", "week":
		itemPaths = append(itemPaths, fmt.Sprintf("%04d/%04d-%02d/%04d-%02d-%02d/%s", tm.Year(), tm.Year(), tm.Month(), tm.Year(), tm.Month(), tm.Day(), effectiveFileName))
	case "description_path", "description":
		if len(parsedPaths) > 0 {
			for _, p := range parsedPaths {
				p = strings.Trim(strings.TrimSpace(p), "/")
				if p != "" {
					itemPaths = append(itemPaths, path.Join(p, effectiveFileName))
				} else {
					itemPaths = append(itemPaths, effectiveFileName)
				}
			}
		} else {
			itemPaths = append(itemPaths, effectiveFileName)
		}
	default: // "flat"
		itemPaths = append(itemPaths, effectiveFileName)
	}

	var relPaths []string
	for _, p := range itemPaths {
		if f.root != "" {
			if !strings.HasPrefix(p, f.root+"/") && p != f.root {
				continue
			}
			p = strings.TrimPrefix(p, f.root+"/")
		}
		relPaths = append(relPaths, p)
	}

	return relPaths
}

// NewObject finds the Object at remote
func (f *Fs) NewObject(ctx context.Context, remote string) (fs.Object, error) {
	if err := f.ensureCachePopulated(ctx); err != nil {
		return nil, fmt.Errorf("cache sync failed: %w", err)
	}

	_, fileName := path.Split(remote)
	fileName = f.opt.Enc.ToStandardName(fileName)

	item, err := f.cache.GetByFileName(fileName)
	if errors.Is(err, sql.ErrNoRows) {
		if f.opt.SupportBinaryData {
			item, err = f.cache.GetByBinaryName(fileName)
		}
		if errors.Is(err, sql.ErrNoRows) {
			item, err = f.findByDedupSuffix(fileName)
			if err != nil {
				return nil, fs.ErrorObjectNotFound
			}
		} else if err != nil {
			return nil, fmt.Errorf("cache binary lookup failed: %w", err)
		}
	} else if err != nil {
		return nil, fmt.Errorf("cache lookup failed: %w", err)
	}

	// Verify that the requested remote actually matches one of the item's virtual paths
	validPaths := f.getVirtualPaths(item, fileName)
	isValid := false
	for _, vp := range validPaths {
		if vp == remote {
			isValid = true
			break
		}
	}

	if !isValid {
		return nil, fs.ErrorObjectNotFound
	}

	var binDetails *binary.BinaryDetails
	if f.opt.SupportBinaryData {
		_, binDetails = parseCaption(item.Caption)
		if binDetails != nil && binDetails.ChunkDetails != nil {
			chunks, err := f.cache.GetChunksByBinaryName(binDetails.Name)
			if err != nil || len(chunks) < binDetails.ChunkDetails.TotalChunks {
				// Incomplete chunked file!
				return nil, fs.ErrorObjectNotFound
			}
		}
	}

	return &Object{
		fs:         f,
		remote:     remote,
		media:      *item,
		hasMedia:   true,
		binDetails: binDetails,
	}, nil
}

func (f *Fs) getTargetAlbumID() (string, error) {
	if len(f.albumIDs) == 0 {
		return "", nil
	}
	return f.cache.GetActiveUploadAlbum(f.albumIDs, f.opt.MaxItemsPerAlbum)
}

func (f *Fs) findByDedupSuffix(fileName string) (*api.MediaItem, error) {
	ext := path.Ext(fileName)
	base := fileName[:len(fileName)-len(ext)]

	idx := strings.LastIndex(base, "_")
	if idx < 0 {
		return nil, fs.ErrorObjectNotFound
	}
	suffix := base[idx+1:]
	if suffix == "" {
		return nil, fs.ErrorObjectNotFound
	}

	item, err := f.cache.GetByDedupKey(suffix)
	if err == nil && (item.TrashTimestamp == 0) {
		return item, nil
	}
	item, err = f.cache.GetByMediaKey(suffix)
	if err == nil && (item.TrashTimestamp == 0) {
		if len(f.albumIDs) > 0 {
			matched := false
			for _, id := range f.albumIDs {
				if item.CollectionID == id {
					matched = true
					break
				}
			}
			if !matched {
				return nil, fs.ErrorObjectNotFound
			}
		}
		return item, nil
	}
	return nil, fs.ErrorObjectNotFound
}

// List the objects and directories in dir into entries
func (f *Fs) List(ctx context.Context, dir string) (entries fs.DirEntries, err error) {
	err = f.ensureCachePopulated(ctx)
	if err != nil {
		return nil, fmt.Errorf("cache sync failed: %w", err)
	}

	items, err := f.cache.ListAll()
	if err != nil {
		return nil, fmt.Errorf("cache list failed: %w", err)
	}

	// Deduplicate items by dedup_key so album references / shadow entries don't duplicate files
	seenDedup := make(map[string]int)
	var dedupedItems []api.MediaItem
	for _, item := range items {
		dKey := item.DedupKey
		if dKey == "" {
			dKey = item.MediaKey
		}
		if idx, seen := seenDedup[dKey]; seen {
			// If already seen, prefer the canonical item
			if item.IsCanonical && !dedupedItems[idx].IsCanonical {
				dedupedItems[idx] = item
			}
			continue
		}
		seenDedup[dKey] = len(dedupedItems)
		dedupedItems = append(dedupedItems, item)
	}
	items = dedupedItems

	dirs := make(map[string]bool)
	seenFiles := make(map[string]bool)
	chunkCounts := make(map[string]int)

	if f.opt.SupportBinaryData {
		for _, item := range items {
			_, bin := parseCaption(item.Caption)
			if bin != nil && bin.ChunkDetails != nil {
				chunkCounts[bin.Name]++
			}
		}
	}

	for _, item := range items {
		if item.FileName == "" {
			continue
		}

		fileName := f.opt.Enc.FromStandardName(item.FileName)
		var binDetails *binary.BinaryDetails
		if f.opt.SupportBinaryData {
			_, binDetails = parseCaption(item.Caption)
			if binDetails != nil && binDetails.ChunkDetails != nil {
				if chunkCounts[binDetails.Name] < binDetails.ChunkDetails.TotalChunks {
					// Incomplete file, do not list until all chunks finish uploading
					continue
				}
			}
		}

		itemPaths := f.getVirtualPaths(&item, fileName)

		for _, itemPath := range itemPaths {

			// Now itemPath is relative to the current virtual filesystem root.
			// We check it against the requested `dir`.
			if dir == "" {
				if !strings.Contains(itemPath, "/") {
					if !seenFiles[itemPath] {
						seenFiles[itemPath] = true
						o := &Object{fs: f, remote: itemPath, media: item, hasMedia: true, binDetails: binDetails}
						entries = append(entries, o)
					}
				} else {
					dirName := strings.Split(itemPath, "/")[0]
					if !dirs[dirName] {
						dirs[dirName] = true
						entries = append(entries, fs.NewDir(dirName, f.startTime))
					}
				}
			} else {
				if strings.HasPrefix(itemPath, dir+"/") {
					relPath := strings.TrimPrefix(itemPath, dir+"/")
					if !strings.Contains(relPath, "/") {
						if !seenFiles[itemPath] {
							seenFiles[itemPath] = true
							o := &Object{fs: f, remote: itemPath, media: item, hasMedia: true, binDetails: binDetails}
							entries = append(entries, o)
						}
					} else {
						subDirName := strings.Split(relPath, "/")[0]
						dirRemote := dir + "/" + subDirName
						if !dirs[dirRemote] {
							dirs[dirRemote] = true
							entries = append(entries, fs.NewDir(dirRemote, f.startTime))
						}
					}
				}
			}
		}
	}

	return entries, nil
}

// Put uploads a file
func (f *Fs) Put(ctx context.Context, in io.Reader, src fs.ObjectInfo, options ...fs.OpenOption) (fs.Object, error) {
	if err := f.cache.CheckTotalAlbumCapacity(f.albumIDs, f.opt.MaxItemsPerAlbum, 1); err != nil {
		return nil, fmt.Errorf("album capacity check failed: %w", err)
	}

	remote := src.Remote()
	size := src.Size()

	var binDetails *binary.BinaryDetails
	uploadFileName := path.Base(remote)
	var sha1Bytes []byte
	var sha1B64 string
	uploadIn := in

	shouldEncodeBinary := f.opt.SupportBinaryData && (f.opt.EncodeAllAsBinary || !binary.IsNativeMedia(remote))
	if shouldEncodeBinary {
		if f.opt.BinaryDataChunking && size > int64(f.opt.BinaryDataChunkSize) {
			return f.putChunkedBinary(ctx, in, src, remote, size)
		}

		unwrappedIn, wrap := accounting.UnWrap(in)
		codecOpts := binary.CodecOptions{
			MinVideoSize:    int64(f.opt.MinSizeNeededForBinaryVideo),
			VideoResolution: f.opt.BinaryVideoResolution,
		}
		encoded, err := binary.Encode(unwrappedIn, remote, size, codecOpts)
		if err != nil {
			return nil, fmt.Errorf("binary encoding failed: %w", err)
		}
		defer encoded.Close()

		uploadIn = wrap(encoded.File)
		size = encoded.MediaSize
		sha1B64 = encoded.MediaSHA1B64
		sha1Bytes, _ = base64.StdEncoding.DecodeString(sha1B64)
		uploadFileName = encoded.MediaFilename
		binDetails = &encoded.Details
	} else {
		hashStr, err := src.Hash(ctx, hash.SHA1)
		if err != nil {
			return nil, fmt.Errorf("failed to get SHA1 hash: %w", err)
		}

		sha1Bytes, err = hex.DecodeString(hashStr)
		if err != nil {
			return nil, fmt.Errorf("invalid SHA1 hash hex: %w", err)
		}
		sha1B64 = base64.StdEncoding.EncodeToString(sha1Bytes)
	}

	mediaKey, err := f.api.FindRemoteMediaByHash(ctx, sha1Bytes)
	if err == nil && mediaKey != "" {
		fs.Debugf(f, "File %q already exists with media key %s", remote, mediaKey)
		_, _ = io.Copy(io.Discard, uploadIn)
	} else {
		uploadToken, err := f.api.GetUploadToken(ctx, sha1B64, size)
		if err != nil {
			return nil, fmt.Errorf("failed to get upload token: %w", err)
		}

		uploadResp, err := f.api.UploadFile(ctx, uploadIn, size, uploadToken)
		if err != nil {
			return nil, fmt.Errorf("upload failed: %w", err)
		}

		uploadFileName = f.opt.Enc.ToStandardName(uploadFileName)
		mediaKey, err = f.api.CommitUpload(ctx, uploadResp, uploadFileName, sha1Bytes)
		if err != nil {
			return nil, fmt.Errorf("commit upload failed: %w", err)
		}
	}

	f.cacheMu.Lock()
	f.forceSync = true
	f.cacheMu.Unlock()
	_ = f.ensureCachePopulated(ctx)

	// Generate dedupKey locally from SHA1 to reliably hit the cache
	s := strings.ReplaceAll(sha1B64, "+", "-")
	s = strings.ReplaceAll(s, "/", "_")
	localDedupKey := strings.TrimRight(s, "=")

	media := api.MediaItem{
		MediaKey:  mediaKey,
		FileName:  uploadFileName,
		SizeBytes: size,
		DedupKey:  localDedupKey,
	}

	if cachedItem, err := f.cache.GetByMediaKey(mediaKey); err == nil {
		media = *cachedItem
	} else if cachedItem, err := f.cache.GetByDedupKey(localDedupKey); err == nil {
		media = *cachedItem
		media.MediaKey = mediaKey
	}

	if media.DedupKey != "" {
		if media.TrashTimestamp > 0 {
			// The item was previously moved to trash. Restore it on Google Photos!
			if err := f.api.RestoreFromTrash(ctx, []string{media.DedupKey}); err != nil {
				fs.Debugf(f, "Failed to restore %q from trash: %v", remote, err)
			} else {
				media.TrashTimestamp = 0
			}
		}

		existingPaths, existingBin := parseCaption(media.Caption)
		if binDetails == nil {
			binDetails = existingBin
			if binDetails == nil {
				// Recover binaryDetails if another entry with this dedupKey had it
				if otherItem, err := f.cache.GetByDedupKey(media.DedupKey); err == nil {
					_, otherBin := parseCaption(otherItem.Caption)
					if otherBin != nil {
						binDetails = otherBin
					}
				}
			}
		}

		fullPath := path.Join(f.root, remote)
		dir := path.Dir(fullPath)
		if dir == "." || dir == "/" {
			dir = ""
		}

		newPath := "/"
		if dir != "" {
			newPath = fmt.Sprintf("/%s", dir)
		}

		pathList := existingPaths
		exists := false
		for _, p := range pathList {
			if p == newPath {
				exists = true
				break
			}
		}
		if !exists {
			pathList = append(pathList, newPath)
		}

		newCaption := buildCaption(pathList, binDetails)
		err := f.api.SetCaption(ctx, media.DedupKey, newCaption)
		if err != nil {
			fs.Debugf(f, "Failed to set caption for %q: %v", remote, err)
		} else {
			media.Caption = newCaption
			media.TrashTimestamp = 0
			_ = f.cache.UpsertItems([]api.MediaItem{media})
		}
	}

	alreadyInAlbum := false
	for _, id := range f.albumIDs {
		if media.CollectionID == id {
			alreadyInAlbum = true
			break
		}
	}

	if !alreadyInAlbum && len(f.albumIDs) > 0 && mediaKey != "" {
		targetAlbum, err := f.getTargetAlbumID()
		if err == nil && targetAlbum != "" {
			if err := f.api.AddMediaToAlbum(ctx, targetAlbum, []string{mediaKey}); err != nil {
				fs.Errorf(f, "Failed to add %q to album %q: %v", remote, targetAlbum, err)
			} else {
				media.CollectionID = targetAlbum
				_ = f.cache.UpsertItems([]api.MediaItem{media})
			}
		}
	}

	return f.NewObject(ctx, remote)
}

func (f *Fs) putChunkedBinary(ctx context.Context, in io.Reader, src fs.ObjectInfo, remote string, size int64) (fs.Object, error) {
	chunkSize := int64(f.opt.BinaryDataChunkSize)
	totalChunks := int((size + chunkSize - 1) / chunkSize)

	baseName := path.Base(remote)
	fullPath := path.Join(f.root, remote)
	dir := path.Dir(fullPath)
	if dir == "." || dir == "/" {
		dir = ""
	}
	newPath := "/"
	if dir != "" {
		newPath = fmt.Sprintf("/%s", dir)
	}

	// Build upfront deterministic album assignment plan for all chunks
	chunkAlbumPlan := make([]string, totalChunks)
	alreadyInAlbum := make([]bool, totalChunks)

	if len(f.albumIDs) > 0 {
		albumCounts := make(map[string]int)
		for _, id := range f.albumIDs {
			count, err := f.cache.CountItemsInAlbum(id)
			if err != nil {
				return nil, fmt.Errorf("failed to check item count for album %s: %w", id, err)
			}
			albumCounts[id] = count
		}

		// Step 1: Detect existing chunks already assigned to an album from previous runs
		for i := 0; i < totalChunks; i++ {
			chunkFileName := f.opt.Enc.ToStandardName(fmt.Sprintf("%s.part%03d.mkv", baseName, i))
			if ch, err := f.cache.GetByFileName(chunkFileName); err == nil && ch.CollectionID != "" {
				for _, id := range f.albumIDs {
					if ch.CollectionID == id {
						chunkAlbumPlan[i] = id
						alreadyInAlbum[i] = true
						break
					}
				}
			}
		}

		// Step 2: Assign unassigned chunks sequentially, filling each album to max_items_per_album
		currentAlbumIdx := 0
		for i := 0; i < totalChunks; i++ {
			if chunkAlbumPlan[i] != "" {
				continue // Already has an assigned album
			}
			for currentAlbumIdx < len(f.albumIDs) && albumCounts[f.albumIDs[currentAlbumIdx]] >= f.opt.MaxItemsPerAlbum {
				currentAlbumIdx++
			}
			if currentAlbumIdx >= len(f.albumIDs) {
				var statuses []string
				for _, id := range f.albumIDs {
					statuses = append(statuses, fmt.Sprintf("%s (%d/%d full)", id, albumCounts[id], f.opt.MaxItemsPerAlbum))
				}
				return nil, fmt.Errorf("insufficient album capacity: cannot assign chunk %d of %d (all %d configured albums have reached the %d items limit: [%s]). Please add more album IDs to album_ids or increase max_items_per_album",
					i, totalChunks, len(f.albumIDs), f.opt.MaxItemsPerAlbum, strings.Join(statuses, ", "))
			}
			targetID := f.albumIDs[currentAlbumIdx]
			chunkAlbumPlan[i] = targetID
			albumCounts[targetID]++
		}
	}

	totalSHA1, err := src.Hash(ctx, hash.SHA1)
	if err != nil {
		totalSHA1 = ""
	}

	concurrency := f.opt.BinaryDataChunkConcurrency
	if concurrency < 1 {
		concurrency = 4
	}
	if concurrency > totalChunks {
		concurrency = totalChunks
	}

	totalStartTime := time.Now()
	fs.Debugf(f, "[CHUNK-PIPELINE] Starting upload of %q (size: %d bytes, %d chunks @ %d bytes, concurrency: %d)",
		remote, size, totalChunks, chunkSize, concurrency)

	unwrappedIn, wrap := accounting.UnWrap(in)

	type chunkUploadJob struct {
		chunkIndex     int
		encoded        *binary.EncodedMedia
		token          string
		mediaKey       string
		targetAlbum    string
		alreadyInAlbum bool
	}

	type chunkCommitJob struct {
		chunkIndex     int
		uploadResp     []byte
		uploadFileName string
		sha1Bytes      []byte
		localDedupKey  string
		cMediaSize     int64
		mediaKey       string
		details        binary.BinaryDetails
		targetAlbum    string
		alreadyInAlbum bool
	}

	// Buffer twice the concurrency so workers are never starved waiting for local encoding
	queueCap := concurrency * 2
	if queueCap > totalChunks {
		queueCap = totalChunks
	}
	jobs := make(chan chunkUploadJob, queueCap)
	commitJobs := make(chan chunkCommitJob, queueCap)
	var uploadErr error
	var errMu sync.Mutex
	var wg sync.WaitGroup
	var commitWg sync.WaitGroup

	// Background producer goroutine: continuously pre-encodes chunks and pre-fetches tokens
	go func() {
		defer close(jobs)
		for i := 0; i < totalChunks; i++ {
			errMu.Lock()
			hasErr := (uploadErr != nil)
			errMu.Unlock()
			if hasErr || ctx.Err() != nil {
				fs.Debugf(f, "[PRODUCER] Aborting encoding loop due to error or cancellation")
				break
			}

			thisChunkBytes := chunkSize
			if i == totalChunks-1 {
				thisChunkBytes = size - int64(i)*chunkSize
			}

			tEncode := time.Now()
			fs.Debugf(f, "[PRODUCER] [Chunk %d/%d] Slicing & encoding %d bytes...", i, totalChunks, thisChunkBytes)

			codecOpts := binary.CodecOptions{
				MinVideoSize:    int64(f.opt.MinSizeNeededForBinaryVideo),
				VideoResolution: f.opt.BinaryVideoResolution,
			}
			encodedChunk, err := binary.EncodeChunk(
				io.LimitReader(unwrappedIn, thisChunkBytes),
				baseName,
				i,
				totalChunks,
				thisChunkBytes,
				int64(i)*chunkSize,
				size,
				totalSHA1,
				codecOpts,
			)
			if err != nil {
				errMu.Lock()
				if uploadErr == nil {
					uploadErr = fmt.Errorf("chunk %d encoding failed: %w", i, err)
				}
				errMu.Unlock()
				fs.Errorf(f, "[PRODUCER] [Chunk %d/%d] Encoding failed: %v", i, totalChunks, err)
				break
			}

			cMediaSize := encodedChunk.MediaSize
			sha1B64 := encodedChunk.MediaSHA1B64
			sha1Bytes, _ := base64.StdEncoding.DecodeString(sha1B64)

			// Pre-fetch upload token so workers start network transfers with 0ms latency
			tPreFetch := time.Now()
			mediaKey, _ := f.api.FindRemoteMediaByHash(ctx, sha1Bytes)
			var token string
			if mediaKey == "" {
				token, err = f.api.GetUploadToken(ctx, sha1B64, cMediaSize)
				if err != nil {
					errMu.Lock()
					if uploadErr == nil {
						uploadErr = fmt.Errorf("chunk %d token pre-fetch failed: %w", i, err)
					}
					errMu.Unlock()
					_ = encodedChunk.Close()
					fs.Errorf(f, "[PRODUCER] [Chunk %d/%d] Token pre-fetch failed: %v", i, totalChunks, err)
					break
				}
				fs.Debugf(f, "[PRODUCER] [Chunk %d/%d] Encoded in %v, pre-fetched token in %v. Queuing...",
					i, totalChunks, time.Since(tEncode), time.Since(tPreFetch))
			} else {
				fs.Debugf(f, "[PRODUCER] [Chunk %d/%d] Encoded in %v (chunk already on remote). Queuing...",
					i, totalChunks, time.Since(tEncode))
			}

			select {
			case jobs <- chunkUploadJob{
				chunkIndex:     i,
				encoded:        encodedChunk,
				token:          token,
				mediaKey:       mediaKey,
				targetAlbum:    chunkAlbumPlan[i],
				alreadyInAlbum: alreadyInAlbum[i],
			}:
				fs.Debugf(f, "[PRODUCER] [Chunk %d/%d] Enqueued successfully with pre-fetched token (target: %s)",
					i, totalChunks, chunkAlbumPlan[i])
			case <-ctx.Done():
				_ = encodedChunk.Close()
				return
			}
		}
	}()

	// Background commit workers: process metadata commitments and captions asynchronously
	const commitWorkers = 2
	for c := 0; c < commitWorkers; c++ {
		commitWg.Add(1)
		cWorkerID := c
		go func() {
			defer commitWg.Done()
			for cJob := range commitJobs {
				errMu.Lock()
				hasErr := (uploadErr != nil)
				errMu.Unlock()
				if hasErr || ctx.Err() != nil {
					continue
				}

				mediaKey := cJob.mediaKey
				if mediaKey == "" {
					tCommit := time.Now()
					var err error
					mediaKey, err = f.api.CommitUpload(ctx, cJob.uploadResp, cJob.uploadFileName, cJob.sha1Bytes)
					if err != nil {
						errMu.Lock()
						if uploadErr == nil {
							uploadErr = fmt.Errorf("chunk %d commit failed: %w", cJob.chunkIndex, err)
						}
						errMu.Unlock()
						fs.Errorf(f, "[COMMITTER-%d] [Chunk %d/%d] Commit failed: %v", cWorkerID, cJob.chunkIndex, totalChunks, err)
						continue
					}
					fs.Debugf(f, "[COMMITTER-%d] [Chunk %d/%d] Commit finished in %v (mediaKey: %s)",
						cWorkerID, cJob.chunkIndex, totalChunks, time.Since(tCommit), mediaKey)
				}

				if cJob.targetAlbum != "" && mediaKey != "" && !cJob.alreadyInAlbum {
					if err := f.api.AddMediaToAlbum(ctx, cJob.targetAlbum, []string{mediaKey}); err != nil {
						fs.Errorf(f, "[COMMITTER-%d] [Chunk %d/%d] Failed to add to album %q: %v",
							cWorkerID, cJob.chunkIndex, totalChunks, cJob.targetAlbum, err)
					}
				}

				media := api.MediaItem{
					MediaKey:     mediaKey,
					FileName:     cJob.uploadFileName,
					SizeBytes:    cJob.cMediaSize,
					DedupKey:     cJob.localDedupKey,
					CollectionID: cJob.targetAlbum,
				}

				tCaption := time.Now()
				chunkCaption := buildCaption([]string{newPath}, &cJob.details)
				_ = f.api.SetCaption(ctx, media.DedupKey, chunkCaption)
				media.Caption = chunkCaption
				_ = f.cache.UpsertItems([]api.MediaItem{media})

				fs.Debugf(f, "[COMMITTER-%d] [Chunk %d/%d] Caption set & cached in %v (album: %s)",
					cWorkerID, cJob.chunkIndex, totalChunks, time.Since(tCaption), cJob.targetAlbum)
			}
		}()
	}

	// Upload worker pool: streams chunks continuously with 0ms inter-chunk delay and phase-shifted startup
	for w := 0; w < concurrency; w++ {
		wg.Add(1)
		workerID := w
		go func() {
			defer wg.Done()

			// Phase-shift worker startup (1.2s per worker) to stagger completion times across the pipeline
			if workerID > 0 {
				staggerDur := time.Duration(workerID) * 1200 * time.Millisecond
				fs.Debugf(f, "[WORKER-%d] Starting with pipeline phase-shift delay of %v", workerID, staggerDur)
				select {
				case <-time.After(staggerDur):
				case <-ctx.Done():
					return
				}
			}

			for job := range jobs {
				errMu.Lock()
				hasErr := (uploadErr != nil)
				errMu.Unlock()
				if hasErr || ctx.Err() != nil {
					_ = job.encoded.Close()
					continue
				}

				tJobStart := time.Now()
				fs.Debugf(f, "[WORKER-%d] [Chunk %d/%d] Dequeued ready-to-upload job (%s, %d bytes)",
					workerID, job.chunkIndex, totalChunks, job.encoded.MediaFilename, job.encoded.MediaSize)

				chunkUploadIn := wrap(job.encoded.File)
				cMediaSize := job.encoded.MediaSize
				sha1B64 := job.encoded.MediaSHA1B64
				sha1Bytes, _ := base64.StdEncoding.DecodeString(sha1B64)
				uploadFileName := f.opt.Enc.ToStandardName(job.encoded.MediaFilename)

				s := strings.ReplaceAll(sha1B64, "+", "-")
				s = strings.ReplaceAll(s, "/", "_")
				localDedupKey := strings.TrimRight(s, "=")

				if job.mediaKey != "" {
					fs.Debugf(f, "[WORKER-%d] [Chunk %d/%d] Reusing existing chunk mediaKey %s (skipping upload)",
						workerID, job.chunkIndex, totalChunks, job.mediaKey)
					_, _ = io.Copy(io.Discard, chunkUploadIn)
					_ = job.encoded.Close()

					commitJobs <- chunkCommitJob{
						chunkIndex:     job.chunkIndex,
						uploadFileName: uploadFileName,
						localDedupKey:  localDedupKey,
						cMediaSize:     cMediaSize,
						mediaKey:       job.mediaKey,
						details:        job.encoded.Details,
						targetAlbum:    job.targetAlbum,
						alreadyInAlbum: job.alreadyInAlbum,
					}
					continue
				}

				// The upload token was already pre-fetched! Start network transfer immediately with 0ms delay!
				tUpload := time.Now()
				fs.Debugf(f, "[WORKER-%d] [Chunk %d/%d] Starting network transfer of %d bytes with pre-fetched token...",
					workerID, job.chunkIndex, totalChunks, cMediaSize)

				resp, err := f.api.UploadFile(ctx, chunkUploadIn, cMediaSize, job.token)
				if err != nil {
					_ = job.encoded.Close()
					errMu.Lock()
					if uploadErr == nil {
						uploadErr = fmt.Errorf("chunk %d upload failed: %w", job.chunkIndex, err)
					}
					errMu.Unlock()
					fs.Errorf(f, "[WORKER-%d] [Chunk %d/%d] UploadFile failed: %v", workerID, job.chunkIndex, totalChunks, err)
					continue
				}

				uploadDuration := time.Since(tUpload)
				uploadMBps := float64(cMediaSize) / (1024 * 1024) / uploadDuration.Seconds()
				fs.Debugf(f, "[WORKER-%d] [Chunk %d/%d] Network transfer finished in %v (%.2f MB/s). Handing off to committer!",
					workerID, job.chunkIndex, totalChunks, uploadDuration, uploadMBps)

				_ = job.encoded.Close()

				// Asynchronously hand off commit & caption to background queue without stalling the upload worker!
				commitJobs <- chunkCommitJob{
					chunkIndex:     job.chunkIndex,
					uploadResp:     resp,
					uploadFileName: uploadFileName,
					sha1Bytes:      sha1Bytes,
					localDedupKey:  localDedupKey,
					cMediaSize:     cMediaSize,
					details:        job.encoded.Details,
					targetAlbum:    job.targetAlbum,
					alreadyInAlbum: job.alreadyInAlbum,
				}

				fs.Debugf(f, "[WORKER-%d] [Chunk %d/%d] Immediately ready for next chunk (transfer time: %v)",
					workerID, job.chunkIndex, totalChunks, time.Since(tJobStart))
			}
		}()
	}

	wg.Wait()
	close(commitJobs)
	commitWg.Wait()

	if uploadErr != nil {
		return nil, uploadErr
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}

	fs.Debugf(f, "[CHUNK-PIPELINE] All %d chunks completed in %v. Finalizing cache...",
		totalChunks, time.Since(totalStartTime))

	f.cacheMu.Lock()
	f.forceSync = true
	f.cacheMu.Unlock()
	_ = f.ensureCachePopulated(ctx)

	return f.NewObject(ctx, remote)
}

func (f *Fs) Mkdir(ctx context.Context, dir string) error {

	return nil
}

func (f *Fs) Shutdown(ctx context.Context) error {
	if f.dlCache != nil {
		atexit.Unregister(f.dlExit)
		f.dlCache.shutdown()
	}
	return f.cache.Close()
}

func (f *Fs) Rmdir(ctx context.Context, dir string) error {

	return errCantRmdir
}

const syncIntervalSeconds = 30

// List, NewObject, and any method that reads from the cache.
func (f *Fs) ensureCachePopulated(ctx context.Context) error {
	f.cacheMu.Lock()
	defer f.cacheMu.Unlock()

	initComplete, err := f.cache.GetInitState()
	if err != nil {
		return err
	}

	if !initComplete {
		fs.Infof(f, "Initializing library cache (first run)...")
		err = f.fullCacheInit(ctx)
		if err != nil {
			return fmt.Errorf("cache init failed: %w", err)
		}
		err = f.cache.SetInitState(true)
		if err != nil {
			return err
		}
		_ = f.cache.SetLastSyncTime(time.Now().Unix())
		return nil
	}

	lastSync, err := f.cache.GetLastSyncTime()
	if err != nil {
		return err
	}

	now := time.Now().Unix()
	if f.forceSync || now-lastSync >= syncIntervalSeconds {
		fs.Debugf(f, "Performing incremental cache sync...")
		err = f.incrementalSync(ctx)
		if err != nil {
			return err
		}
		_ = f.cache.SetLastSyncTime(now)
		f.forceSync = false
	}

	return nil
}

func (f *Fs) fullCacheInit(ctx context.Context) error {
	stateToken, pageToken, err := f.cache.GetStateTokens()
	if err != nil {
		return err
	}

	if pageToken != "" {
		err = f.processInitPages(ctx, pageToken)
		if err != nil {
			return err
		}
	}

	respData, err := f.api.GetLibraryState(ctx, stateToken)
	if err != nil {
		return fmt.Errorf("get library state failed: %w", err)
	}

	newStateToken, newPageToken, items, deletions := ParseDbUpdate(respData)

	err = f.cache.UpdateStateTokens(newStateToken, newPageToken)
	if err != nil {
		return err
	}
	err = f.cache.UpsertItems(items)
	if err != nil {
		return err
	}
	if len(deletions) > 0 {
		err = f.cache.DeleteItems(deletions)
		if err != nil {
			return err
		}
	}

	fs.Infof(f, "Init: %d items synced, %d deleted", len(items), len(deletions))

	if newPageToken != "" {
		err = f.processInitPages(ctx, newPageToken)
		if err != nil {
			return err
		}
	}

	return nil
}

func (f *Fs) processInitPages(ctx context.Context, pageToken string) error {
	for pageToken != "" {
		respData, err := f.api.GetLibraryPageInit(ctx, pageToken)
		if err != nil {
			return fmt.Errorf("get library page init failed: %w", err)
		}

		_, nextPageToken, items, deletions := ParseDbUpdate(respData)

		err = f.cache.UpdateStateTokens("", nextPageToken)
		if err != nil {
			return err
		}
		err = f.cache.UpsertItems(items)
		if err != nil {
			return err
		}
		if len(deletions) > 0 {
			err = f.cache.DeleteItems(deletions)
			if err != nil {
				return err
			}
		}

		fs.Infof(f, "Init page: %d items synced, %d deleted", len(items), len(deletions))
		pageToken = nextPageToken
	}
	return nil
}

func (f *Fs) incrementalSync(ctx context.Context) error {
	stateToken, _, err := f.cache.GetStateTokens()
	if err != nil {
		return err
	}

	if stateToken == "" {
		return nil
	}

	respData, err := f.api.GetLibraryState(ctx, stateToken)
	if err != nil {
		return fmt.Errorf("incremental sync failed: %w", err)
	}

	newStateToken, newPageToken, items, deletions := ParseDbUpdate(respData)

	err = f.cache.UpdateStateTokens(newStateToken, newPageToken)
	if err != nil {
		return err
	}
	err = f.cache.UpsertItems(items)
	if err != nil {
		return err
	}
	if len(deletions) > 0 {
		err = f.cache.DeleteItems(deletions)
		if err != nil {
			return err
		}
	}

	if len(items) > 0 || len(deletions) > 0 {
		fs.Infof(f, "Incremental sync: %d updated, %d deleted", len(items), len(deletions))
	}

	if newPageToken != "" {
		return f.processPages(ctx, stateToken, newPageToken)
	}

	return nil
}

func (f *Fs) processPages(ctx context.Context, stateToken, pageToken string) error {
	for pageToken != "" {
		respData, err := f.api.GetLibraryPage(ctx, pageToken, stateToken)
		if err != nil {
			return fmt.Errorf("get library page failed: %w", err)
		}

		_, nextPageToken, items, deletions := ParseDbUpdate(respData)

		err = f.cache.UpdateStateTokens("", nextPageToken)
		if err != nil {
			return err
		}
		err = f.cache.UpsertItems(items)
		if err != nil {
			return err
		}
		if len(deletions) > 0 {
			err = f.cache.DeleteItems(deletions)
			if err != nil {
				return err
			}
		}

		fs.Infof(f, "Sync page: %d updated, %d deleted", len(items), len(deletions))
		pageToken = nextPageToken
	}
	return nil
}

func (f *Fs) About(ctx context.Context) (*fs.Usage, error) {
	respData, err := f.api.GetLibraryState(ctx, "")
	if err != nil {
		return nil, fmt.Errorf("failed to get library state: %w", err)
	}

	root, err := DecodeRaw(respData)
	if err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	field1, err := root.GetMessage(1)
	if err != nil {
		return nil, fmt.Errorf("no field 1 in response: %w", err)
	}

	usage := &fs.Usage{}

	if accountInfo, err := field1.GetMessage(4); err == nil {
		if itemCountMsg, err := accountInfo.GetMessage(5); err == nil {
			count := itemCountMsg.GetVarint(1)
			usage.Objects = fs.NewUsageValue(count)
		}

		if quotaInfo, err := accountInfo.GetMessage(8); err == nil {
			used := quotaInfo.GetVarint(1)  // bytes used against quota
			total := quotaInfo.GetVarint(2) // total quota bytes
			if total > 0 {
				usage.Total = fs.NewUsageValue(total)
				usage.Used = fs.NewUsageValue(used)
				usage.Free = fs.NewUsageValue(total - used)
			}
		}
	}

	return usage, nil
}

func (f *Fs) Command(ctx context.Context, name string, arg []string, opt map[string]string) (interface{}, error) {
	switch name {
	case "set-description":
		if len(arg) < 1 {
			return nil, errors.New("set-description requires a file path argument")
		}
		description, ok := opt["description"]
		if !ok {
			return nil, errors.New("set-description requires -o description=VALUE")
		}
		obj, err := f.NewObject(ctx, arg[0])
		if err != nil {
			return nil, err
		}
		o := obj.(*Object)
		if o.media.DedupKey == "" {
			return nil, errors.New("no dedup key available")
		}
		err = f.api.SetCaption(ctx, o.media.DedupKey, description)
		if err != nil {
			return nil, fmt.Errorf("failed to set description: %w", err)
		}
		f.cacheMu.Lock()
		f.forceSync = true
		f.cacheMu.Unlock()
		_ = f.ensureCachePopulated(ctx)
		return "description set successfully", nil

	default:
		return nil, fs.ErrorCommandNotFound
	}
}

// Fs returns the parent Fs
func (o *Object) Fs() fs.Info { return o.fs }

func (o *Object) Remote() string { return o.remote }

func (o *Object) String() string {
	if o == nil {
		return "<nil>"
	}
	return o.remote
}

func (o *Object) Size() int64 {
	if !o.hasMedia {
		return -1
	}
	if o.binDetails != nil {
		return o.binDetails.Size
	}
	return o.media.SizeBytes
}

func (o *Object) ModTime(ctx context.Context) time.Time {
	if !o.hasMedia {
		return time.Time{}
	}
	if o.media.UTCTimestamp > 0 {
		return time.Unix(o.media.UTCTimestamp/1000, (o.media.UTCTimestamp%1000)*int64(time.Millisecond))
	}
	return time.Time{}
}

func (o *Object) SetModTime(ctx context.Context, modTime time.Time) error {
	return fs.ErrorCantSetModTime
}

func (o *Object) Storable() bool { return true }

func (o *Object) Hash(ctx context.Context, ty hash.Type) (string, error) {
	if ty != hash.SHA1 {
		return "", hash.ErrUnsupported
	}
	if !o.hasMedia {
		return "", nil
	}
	if o.binDetails != nil && o.binDetails.SHA1 != "" {
		return o.binDetails.SHA1, nil
	}
	return o.media.SHA1Hash, nil
}

func (o *Object) Open(ctx context.Context, options ...fs.OpenOption) (io.ReadCloser, error) {
	if !o.hasMedia || o.media.MediaKey == "" {
		return nil, errors.New("no media key available")
	}

	if o.binDetails != nil && o.binDetails.ChunkDetails != nil {
		chunks, err := o.fs.cache.GetChunksByBinaryName(o.binDetails.Name)
		if err == nil && len(chunks) > 0 {
			return newChunkedStreamReader(ctx, o.fs, chunks, o.binDetails, options)
		}
	}

	var stream io.ReadCloser

	if o.fs.dlCache == nil {
		downloadURL, err := o.fs.api.GetDownloadURL(ctx, o.media.MediaKey)
		if err != nil {
			return nil, fmt.Errorf("failed to get download URL: %w", err)
		}
		body, err := o.fs.api.DownloadFile(ctx, downloadURL, options...)
		if err != nil {
			return nil, fmt.Errorf("failed to download file: %w", err)
		}
		stream = body
	} else {
		entry, err := o.fs.dlCache.getOrStart(ctx, o.media.MediaKey, o.media.SizeBytes)
		if err != nil {
			return nil, fmt.Errorf("failed to start download: %w", err)
		}

		reader := &cachedReader{
			entry: entry,
			dc:    o.fs.dlCache,
		}

		for _, option := range options {
			switch opt := option.(type) {
			case *fs.RangeOption:
				if opt.Start >= 0 {
					reader.readPos = opt.Start
				}
			case *fs.SeekOption:
				reader.readPos = opt.Offset
			}
		}
		stream = reader
	}

	if o.binDetails != nil {
		return binary.DecodeStream(stream, o.media.FileName, o.binDetails.Size)
	}

	return stream, nil
}

func (o *Object) Update(ctx context.Context, in io.Reader, src fs.ObjectInfo, options ...fs.OpenOption) error {
	newObj, err := o.fs.Put(ctx, in, src, options...)
	if err != nil {
		return err
	}
	newO := newObj.(*Object)
	o.media = newO.media
	o.hasMedia = newO.hasMedia
	return nil
}

func (o *Object) Metadata(ctx context.Context) (fs.Metadata, error) {
	if !o.hasMedia {
		return nil, nil
	}
	m := fs.Metadata{}

	if o.media.Caption != "" {
		m["description"] = o.media.Caption
	}

	switch o.media.Type {
	case 1:
		m["media-type"] = "photo"
	case 2:
		m["media-type"] = "video"
	default:
		if o.media.Type != 0 {
			m["media-type"] = strconv.FormatInt(o.media.Type, 10)
		}
	}

	if o.media.Width > 0 {
		m["width"] = strconv.FormatInt(o.media.Width, 10)
	}
	if o.media.Height > 0 {
		m["height"] = strconv.FormatInt(o.media.Height, 10)
	}

	if o.media.Duration > 0 {
		m["duration"] = strconv.FormatInt(o.media.Duration, 10)
	}

	if o.media.Latitude != 0 || o.media.Longitude != 0 {
		m["latitude"] = strconv.FormatFloat(o.media.Latitude, 'f', -1, 64)
		m["longitude"] = strconv.FormatFloat(o.media.Longitude, 'f', -1, 64)
	}
	if o.media.LocationName != "" {
		m["location-name"] = o.media.LocationName
	}

	if o.media.Make != "" {
		m["camera-make"] = o.media.Make
	}
	if o.media.Model != "" {
		m["camera-model"] = o.media.Model
	}

	if o.media.Aperture != 0 {
		m["aperture"] = strconv.FormatFloat(o.media.Aperture, 'f', -1, 64)
	}
	if o.media.ShutterSpeed != 0 {
		m["shutter-speed"] = strconv.FormatFloat(o.media.ShutterSpeed, 'f', -1, 64)
	}
	if o.media.ISO > 0 {
		m["iso"] = strconv.FormatInt(o.media.ISO, 10)
	}
	if o.media.FocalLength != 0 {
		m["focal-length"] = strconv.FormatFloat(o.media.FocalLength, 'f', -1, 64)
	}

	if o.media.UTCTimestamp > 0 {
		m["taken-at"] = time.UnixMilli(o.media.UTCTimestamp).UTC().Format(time.RFC3339)
	}
	if o.media.ServerCreationTimestamp > 0 {
		m["uploaded-at"] = time.UnixMilli(o.media.ServerCreationTimestamp).UTC().Format(time.RFC3339)
	}

	m["is-favorite"] = strconv.FormatBool(o.media.IsFavorite)
	m["is-archived"] = strconv.FormatBool(o.media.IsArchived)

	if o.media.Origin != "" {
		m["origin"] = o.media.Origin
	}

	return m, nil
}

func (o *Object) SetMetadata(ctx context.Context, metadata fs.Metadata) error {
	if !o.hasMedia {
		return errors.New("no media info available")
	}
	if o.media.DedupKey == "" {
		return errors.New("no dedup key available for metadata update")
	}

	changed := false

	if description, ok := metadata["description"]; ok {
		err := o.fs.api.SetCaption(ctx, o.media.DedupKey, description)
		if err != nil {
			return fmt.Errorf("failed to set caption: %w", err)
		}
		o.media.Caption = description
		changed = true
	}

	if changed {
		o.fs.cacheMu.Lock()
		o.fs.forceSync = true
		o.fs.cacheMu.Unlock()
		_ = o.fs.ensureCachePopulated(ctx)
	}
	return nil
}

func (o *Object) Remove(ctx context.Context) error {
	if !o.hasMedia || o.media.DedupKey == "" {
		return errors.New("no dedup key available for trash operation")
	}

	isDescMode := strings.ToLower(o.fs.opt.ListMode) == "description_path" || strings.ToLower(o.fs.opt.ListMode) == "description"
	existingPaths, binDetails := parseCaption(o.media.Caption)
	var pathList []string

	if isDescMode && len(existingPaths) > 0 {
		currentDir := path.Dir(o.remote)
		if currentDir == "." || currentDir == "/" {
			currentDir = ""
		}

		targetPath := "/"
		if currentDir != "" {
			targetPath = fmt.Sprintf("/%s", currentDir)
		}

		for _, p := range existingPaths {
			if p != targetPath {
				pathList = append(pathList, p)
			}
		}
	}

	// If not description mode, or if it was description mode but no paths remain
	if !isDescMode || len(pathList) == 0 {
		var dedupKeys []string
		var mediaKeys []string

		if binDetails != nil && binDetails.ChunkDetails != nil {
			// Atomically trash all chunks belonging to this file
			if chunks, err := o.fs.cache.GetChunksByBinaryName(binDetails.Name); err == nil && len(chunks) > 0 {
				for _, ch := range chunks {
					if ch.DedupKey != "" {
						dedupKeys = append(dedupKeys, ch.DedupKey)
					}
					mediaKeys = append(mediaKeys, ch.MediaKey)
				}
			}
		}

		if len(dedupKeys) == 0 {
			dedupKeys = []string{o.media.DedupKey}
			mediaKeys = []string{o.media.MediaKey}
		}

		err := o.fs.api.MoveToTrash(ctx, dedupKeys)
		if err != nil {
			return err
		}
		_ = o.fs.cache.DeleteItems(mediaKeys)
	} else {
		// Just update the caption because it still exists in other folders
		if binDetails != nil && binDetails.ChunkDetails != nil {
			// Update caption across ALL chunks belonging to this file
			if chunks, err := o.fs.cache.GetChunksByBinaryName(binDetails.Name); err == nil && len(chunks) > 0 {
				for _, ch := range chunks {
					_, chBin := parseCaption(ch.Caption)
					newChCaption := buildCaption(pathList, chBin)
					if newChCaption != ch.Caption && ch.DedupKey != "" {
						_ = o.fs.api.SetCaption(ctx, ch.DedupKey, newChCaption)
						ch.Caption = newChCaption
						_ = o.fs.cache.UpsertItems([]api.MediaItem{ch})
					}
				}
			}
		} else {
			newCaption := buildCaption(pathList, binDetails)

			if newCaption != o.media.Caption {
				err := o.fs.api.SetCaption(ctx, o.media.DedupKey, newCaption)
				if err != nil {
					return fmt.Errorf("failed to update description path on remove: %w", err)
				}
				o.media.Caption = newCaption
				_ = o.fs.cache.UpsertItems([]api.MediaItem{o.media})
			}
		}
	}

	o.fs.cacheMu.Lock()
	o.fs.forceSync = true
	o.fs.cacheMu.Unlock()
	_ = o.fs.ensureCachePopulated(ctx)
	return nil
}

func (o *Object) ID() string {
	return o.media.MediaKey
}

var (
	_ fs.Fs            = (*Fs)(nil)
	_ fs.Abouter       = (*Fs)(nil)
	_ fs.Commander     = (*Fs)(nil)
	_ fs.Shutdowner    = (*Fs)(nil)
	_ fs.Object        = (*Object)(nil)
	_ fs.IDer          = (*Object)(nil)
	_ fs.Metadataer    = (*Object)(nil)
	_ fs.SetMetadataer = (*Object)(nil)
)
