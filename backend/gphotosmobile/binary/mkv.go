package binary

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
)

// Matroska / EBML Element IDs
var (
	idEBML            = []byte{0x1A, 0x45, 0xDF, 0xA3}
	idEBMLVersion     = []byte{0x42, 0x86}
	idEBMLReadVersion = []byte{0x42, 0xF7}
	idEBMLMaxIDLen    = []byte{0x42, 0xF2}
	idEBMLMaxSizeLen  = []byte{0x42, 0xF3}
	idDocType         = []byte{0x42, 0x82}
	idDocTypeVersion  = []byte{0x42, 0x87}
	idDocTypeReadVer  = []byte{0x42, 0x85}

	idSegment        = []byte{0x18, 0x53, 0x80, 0x67}
	idInfo           = []byte{0x15, 0x49, 0xA9, 0x66}
	idTimestampScale = []byte{0x2A, 0xD7, 0xB1}
	idMuxingApp      = []byte{0x4D, 0x80}
	idWritingApp     = []byte{0x57, 0x41}

	idTracks        = []byte{0x16, 0x54, 0xAE, 0x6B}
	idTrackEntry    = []byte{0xAE}
	idTrackNumber   = []byte{0xD7}
	idTrackUID      = []byte{0x73, 0xC5}
	idTrackType     = []byte{0x83}
	idFlagLacing    = []byte{0x9C}
	idCodecID       = []byte{0x86}
	idCodecPrivate  = []byte{0x63, 0xA2}
	idVideo         = []byte{0xE0}
	idPixelWidth    = []byte{0xB0}
	idPixelHeight   = []byte{0xBA}

	idCluster     = []byte{0x1F, 0x43, 0xB6, 0x75}
	idTimestamp   = []byte{0xE7}
	idSimpleBlock = []byte{0xA3}
)

// encodeVINT encodes an integer as an EBML Variable Size Integer.
func encodeVINT(val uint64) []byte {
	switch {
	case val < 0x7F:
		return []byte{byte(val | 0x80)}
	case val < 0x3FFF:
		return []byte{byte((val >> 8) | 0x40), byte(val & 0xFF)}
	case val < 0x1FFFFF:
		return []byte{byte((val >> 16) | 0x20), byte((val >> 8) & 0xFF), byte(val & 0xFF)}
	case val < 0x0FFFFFFF:
		return []byte{byte((val >> 24) | 0x10), byte((val >> 16) & 0xFF), byte((val >> 8) & 0xFF), byte(val & 0xFF)}
	default:
		buf := make([]byte, 8)
		binary.BigEndian.PutUint64(buf, val)
		buf[0] |= 0x01
		return buf
	}
}

// makeElement creates an EBML element with [ID] [Size VINT] [Data].
func makeElement(id []byte, data []byte) []byte {
	vint := encodeVINT(uint64(len(data)))
	res := make([]byte, 0, len(id)+len(vint)+len(data))
	res = append(res, id...)
	res = append(res, vint...)
	res = append(res, data...)
	return res
}

func makeUintElement(id []byte, val uint64) []byte {
	// Serialize as big endian integer
	var intBytes []byte
	switch {
	case val <= 0xFF:
		intBytes = []byte{byte(val)}
	case val <= 0xFFFF:
		intBytes = []byte{byte(val >> 8), byte(val)}
	case val <= 0xFFFFFF:
		intBytes = []byte{byte(val >> 16), byte(val >> 8), byte(val)}
	default:
		intBytes = make([]byte, 4)
		binary.BigEndian.PutUint32(intBytes, uint32(val))
	}
	return makeElement(id, intBytes)
}

func makeStringElement(id []byte, str string) []byte {
	return makeElement(id, []byte(str))
}

// buildBitmapInfoHeader creates the 40-byte BITMAPINFOHEADER for V_MS/VFW/FOURCC RGB24.
func buildBitmapInfoHeader(w, h int) []byte {
	buf := make([]byte, 40)
	binary.LittleEndian.PutUint32(buf[0:4], 40)              // biSize
	binary.LittleEndian.PutUint32(buf[4:8], uint32(w))       // biWidth
	binary.LittleEndian.PutUint32(buf[8:12], uint32(h))      // biHeight
	binary.LittleEndian.PutUint16(buf[12:14], 1)             // biPlanes
	binary.LittleEndian.PutUint16(buf[14:16], 24)            // biBitCount (RGB24)
	binary.LittleEndian.PutUint32(buf[16:20], 0)             // biCompression (BI_RGB)
	binary.LittleEndian.PutUint32(buf[20:24], uint32(w*h*3)) // biSizeImage
	return buf
}

