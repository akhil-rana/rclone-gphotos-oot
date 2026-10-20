package gphotosmobile

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"path"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/rclone/rclone/fs"
	"github.com/rclone/rclone/fs/config"
	"github.com/rclone/rclone/fs/config/configmap"
	"github.com/rclone/rclone/fs/config/configstruct"
	"github.com/rclone/rclone/fs/hash"
	"github.com/rclone/rclone/lib/atexit"
	"github.com/rclone/rclone/lib/encoder"
	"github.com/user/rclone-gphotos-oot/backend/gphotosmobile/api"

	_ "embed"
)

var (
	errCantRmdir = errors.New("can't remove virtual directory")
)

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

//go:embed gphotosmobile.md
var gphotosmobileMD string

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
			Help:    "Format to list files (e.g., flat, calendar, description_path).",
			Default: "flat",
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
	AuthData        string               `config:"auth_data"`
	CacheDBPath     string               `config:"cache_db_path"`
	DeviceModel     string               `config:"device_model"`
	DeviceMake      string               `config:"device_make"`
	UnlimitedUpload bool                 `config:"unlimited_upload"`
	ListMode        string               `config:"list_mode"`
	APKVersion      int64                `config:"apk_version"`
	AndroidAPI      int64                `config:"android_api"`
	DownloadCache   bool                 `config:"download_cache"`
	Enc             encoder.MultiEncoder `config:"encoding"`
}

// Fs represents a remote Google Photos storage via mobile API
type Fs struct {
	name      string          // name of this remote
	root      string          // the path we are working on
	opt       Options         // parsed options
	features  *fs.Features    // optional features
	api       *MobileAPI      // mobile API client (includes pacer for rate limiting/retry)
	cache     *Cache          // SQLite cache
	cacheMu   sync.Mutex      // protects cache operations
	startTime time.Time       // time Fs was started
	dlCache   *downloadCache  // shared download cache for temp files
	dlExit    atexit.FnHandle // removes download cache temp files at exit
	forceSync bool            // force next sync regardless of throttle
}

