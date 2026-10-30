package binary

import (
	"crypto/sha1"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// EncodedMedia represents a prepared media file ready for upload to Google Photos.
type EncodedMedia struct {
	File          *os.File
	MediaFilename string
	MediaSize     int64
	MediaSHA1B64  string
	Details       BinaryDetails
}

// Close cleans up and removes the temporary media file.
func (em *EncodedMedia) Close() error {
	if em.File != nil {
		path := em.File.Name()
		_ = em.File.Close()
		_ = os.Remove(path)
		em.File = nil
	}
	return nil
}

// Encode converts arbitrary binary data from src into an uncompressed PNG or MKV media file on disk.
func Encode(src io.Reader, filename string, fileSize int64, opts ...CodecOptions) (*EncodedMedia, error) {
	if fileSize > MaxFileBytes {
		return nil, fmt.Errorf("file size %d exceeds 50 GB maximum supported binary limit", fileSize)
	}

	opt := CodecOptions{}
	if len(opts) > 0 {
		opt = opts[0]
	}
	minVideoSize := opt.MinVideoSize
	if minVideoSize <= 0 {
		minVideoSize = MaxImageBytes
	}

	tmpFile, err := os.CreateTemp("", "rclone-b2m-*.tmp")
	if err != nil {
		return nil, fmt.Errorf("failed to create temp file for binary encoding: %w", err)
	}

	// Calculate SHA-1 of the original binary data while reading
	origSHA1 := sha1.New()
	teeSrc := io.TeeReader(src, origSHA1)

	baseName := filepath.Base(filename)
	var mediaFilename string

	if fileSize < minVideoSize {
		// Small file (< minVideoSize) -> PNG image
		mediaFilename = baseName + ".png"
		if err := EncodePNG(tmpFile, teeSrc, fileSize); err != nil {
			_ = tmpFile.Close()
			_ = os.Remove(tmpFile.Name())
			return nil, fmt.Errorf("failed to encode PNG: %w", err)
		}
	} else {
		// Large file (>= minVideoSize up to 50 GB) -> MKV video
		mediaFilename = baseName + ".mkv"
		res := SelectVideoResolution(fileSize, opt.VideoResolution)
		if err := EncodeMKV(tmpFile, teeSrc, fileSize, res); err != nil {
			_ = tmpFile.Close()
			_ = os.Remove(tmpFile.Name())
			return nil, fmt.Errorf("failed to encode MKV: %w", err)
		}
	}

	origSHA1Hex := hex.EncodeToString(origSHA1.Sum(nil))

	// Get size of the generated media file
	fi, err := tmpFile.Stat()
	if err != nil {
		_ = tmpFile.Close()
		_ = os.Remove(tmpFile.Name())
		return nil, err
	}
	mediaSize := fi.Size()

	// Calculate SHA1 of the generated media file for Google Photos upload token
	if _, err := tmpFile.Seek(0, io.SeekStart); err != nil {
		_ = tmpFile.Close()
		_ = os.Remove(tmpFile.Name())
		return nil, err
	}
	mediaSHA1 := sha1.New()
	if _, err := io.Copy(mediaSHA1, tmpFile); err != nil {
		_ = tmpFile.Close()
		_ = os.Remove(tmpFile.Name())
		return nil, err
	}
	mediaSHA1B64 := base64.StdEncoding.EncodeToString(mediaSHA1.Sum(nil))

	// Reset file pointer for uploading
	if _, err := tmpFile.Seek(0, io.SeekStart); err != nil {
		_ = tmpFile.Close()
		_ = os.Remove(tmpFile.Name())
		return nil, err
	}

	return &EncodedMedia{
		File:          tmpFile,
		MediaFilename: mediaFilename,
		MediaSize:     mediaSize,
		MediaSHA1B64:  mediaSHA1B64,
		Details: BinaryDetails{
			Name: baseName,
			Size: fileSize,
			SHA1: origSHA1Hex,
		},
	}, nil
}

// EncodeChunk converts a chunk of arbitrary binary data into a standalone PNG or MKV media file.
func EncodeChunk(src io.Reader, baseName string, chunkIndex, totalChunks int, thisChunkSize, offset, totalFileSize int64, totalSHA1 string, opts ...CodecOptions) (*EncodedMedia, error) {
	tmpFile, err := os.CreateTemp("", fmt.Sprintf("rclone-b2m-chunk-%d-*.tmp", chunkIndex))
	if err != nil {
		return nil, fmt.Errorf("failed to create temp file for chunk: %w", err)
	}

	opt := CodecOptions{}
	if len(opts) > 0 {
		opt = opts[0]
	}
	minVideoSize := opt.MinVideoSize
	if minVideoSize <= 0 {
		minVideoSize = MaxImageBytes
	}

	chunkPrefix := fmt.Sprintf("%s.part%03d", baseName, chunkIndex)
	var mediaFilename string

	if thisChunkSize < minVideoSize {
		mediaFilename = chunkPrefix + ".png"
		if err := EncodePNG(tmpFile, src, thisChunkSize); err != nil {
			_ = tmpFile.Close()
			_ = os.Remove(tmpFile.Name())
			return nil, fmt.Errorf("failed to encode chunk PNG: %w", err)
		}
	} else {
		mediaFilename = chunkPrefix + ".mkv"
		res := SelectVideoResolution(thisChunkSize, opt.VideoResolution)
		if err := EncodeMKV(tmpFile, src, thisChunkSize, res); err != nil {
			_ = tmpFile.Close()
			_ = os.Remove(tmpFile.Name())
			return nil, fmt.Errorf("failed to encode chunk MKV: %w", err)
		}
	}

	fi, err := tmpFile.Stat()
	if err != nil {
		_ = tmpFile.Close()
		_ = os.Remove(tmpFile.Name())
		return nil, err
	}
	mediaSize := fi.Size()

	if _, err := tmpFile.Seek(0, io.SeekStart); err != nil {
		_ = tmpFile.Close()
		_ = os.Remove(tmpFile.Name())
		return nil, err
	}
	mediaSHA1 := sha1.New()
	if _, err := io.Copy(mediaSHA1, tmpFile); err != nil {
		_ = tmpFile.Close()
		_ = os.Remove(tmpFile.Name())
		return nil, err
	}
	mediaSHA1B64 := base64.StdEncoding.EncodeToString(mediaSHA1.Sum(nil))

	if _, err := tmpFile.Seek(0, io.SeekStart); err != nil {
		_ = tmpFile.Close()
		_ = os.Remove(tmpFile.Name())
		return nil, err
	}

	return &EncodedMedia{
		File:          tmpFile,
		MediaFilename: mediaFilename,
		MediaSize:     mediaSize,
		MediaSHA1B64:  mediaSHA1B64,
		Details: BinaryDetails{
			Name: baseName,
			Size: totalFileSize,
			SHA1: totalSHA1,
			ChunkDetails: &ChunkDetails{
				ChunkIndex:  chunkIndex,
				TotalChunks: totalChunks,
				ChunkSize:   thisChunkSize,
				Offset:      offset,
			},
		},
	}, nil
}

// DecodeStream returns an io.ReadCloser that decodes a downloaded media stream into the original binary bytes.
func DecodeStream(mediaStream io.ReadCloser, filename string, originalSize int64) (io.ReadCloser, error) {
	ext := strings.ToLower(filepath.Ext(filename))
	var (
		decoder io.ReadCloser
		err     error
	)
	if ext == ".png" {
		decoder, err = NewPNGDecoder(mediaStream, originalSize)
	} else {
		// Default to MKV decoder
		decoder, err = NewMKVDecoder(mediaStream, originalSize)
	}
	if err != nil {
		_ = mediaStream.Close()
		return nil, err
	}
	return &decodedStreamReader{decoder: decoder, src: mediaStream}, nil
}

type decodedStreamReader struct {
	decoder io.ReadCloser
	src     io.Closer
}

func (r *decodedStreamReader) Read(p []byte) (int, error) {
	return r.decoder.Read(p)
}

func (r *decodedStreamReader) Close() error {
	err := r.decoder.Close()
	if r.src != nil {
		if closeErr := r.src.Close(); err == nil {
			err = closeErr
		}
	}
	return err
}