// EncodeMKV streams binary data from src into dst as an uncompressed RGB24 MKV video.
func EncodeMKV(dst io.Writer, src io.Reader, fileSize int64, res VideoResolution) error {
	frameBytes := res.Width * res.Height * 3
	totalFrames := int((fileSize + int64(frameBytes) - 1) / int64(frameBytes))
	if totalFrames == 0 {
		totalFrames = 1
	}

	// 1. EBML Header
	var ebmlHeader bytes.Buffer
	ebmlHeader.Write(makeUintElement(idEBMLVersion, 1))
	ebmlHeader.Write(makeUintElement(idEBMLReadVersion, 1))
	ebmlHeader.Write(makeUintElement(idEBMLMaxIDLen, 4))
	ebmlHeader.Write(makeUintElement(idEBMLMaxSizeLen, 8))
	ebmlHeader.Write(makeStringElement(idDocType, "matroska"))
	ebmlHeader.Write(makeUintElement(idDocTypeVersion, 4))
	ebmlHeader.Write(makeUintElement(idDocTypeReadVer, 2))
	if _, err := dst.Write(makeElement(idEBML, ebmlHeader.Bytes())); err != nil {
		return err
	}

	// 2. Segment Header (Unknown Size: 0x01FFFFFFFFFFFFFF)
	if _, err := dst.Write(idSegment); err != nil {
		return err
	}
	unknownSize := []byte{0x01, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF}
	if _, err := dst.Write(unknownSize); err != nil {
		return err
	}

	// 3. Info Element
	var info bytes.Buffer
	info.Write(makeUintElement(idTimestampScale, 1000000)) // 1ms
	info.Write(makeStringElement(idMuxingApp, "rclone-gphotosmobile"))
	info.Write(makeStringElement(idWritingApp, "rclone-gphotosmobile"))
	if _, err := dst.Write(makeElement(idInfo, info.Bytes())); err != nil {
		return err
	}

	// 4. Tracks Element
	var videoTrack bytes.Buffer
	videoTrack.Write(makeUintElement(idPixelWidth, uint64(res.Width)))
	videoTrack.Write(makeUintElement(idPixelHeight, uint64(res.Height)))

	var trackEntry bytes.Buffer
	trackEntry.Write(makeUintElement(idTrackNumber, 1))
	trackEntry.Write(makeUintElement(idTrackUID, 1))
	trackEntry.Write(makeUintElement(idTrackType, 1)) // 1 = Video
	trackEntry.Write(makeUintElement(idFlagLacing, 0))
	trackEntry.Write(makeStringElement(idCodecID, "V_MS/VFW/FOURCC"))
	trackEntry.Write(makeElement(idCodecPrivate, buildBitmapInfoHeader(res.Width, res.Height)))
	trackEntry.Write(makeElement(idVideo, videoTrack.Bytes()))

	var tracks bytes.Buffer
	tracks.Write(makeElement(idTrackEntry, trackEntry.Bytes()))
	if _, err := dst.Write(makeElement(idTracks, tracks.Bytes())); err != nil {
		return err
	}

	// 5. Clusters and SimpleBlocks
	// Standard 24 fps (41.67 ms per frame). 24 frames per cluster (1 second).
	const fps = 24
	const frameDurationMs = 1000 / fps
	frameBuf := make([]byte, frameBytes)

	for frameIdx := 0; frameIdx < totalFrames; {
		clusterStartFrame := frameIdx
		clusterTimestamp := uint64(clusterStartFrame * frameDurationMs)

		var clusterPayload bytes.Buffer
		clusterPayload.Write(makeUintElement(idTimestamp, clusterTimestamp))

		framesInThisCluster := totalFrames - clusterStartFrame
		if framesInThisCluster > fps {
			framesInThisCluster = fps
		}

		for i := 0; i < framesInThisCluster; i++ {
			n, err := io.ReadFull(src, frameBuf)
			if err != nil && err != io.EOF && err != io.ErrUnexpectedEOF {
				return err
			}

			// Pad incomplete frame with zeros
			if n < frameBytes {
				for p := n; p < frameBytes; p++ {
					frameBuf[p] = 0
				}
			}

			// SimpleBlock: Track 1 (0x81), Relative Time (int16), Flags (0x80 Keyframe), Payload
			relTime := int16(i * frameDurationMs)
			blockHeader := make([]byte, 4)
			blockHeader[0] = 0x81 // Track 1
			binary.BigEndian.PutUint16(blockHeader[1:3], uint16(relTime))
			blockHeader[3] = 0x80 // Keyframe flag

			var blockData bytes.Buffer
			blockData.Write(blockHeader)
			blockData.Write(frameBuf)

			clusterPayload.Write(makeElement(idSimpleBlock, blockData.Bytes()))
			frameIdx++
		}

		if _, err := dst.Write(makeElement(idCluster, clusterPayload.Bytes())); err != nil {
			return err
		}
	}

	return nil
}

// MKVDecoder reads an uncompressed RGB24 MKV stream and outputs the original binary bytes.
type MKVDecoder struct {
	r         io.Reader
	remBytes  int64
	frameBuf  []byte
	frameOff  int
	frameLen  int
	blockData []byte
	closed    bool
}

// NewMKVDecoder creates a streaming demuxer for uncompressed MKV binary streams.
func NewMKVDecoder(src io.Reader, originalSize int64) (*MKVDecoder, error) {
	return &MKVDecoder{
		r:        src,
		remBytes: originalSize,
		frameBuf: make([]byte, 1024*1024),
	}, nil
}

