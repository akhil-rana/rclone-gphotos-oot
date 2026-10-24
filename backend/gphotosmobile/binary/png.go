package binary

import (
	"bytes"
	"compress/zlib"
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"math"
)

var pngMagic = []byte("\x89PNG\r\n\x1a\n")

// CalculatePNGDimensions calculates width and height for a given file size.
// Minimum resolution is 240x240 pixels.
func CalculatePNGDimensions(fileSize int64) (width int, height int) {
	const minDim = 240
	pixelsNeeded := (fileSize + 2) / 3
	minPixels := int64(minDim * minDim)

	if pixelsNeeded <= minPixels {
		return minDim, minDim
	}

	w := int(math.Ceil(math.Sqrt(float64(pixelsNeeded))))
	if w < minDim {
		w = minDim
	}
	h := int((pixelsNeeded + int64(w) - 1) / int64(w))
	if h < minDim {
		h = minDim
	}

	return w, h
}

// EncodePNG streams binary data from src into dst as an uncompressed RGB24 PNG image.
func EncodePNG(dst io.Writer, src io.Reader, fileSize int64) error {
	w, h := CalculatePNGDimensions(fileSize)

	// 1. Write PNG Signature
	if _, err := dst.Write(pngMagic); err != nil {
		return err
	}

	// 2. Write IHDR Chunk
	ihdrData := make([]byte, 13)
	binary.BigEndian.PutUint32(ihdrData[0:4], uint32(w))
	binary.BigEndian.PutUint32(ihdrData[4:8], uint32(h))
	ihdrData[8] = 8 // Bit depth: 8 bits per channel
	ihdrData[9] = 2 // Color type: 2 (Truecolor RGB)
	ihdrData[10] = 0 // Compression method: Deflate
	ihdrData[11] = 0 // Filter method: Standard
	ihdrData[12] = 0 // Interlace: None
	if err := writeChunk(dst, "IHDR", ihdrData); err != nil {
		return err
	}

	// 3. Write IDAT Chunk (zlib Deflate stream)
	// We use an io.Pipe to stream scanlines through zlib without loading everything in memory.
	pr, pw := io.Pipe()
	go func() {
		defer pw.Close()
		zw, err := zlib.NewWriterLevel(pw, zlib.NoCompression)
		if err != nil {
			_ = pw.CloseWithError(err)
			return
		}
		defer zw.Close()

		rowBytes := w * 3
		rowBuf := make([]byte, rowBytes)
		filterByte := []byte{0} // Filter type 0: None

		for row := 0; row < h; row++ {
			// Write filter byte for this scanline
			if _, err := zw.Write(filterByte); err != nil {
				_ = pw.CloseWithError(err)
				return
			}

			// Read up to rowBytes from src
			n, err := io.ReadFull(src, rowBuf)
			if err != nil && err != io.EOF && err != io.ErrUnexpectedEOF {
				_ = pw.CloseWithError(err)
				return
			}

			// Pad with null bytes if src is exhausted
			if n < rowBytes {
				for i := n; i < rowBytes; i++ {
					rowBuf[i] = 0
				}
			}

			if _, err := zw.Write(rowBuf); err != nil {
				_ = pw.CloseWithError(err)
				return
			}
		}
	}()

	// Write compressed stream inside IDAT chunk(s)
	// Read in 64KB blocks to keep chunk sizes standard
	buf := make([]byte, 64*1024)
	for {
		n, err := pr.Read(buf)
		if n > 0 {
			if wErr := writeChunk(dst, "IDAT", buf[:n]); wErr != nil {
				return wErr
			}
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
	}

	// 4. Write IEND Chunk
	return writeChunk(dst, "IEND", nil)
}

func writeChunk(w io.Writer, chunkType string, data []byte) error {
	length := uint32(len(data))
	if err := binary.Write(w, binary.BigEndian, length); err != nil {
		return err
	}

	typeBytes := []byte(chunkType)
	if _, err := w.Write(typeBytes); err != nil {
		return err
	}

	if length > 0 {
		if _, err := w.Write(data); err != nil {
			return err
		}
	}

	crc := crc32.NewIEEE()
	crc.Write(typeBytes)
	if length > 0 {
		crc.Write(data)
	}

	return binary.Write(w, binary.BigEndian, crc.Sum32())
}

// PNGDecoder reads an uncompressed RGB24 PNG and extracts the original binary data.
type PNGDecoder struct {
	r          io.Reader
	zr         io.ReadCloser
	width      int
	height     int
	rowBytes   int
	remBytes   int64
	rowBuf     []byte
	rowOffset  int
	rowAvail   int
	readFilter bool
	closed     bool
}

// NewPNGDecoder constructs a streaming decoder from an input PNG stream and the exact expected binary size.
func NewPNGDecoder(src io.Reader, originalSize int64) (*PNGDecoder, error) {
	// Read and verify PNG signature
	sig := make([]byte, 8)
	if _, err := io.ReadFull(src, sig); err != nil {
		return nil, fmt.Errorf("failed to read PNG signature: %w", err)
	}
	if !bytes.Equal(sig, pngMagic) {
		return nil, errors.New("invalid PNG signature")
	}

	// Collect IDAT streams and read IHDR
	var width, height int
	idatPipeReader, idatPipeWriter := io.Pipe()

	go func() {
		defer idatPipeWriter.Close()
		for {
			var length uint32
			if err := binary.Read(src, binary.BigEndian, &length); err != nil {
				_ = idatPipeWriter.CloseWithError(err)
				return
			}

			chunkType := make([]byte, 4)
			if _, err := io.ReadFull(src, chunkType); err != nil {
				_ = idatPipeWriter.CloseWithError(err)
				return
			}

			if string(chunkType) == "IHDR" {
				ihdr := make([]byte, length)
				if _, err := io.ReadFull(src, ihdr); err != nil {
					_ = idatPipeWriter.CloseWithError(err)
					return
				}
				width = int(binary.BigEndian.Uint32(ihdr[0:4]))
				height = int(binary.BigEndian.Uint32(ihdr[4:8]))
			} else if string(chunkType) == "IDAT" {
				if _, err := io.CopyN(idatPipeWriter, src, int64(length)); err != nil {
					_ = idatPipeWriter.CloseWithError(err)
					return
				}
			} else if string(chunkType) == "IEND" {
				return
			} else {
				// Skip other chunks
				if _, err := io.CopyN(io.Discard, src, int64(length)); err != nil {
					_ = idatPipeWriter.CloseWithError(err)
					return
				}
			}

			// Discard CRC32
			var crc uint32
			if err := binary.Read(src, binary.BigEndian, &crc); err != nil {
				_ = idatPipeWriter.CloseWithError(err)
				return
			}
		}
	}()

	zr, err := zlib.NewReader(idatPipeReader)
	if err != nil {
		return nil, fmt.Errorf("failed to initialize zlib reader: %w", err)
	}

	return &PNGDecoder{
		zr:         zr,
		width:      width,
		height:     height,
		rowBytes:   width * 3,
		remBytes:   originalSize,
		rowBuf:     make([]byte, width*3),
		readFilter: true,
	}, nil
}

func (d *PNGDecoder) Read(p []byte) (int, error) {
	if d.remBytes <= 0 {
		return 0, io.EOF
	}

	bytesWritten := 0
	for bytesWritten < len(p) && d.remBytes > 0 {
		// If we need data from a new row
		if d.rowOffset >= d.rowAvail {
			// Read 1-byte filter
			filter := make([]byte, 1)
			if _, err := io.ReadFull(d.zr, filter); err != nil {
				if err == io.EOF && d.remBytes <= 0 {
					return bytesWritten, io.EOF
				}
				return bytesWritten, err
			}

			// Read row data
			n, err := io.ReadFull(d.zr, d.rowBuf)
			if err != nil && err != io.EOF && err != io.ErrUnexpectedEOF {
				return bytesWritten, err
			}
			d.rowAvail = n
			d.rowOffset = 0
			if d.rowAvail == 0 {
				return bytesWritten, io.EOF
			}
		}

		toCopy := d.rowAvail - d.rowOffset
		if int64(toCopy) > d.remBytes {
			toCopy = int(d.remBytes)
		}
		if toCopy > len(p)-bytesWritten {
			toCopy = len(p) - bytesWritten
		}

		copy(p[bytesWritten:], d.rowBuf[d.rowOffset:d.rowOffset+toCopy])
		d.rowOffset += toCopy
		bytesWritten += toCopy
		d.remBytes -= int64(toCopy)
	}

	if bytesWritten == 0 && d.remBytes <= 0 {
		return 0, io.EOF
	}
	return bytesWritten, nil
}

func (d *PNGDecoder) Close() error {
	if d.closed {
		return nil
	}
	d.closed = true
	if d.zr != nil {
		return d.zr.Close()
	}
	return nil
}