type Object struct {
	fs       *Fs           // parent Fs
	remote   string        // remote path
	media    api.MediaItem // cached media item info
	hasMedia bool          // whether media info is populated
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

	f := &Fs{
		name:      name,
		root:      root,
		opt:       *opt,
		api:       api,
		cache:     cache,
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

	switch strings.ToLower(f.opt.ListMode) {
	case "year":
		itemPaths = append(itemPaths, fmt.Sprintf("%04d/%s", tm.Year(), fileName))
	case "month", "year_month":
		itemPaths = append(itemPaths, fmt.Sprintf("%04d/%04d-%02d/%s", tm.Year(), tm.Year(), tm.Month(), fileName))
	case "calendar", "calender", "day", "week":
		itemPaths = append(itemPaths, fmt.Sprintf("%04d/%04d-%02d/%04d-%02d-%02d/%s", tm.Year(), tm.Year(), tm.Month(), tm.Year(), tm.Month(), tm.Day(), fileName))
	case "description_path", "description":
		pathsStr := ""
		idx := strings.Index(item.Caption, "{paths:[")
		if idx != -1 {
			endIdx := strings.Index(item.Caption[idx:], "]}")
			if endIdx != -1 {
				pathsStr = item.Caption[idx+len("{paths:[") : idx+endIdx]
			}
		} else {
			idx = strings.Index(item.Caption, "[paths:")
			if idx != -1 {
				endIdx := strings.Index(item.Caption[idx:], "]")
				if endIdx != -1 {
					pathsStr = item.Caption[idx+len("[paths:") : idx+endIdx]
				}
			} else {
				idx = strings.Index(item.Caption, "[path:")
				if idx != -1 {
					endIdx := strings.Index(item.Caption[idx:], "]")
					if endIdx != -1 {
						pathsStr = item.Caption[idx+len("[path:") : idx+endIdx]
					}
				}
			}
		}

		if pathsStr != "" {
			parts := strings.Split(pathsStr, ",")
			for _, p := range parts {
				p = strings.TrimSpace(p)
				p = strings.TrimPrefix(p, "\"")
				p = strings.TrimSuffix(p, "\"")
				p = strings.TrimSpace(p)
				p = strings.TrimPrefix(p, "/")
				p = strings.TrimSuffix(p, "/")
				if p != "" {
					itemPaths = append(itemPaths, path.Join(p, fileName))
				} else {
					itemPaths = append(itemPaths, fileName)
				}
			}
		} else {
			itemPaths = append(itemPaths, fileName)
		}
	default: // "flat"
		itemPaths = append(itemPaths, fileName)
	}

	// Apply f.root filtering if necessary, but actually for NewObject we might not need to if `remote` is already relative to f.root.
	// Wait, `remote` in NewObject is relative to `f.root`.
	// So we should return paths relative to `f.root`.
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
		item, err = f.findByDedupSuffix(fileName)
		if err != nil {
			return nil, fs.ErrorObjectNotFound
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

	return &Object{
		fs:       f,
		remote:   remote,
		media:    *item,
		hasMedia: true,
	}, nil
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
	if err == nil {
		return item, nil
	}
	item, err = f.cache.GetByMediaKey(suffix)
	if err == nil {
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

	isFlatMode := strings.ToLower(f.opt.ListMode) == "flat" || f.opt.ListMode == ""

	nameCount := make(map[string]int)
	if isFlatMode {
		for _, item := range items {
			if item.FileName != "" {
				nameCount[item.FileName]++
			}
		}
	}

	dirs := make(map[string]bool)

	for _, item := range items {
		if item.FileName == "" {
			continue
		}

		fileName := item.FileName
		if isFlatMode && nameCount[fileName] > 1 {
			ext := path.Ext(fileName)
			base := fileName[:len(fileName)-len(ext)]
			suffix := item.DedupKey
			if suffix == "" {
				suffix = item.MediaKey
			}
			fileName = fmt.Sprintf("%s_%s%s", base, suffix, ext)
		}
		fileName = f.opt.Enc.FromStandardName(fileName)

		itemPaths := f.getVirtualPaths(&item, fileName)

		for _, itemPath := range itemPaths {

			// Now itemPath is relative to the current virtual filesystem root.
			// We check it against the requested `dir`.
			if dir == "" {
				if !strings.Contains(itemPath, "/") {
					o := &Object{fs: f, remote: itemPath, media: item, hasMedia: true}
					entries = append(entries, o)
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
						o := &Object{fs: f, remote: itemPath, media: item, hasMedia: true}
						entries = append(entries, o)
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
	remote := src.Remote()
	size := src.Size()

	hashStr, err := src.Hash(ctx, hash.SHA1)
	if err != nil {
		return nil, fmt.Errorf("failed to get SHA1 hash: %w", err)
	}

	sha1Bytes, err := hex.DecodeString(hashStr)
	if err != nil {
		return nil, fmt.Errorf("invalid SHA1 hash hex: %w", err)
	}
	sha1B64 := base64.StdEncoding.EncodeToString(sha1Bytes)

	mediaKey, err := f.api.FindRemoteMediaByHash(ctx, sha1Bytes)
	if err == nil && mediaKey != "" {
		fs.Debugf(f, "File %q already exists with media key %s", remote, mediaKey)
		_, _ = io.Copy(io.Discard, in)
	} else {
		uploadToken, err := f.api.GetUploadToken(ctx, sha1B64, size)
		if err != nil {
			return nil, fmt.Errorf("failed to get upload token: %w", err)
		}

		uploadResp, err := f.api.UploadFile(ctx, in, size, uploadToken)
		if err != nil {
			return nil, fmt.Errorf("upload failed: %w", err)
		}

		_, fileName := path.Split(remote)
		fileName = f.opt.Enc.ToStandardName(fileName)
		mediaKey, err = f.api.CommitUpload(ctx, uploadResp, fileName, sha1Bytes)
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
		FileName:  path.Base(remote),
		SizeBytes: size,
		DedupKey:  localDedupKey,
	}

	if cachedItem, err := f.cache.GetByDedupKey(localDedupKey); err == nil {
		media = *cachedItem
	} else if cachedItem, err := f.cache.GetByMediaKey(mediaKey); err == nil {
		media = *cachedItem
	}

	if strings.ToLower(f.opt.ListMode) == "description_path" || strings.ToLower(f.opt.ListMode) == "description" {
		if media.DedupKey != "" {
			fullPath := path.Join(f.root, remote)
			dir := path.Dir(fullPath)
			if dir == "." || dir == "/" {
				dir = ""
			}

			caption := media.Caption
			pathsStr := ""
			idx := strings.Index(caption, "{paths:[")
			if idx != -1 {
				endIdx := strings.Index(caption[idx:], "]}")
				if endIdx != -1 {
					pathsStr = caption[idx+len("{paths:[") : idx+endIdx]
					caption = caption[:idx] + caption[idx+endIdx+2:]
				}
			} else {
				idx = strings.Index(caption, "[paths:")
				if idx != -1 {
					endIdx := strings.Index(caption[idx:], "]")
					if endIdx != -1 {
						pathsStr = caption[idx+len("[paths:") : idx+endIdx]
						caption = caption[:idx] + caption[idx+endIdx+1:]
					}
				} else {
					idx = strings.Index(caption, "[path:")
					if idx != -1 {
						endIdx := strings.Index(caption[idx:], "]")
						if endIdx != -1 {
							pathsStr = caption[idx+len("[path:") : idx+endIdx]
							caption = caption[:idx] + caption[idx+endIdx+1:]
						}
					}
				}
			}

			caption = strings.TrimSpace(caption)

			pathList := []string{}
			if pathsStr != "" {
				parts := strings.Split(pathsStr, ",")
				for _, p := range parts {
					p = strings.TrimSpace(p)
					p = strings.TrimPrefix(p, "\"")
					p = strings.TrimSuffix(p, "\"")
					p = strings.TrimSpace(p)
					if p != "" {
						pathList = append(pathList, p)
					}
				}
			}

			newPath := "/"
			if dir != "" {
				newPath = fmt.Sprintf("/%s", dir)
			}

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

			if len(pathList) > 0 {
				if caption != "" {
					caption += " "
				}
				var quotedPaths []string
				for _, p := range pathList {
					quotedPaths = append(quotedPaths, fmt.Sprintf(`"%s"`, p))
				}
				caption += fmt.Sprintf("{paths:[%s]}", strings.Join(quotedPaths, ","))
			}

			if caption != media.Caption {
				err := f.api.SetCaption(ctx, media.DedupKey, caption)
				if err != nil {
					fs.Debugf(f, "Failed to set description path for %q: %v", remote, err)
				} else {
					media.Caption = caption
					_ = f.cache.UpsertItems([]api.MediaItem{media})
				}
			}
		}
	}

	return &Object{
		fs:       f,
		remote:   remote,
		media:    media,
		hasMedia: true,
	}, nil
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
	return o.media.SHA1Hash, nil
}

func (o *Object) Open(ctx context.Context, options ...fs.OpenOption) (io.ReadCloser, error) {
	if !o.hasMedia || o.media.MediaKey == "" {
		return nil, errors.New("no media key available")
	}

	if o.fs.dlCache == nil {
		downloadURL, err := o.fs.api.GetDownloadURL(ctx, o.media.MediaKey)
		if err != nil {
			return nil, fmt.Errorf("failed to get download URL: %w", err)
		}
		body, err := o.fs.api.DownloadFile(ctx, downloadURL)
		if err != nil {
			return nil, fmt.Errorf("failed to download file: %w", err)
		}
		return body, nil
	}

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

	return reader, nil
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
	var pathList []string
	var caption string

	if isDescMode {
		caption = o.media.Caption
		pathsStr := ""
		idx := strings.Index(caption, "{paths:[")
		if idx != -1 {
			endIdx := strings.Index(caption[idx:], "]}")
			if endIdx != -1 {
				pathsStr = caption[idx+len("{paths:[") : idx+endIdx]
				caption = caption[:idx] + caption[idx+endIdx+2:]
			}
		} else {
			idx = strings.Index(caption, "[paths:")
			if idx != -1 {
				endIdx := strings.Index(caption[idx:], "]")
				if endIdx != -1 {
					pathsStr = caption[idx+len("[paths:") : idx+endIdx]
					caption = caption[:idx] + caption[idx+endIdx+1:]
				}
			} else {
				idx = strings.Index(caption, "[path:")
				if idx != -1 {
					endIdx := strings.Index(caption[idx:], "]")
					if endIdx != -1 {
						pathsStr = caption[idx+len("[path:") : idx+endIdx]
						caption = caption[:idx] + caption[idx+endIdx+1:]
					}
				}
			}
		}

		caption = strings.TrimSpace(caption)

		if pathsStr != "" {
			parts := strings.Split(pathsStr, ",")
			for _, p := range parts {
				p = strings.TrimSpace(p)
				p = strings.TrimPrefix(p, "\"")
				p = strings.TrimSuffix(p, "\"")
				p = strings.TrimSpace(p)
				if p != "" {
					pathList = append(pathList, p)
				}
			}
		}

		// Find the path we are trying to remove
		currentDir := path.Dir(o.remote)
		if currentDir == "." || currentDir == "/" {
			currentDir = ""
		}

		targetPath := "/"
		if currentDir != "" {
			targetPath = fmt.Sprintf("/%s", currentDir)
		}

		// Remove the specific path from the array
		var newPathList []string
		for _, p := range pathList {
			if p != targetPath {
				newPathList = append(newPathList, p)
			}
		}
		pathList = newPathList
	}

	// If not description mode, or if it was description mode but no paths remain
	if !isDescMode || len(pathList) == 0 {
		err := o.fs.api.MoveToTrash(ctx, []string{o.media.DedupKey})
		if err != nil {
			return err
		}
	} else {
		// Just update the caption because it still exists in other folders
		if len(pathList) > 0 {
			if caption != "" {
				caption += " "
			}
			var quotedPaths []string
			for _, p := range pathList {
				quotedPaths = append(quotedPaths, fmt.Sprintf(`"%s"`, p))
			}
			caption += fmt.Sprintf("{paths:[%s]}", strings.Join(quotedPaths, ","))
		}

		if caption != o.media.Caption {
			err := o.fs.api.SetCaption(ctx, o.media.DedupKey, caption)
			if err != nil {
				return fmt.Errorf("failed to update description path on remove: %w", err)
			}
			o.media.Caption = caption
			_ = o.fs.cache.UpsertItems([]api.MediaItem{o.media})
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