// readVINT decodes an EBML Variable Size Integer from the reader.
func readVINT(r io.Reader) (uint64, error) {
	first := make([]byte, 1)
	if _, err := io.ReadFull(r, first); err != nil {
		return 0, err
	}

	b := first[0]
	var mask byte
	var length int

	switch {
	case b&0x80 != 0:
		mask = 0x7F
		length = 1
	case b&0x40 != 0:
		mask = 0x3F
		length = 2
	case b&0x20 != 0:
		mask = 0x1F
		length = 3
	case b&0x10 != 0:
		mask = 0x0F
		length = 4
	case b&0x08 != 0:
		mask = 0x07
		length = 5
	case b&0x04 != 0:
		mask = 0x03
		length = 6
	case b&0x02 != 0:
		mask = 0x01
		length = 7
	case b&0x01 != 0:
		mask = 0x00
		length = 8
	default:
		return 0, errors.New("invalid VINT leading byte")
	}

	val := uint64(b & mask)
	if length > 1 {
		rest := make([]byte, length-1)
		if _, err := io.ReadFull(r, rest); err != nil {
			return 0, err
		}
		for _, v := range rest {
			val = (val << 8) | uint64(v)
		}
	}

	return val, nil
}

// readElementID decodes an EBML element ID (1 to 4 bytes).
func readElementID(r io.Reader) (uint32, error) {
	first := make([]byte, 1)
	if _, err := io.ReadFull(r, first); err != nil {
		return 0, err
	}
	b := first[0]
	var length int
	switch {
	case b&0x80 != 0:
		length = 1
	case b&0x40 != 0:
		length = 2
	case b&0x20 != 0:
		length = 3
	case b&0x10 != 0:
		length = 4
	default:
		return 0, fmt.Errorf("invalid element ID leading byte: 0x%02x", b)
	}

	id := uint32(b)
	if length > 1 {
		rest := make([]byte, length-1)
		if _, err := io.ReadFull(r, rest); err != nil {
			return 0, err
		}
		for _, v := range rest {
			id = (id << 8) | uint32(v)
		}
	}
	return id, nil
}

func (d *MKVDecoder) Read(p []byte) (int, error) {
	if d.remBytes <= 0 {
		return 0, io.EOF
	}

	bytesWritten := 0
	for bytesWritten < len(p) && d.remBytes > 0 {
		if d.frameOff < d.frameLen {
			toCopy := d.frameLen - d.frameOff
			if int64(toCopy) > d.remBytes {
				toCopy = int(d.remBytes)
			}
			if toCopy > len(p)-bytesWritten {
				toCopy = len(p) - bytesWritten
			}

			copy(p[bytesWritten:], d.frameBuf[d.frameOff:d.frameOff+toCopy])
			d.frameOff += toCopy
			bytesWritten += toCopy
			d.remBytes -= int64(toCopy)
			continue
		}

		// Read next EBML Element Header
		id, size, err := d.nextElement()
		if err != nil {
			if err == io.EOF && d.remBytes <= 0 {
				return bytesWritten, io.EOF
			}
			return bytesWritten, err
		}

		switch id {
		case 0x1A45DFA3, 0x1549A966, 0x1654AE6B, 0xE7:
			// EBML Header, Info, Tracks, or Timestamp: skip element data
			if size > 0 && size < 0x01FFFFFFFFFFFFFF {
				if _, err := io.CopyN(io.Discard, d.r, int64(size)); err != nil {
					return bytesWritten, err
				}
			}
		case 0x18538067, 0x1F43B675:
			// Segment or Cluster container: continue parsing children inside
			continue
		case 0xA3:
			// SimpleBlock
			if size < 4 {
				return bytesWritten, fmt.Errorf("simpleblock size too small: %d", size)
			}

			// Read 4 bytes block header: Track (1 byte), RelTime (2 bytes), Flags (1 byte)
			hdr := make([]byte, 4)
			if _, err := io.ReadFull(d.r, hdr); err != nil {
				return bytesWritten, err
			}

			payloadSize := int(size - 4)
			if len(d.frameBuf) < payloadSize {
				d.frameBuf = make([]byte, payloadSize)
			}

			n, err := io.ReadFull(d.r, d.frameBuf[:payloadSize])
			if err != nil {
				return bytesWritten, err
			}

			d.frameLen = n
			d.frameOff = 0
		default:
			// Skip unknown element
			if size > 0 && size < 0x01FFFFFFFFFFFFFF {
				if _, err := io.CopyN(io.Discard, d.r, int64(size)); err != nil {
					return bytesWritten, err
				}
			}
		}
	}

	if bytesWritten == 0 && d.remBytes <= 0 {
		return 0, io.EOF
	}
	return bytesWritten, nil
}

func (d *MKVDecoder) nextElement() (uint32, uint64, error) {
	id, err := readElementID(d.r)
	if err != nil {
		return 0, 0, err
	}
	size, err := readVINT(d.r)
	if err != nil {
		return 0, 0, err
	}
	return id, size, nil
}

func (d *MKVDecoder) Close() error {
	d.closed = true
	return nil
}
