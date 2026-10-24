package gphotosmobile

import (
	"context"
	"fmt"
	"io"

	"github.com/rclone/rclone/fs"
	"github.com/user/rclone-gphotos-oot/backend/gphotosmobile/api"
	"github.com/user/rclone-gphotos-oot/backend/gphotosmobile/binary"
)

// chunkedStreamReader streams across multiple media chunks on demand with range seeking.
type chunkedStreamReader struct {
	ctx          context.Context
	fs           *Fs
	chunks       []api.MediaItem
	currentIdx   int
	currentStream io.ReadCloser
	bytesLeft    int64
	closed       bool
}

// newChunkedStreamReader initializes a reader across chunked media items.
func newChunkedStreamReader(ctx context.Context, f *Fs, chunks []api.MediaItem, binDetails *binary.BinaryDetails, options []fs.OpenOption) (io.ReadCloser, error) {
	if len(chunks) == 0 {
		return nil, fmt.Errorf("no chunks available for %s", binDetails.Name)
	}

	// Deduplicate chunks strictly by chunkIndex, preferring canonical / most recent item
	chunksByIndex := make(map[int]api.MediaItem)
	for _, ch := range chunks {
		_, bin := parseCaption(ch.Caption)
		if bin != nil && bin.ChunkDetails != nil {
			idx := bin.ChunkDetails.ChunkIndex
			if existing, ok := chunksByIndex[idx]; !ok {
				chunksByIndex[idx] = ch
			} else if ch.IsCanonical && !existing.IsCanonical {
				chunksByIndex[idx] = ch
			} else if ch.UTCTimestamp > existing.UTCTimestamp {
				chunksByIndex[idx] = ch
			}
		}
	}

	totalChunks := binDetails.ChunkDetails.TotalChunks
	if totalChunks <= 0 {
		totalChunks = len(chunksByIndex)
	}

	orderedItems := make([]api.MediaItem, totalChunks)
	for i := 0; i < totalChunks; i++ {
		item, ok := chunksByIndex[i]
		if !ok {
			return nil, fmt.Errorf("missing chunk %d of %d for %s", i, totalChunks, binDetails.Name)
		}
		orderedItems[i] = item
	}

	// Calculate start offset and total bytes to read
	standardChunkSize := int64(f.opt.BinaryDataChunkSize)
	if len(orderedItems) > 0 {
		_, chunk0Bin := parseCaption(orderedItems[0].Caption)
		if chunk0Bin != nil && chunk0Bin.ChunkDetails != nil && chunk0Bin.ChunkDetails.ChunkSize > 0 {
			standardChunkSize = chunk0Bin.ChunkDetails.ChunkSize
		}
	}
	if standardChunkSize <= 0 {
		standardChunkSize = 64 * 1024 * 1024
	}

	startOffset := int64(0)
	bytesToRead := binDetails.Size

	for _, option := range options {
		switch opt := option.(type) {
		case *fs.RangeOption:
			if opt.Start >= 0 {
				startOffset = opt.Start
				if opt.End >= opt.Start {
					bytesToRead = opt.End - opt.Start + 1
				} else {
					bytesToRead = binDetails.Size - startOffset
				}
			}
		case *fs.SeekOption:
			if opt.Offset >= 0 {
				startOffset = opt.Offset
				bytesToRead = binDetails.Size - startOffset
			}
		}
	}

	if startOffset >= binDetails.Size {
		return io.NopCloser(nil), io.EOF
	}

	startChunkIdx := int(startOffset / standardChunkSize)
	if startChunkIdx >= len(orderedItems) {
		startChunkIdx = len(orderedItems) - 1
	}
	skipInFirstChunk := startOffset % standardChunkSize

	csr := &chunkedStreamReader{
		ctx:        ctx,
		fs:         f,
		chunks:     orderedItems,
		currentIdx: startChunkIdx,
		bytesLeft:  bytesToRead,
	}

	// Open the starting chunk
	if err := csr.openCurrentChunk(); err != nil {
		return nil, err
	}

	// Discard prefix bytes within the starting chunk
	if skipInFirstChunk > 0 {
		if _, err := io.CopyN(io.Discard, csr.currentStream, skipInFirstChunk); err != nil {
			_ = csr.Close()
			return nil, fmt.Errorf("failed to seek within initial chunk: %w", err)
		}
	}

	return csr, nil
}

func (r *chunkedStreamReader) openCurrentChunk() error {
	if r.currentIdx >= len(r.chunks) {
		return io.EOF
	}

	item := r.chunks[r.currentIdx]
	_, bin := parseCaption(item.Caption)
	chunkOriginalSize := int64(0)
	if bin != nil && bin.ChunkDetails != nil {
		chunkOriginalSize = bin.ChunkDetails.ChunkSize
	} else {
		chunkOriginalSize = item.SizeBytes
	}

	downloadURL, err := r.fs.api.GetDownloadURL(r.ctx, item.MediaKey)
	if err != nil {
		return fmt.Errorf("failed to get download URL for chunk %d: %w", r.currentIdx, err)
	}

	rawStream, err := r.fs.api.DownloadFile(r.ctx, downloadURL)
	if err != nil {
		return fmt.Errorf("failed to download chunk %d: %w", r.currentIdx, err)
	}

	decodedStream, err := binary.DecodeStream(rawStream, item.FileName, chunkOriginalSize)
	if err != nil {
		_ = rawStream.Close()
		return fmt.Errorf("failed to decode chunk %d stream: %w", r.currentIdx, err)
	}

	r.currentStream = decodedStream
	return nil
}

func (r *chunkedStreamReader) Read(p []byte) (int, error) {
	if r.closed || r.bytesLeft <= 0 {
		return 0, io.EOF
	}

	toRead := len(p)
	if int64(toRead) > r.bytesLeft {
		toRead = int(r.bytesLeft)
	}

	for {
		if r.currentStream == nil {
			if err := r.openCurrentChunk(); err != nil {
				return 0, err
			}
		}

		n, err := r.currentStream.Read(p[:toRead])
		if n > 0 {
			r.bytesLeft -= int64(n)
			return n, nil
		}

		if err == io.EOF {
			_ = r.currentStream.Close()
			r.currentStream = nil
			r.currentIdx++
			if r.currentIdx >= len(r.chunks) {
				return 0, io.EOF
			}
			continue
		}

		if err != nil {
			return 0, err
		}
	}
}

func (r *chunkedStreamReader) Close() error {
	if r.closed {
		return nil
	}
	r.closed = true
	if r.currentStream != nil {
		return r.currentStream.Close()
	}
	return nil
}
