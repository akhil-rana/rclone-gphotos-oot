package binary

import (
	"path/filepath"
	"strings"
)

// MaxImageBytes defines the threshold where we transition from PNG image to MKV video (10 MB).
// Files < 10 MB are encoded as PNG; files >= 10 MB are encoded as MKV.
const MaxImageBytes = 10 * 1024 * 1024

// MaxFileBytes defines the maximum supported binary file size (50 GB).
const MaxFileBytes = 50 * 1024 * 1024 * 1024

// ChunkDetails stores chunking metadata when large binary files are split into parts.
type ChunkDetails struct {
	ChunkIndex  int   `json:"chunkIndex"`
	TotalChunks int   `json:"totalChunks"`
	ChunkSize   int64 `json:"chunkSize"`
	Offset      int64 `json:"offset"`
}

// BinaryDetails encapsulates the metadata stored in Google Photos caption JSON.
type BinaryDetails struct {
	Name         string        `json:"name"`
	Size         int64         `json:"size"`
	SHA1         string        `json:"sha1"`
	ChunkDetails *ChunkDetails `json:"chunkDetails,omitempty"`
}

// VideoResolution represents a standard video resolution candidate for encoding.
type VideoResolution struct {
	Name   string
	Width  int
	Height int
}

// Standard candidate video resolutions ranging from 240p to 4096p (DCI 4K).
var CandidateResolutions = []VideoResolution{
	{Name: "240p", Width: 426, Height: 240},
	{Name: "360p", Width: 640, Height: 360},
	{Name: "480p", Width: 854, Height: 480},
	{Name: "720p", Width: 1280, Height: 720},
	{Name: "1080p", Width: 1920, Height: 1080},
	{Name: "1440p", Width: 2560, Height: 1440},
	{Name: "4K", Width: 3840, Height: 2160},
	{Name: "2160p", Width: 3840, Height: 2160},
	{Name: "4096p", Width: 4096, Height: 2160},
}

// MinBytesFor1Sec returns the minimum payload bytes required to produce 1 second (24 frames) of video.
func (v VideoResolution) MinBytesFor1Sec() int64 {
	return int64(v.Width * v.Height * 3 * 24)
}

// LookupResolution finds a resolution by name (case-insensitive).
func LookupResolution(name string) (VideoResolution, bool) {
	name = strings.ToLower(strings.TrimSpace(name))
	for _, res := range CandidateResolutions {
		if strings.ToLower(res.Name) == name {
			return res, true
		}
	}
	return VideoResolution{}, false
}

// CodecOptions configures binary media encoding parameters.
type CodecOptions struct {
	MinVideoSize    int64  // File size threshold to encode as MKV rather than PNG (default 10 MB)
	VideoResolution string // "auto", "240p", "360p", "480p", "720p", "1080p", "1440p", "4K", "4096p"
}

// SelectVideoResolution evaluates candidate resolutions that guarantee at least 1 second
// of video (fileSize >= res.MinBytesFor1Sec()) and selects the one that minimizes trailing padding bytes.
func SelectVideoResolution(fileSize int64, requestedRes string) VideoResolution {
	requestedRes = strings.ToLower(strings.TrimSpace(requestedRes))
	if requestedRes != "" && requestedRes != "auto" {
		if res, ok := LookupResolution(requestedRes); ok {
			return res
		}
	}

	// In auto mode, only consider resolutions where fileSize produces at least 1 second of video
	var validCandidates []VideoResolution
	for _, res := range CandidateResolutions {
		if res.Name == "2160p" {
			continue
		}
		if fileSize >= res.MinBytesFor1Sec() {
			validCandidates = append(validCandidates, res)
		}
	}
	if len(validCandidates) == 0 {
		return CandidateResolutions[0] // 240p fallback
	}

	bestRes := validCandidates[0]
	minPadding := int64(-1)

	for _, res := range validCandidates {
		frameBytes := int64(res.Width * res.Height * 3)
		rem := fileSize % frameBytes
		padding := int64(0)
		if rem != 0 {
			padding = frameBytes - rem
		}

		if minPadding == -1 || padding < minPadding {
			minPadding = padding
			bestRes = res
		}
	}

	return bestRes
}

// IsNativeMedia checks whether a file is already a recognized photo or video format.
func IsNativeMedia(filename string) bool {
	ext := strings.ToLower(filepath.Ext(filename))
	switch ext {
	case ".jpg", ".jpeg", ".png", ".gif", ".webp", ".bmp", ".heic", ".heif", ".dng", ".cr2", ".nef",
		".mp4", ".mov", ".mkv", ".avi", ".webm", ".m4v", ".3gp", ".wmv":
		return true
	default:
		return false
	}
}
