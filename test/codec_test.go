package test

import (
	"bytes"
	"crypto/rand"
	"crypto/sha1"
	"encoding/hex"
	"io"
	"os"
	"os/exec"
	"testing"

	"github.com/user/rclone-gphotos-oot/backend/gphotosmobile/binary"
)

func TestPNGRoundtrip(t *testing.T) {
	sizes := []int{10, 500, 1024, 180 * 1024, 1024 * 1024}

	for _, size := range sizes {
		data := make([]byte, size)
		if _, err := rand.Read(data); err != nil {
			t.Fatal(err)
		}

		var buf bytes.Buffer
		if err := binary.EncodePNG(&buf, bytes.NewReader(data), int64(size)); err != nil {
			t.Fatalf("EncodePNG failed for size %d: %v", size, err)
		}

		decoder, err := binary.NewPNGDecoder(&buf, int64(size))
		if err != nil {
			t.Fatalf("NewPNGDecoder failed for size %d: %v", size, err)
		}
		defer decoder.Close()

		decoded, err := io.ReadAll(decoder)
		if err != nil {
			t.Fatalf("ReadAll failed for size %d: %v", size, err)
		}

		if len(decoded) != size {
			t.Fatalf("Decoded length mismatch: got %d, want %d", len(decoded), size)
		}

		if !bytes.Equal(data, decoded) {
			t.Fatalf("Decoded content mismatch for size %d", size)
		}
	}
}

func TestMKVRoundtrip(t *testing.T) {
	res := binary.VideoResolution{Name: "test", Width: 240, Height: 240}
	frameBytes := res.Width * res.Height * 3 // 172,800 bytes
	sizes := []int{100, frameBytes / 2, frameBytes + 500, frameBytes*2 + 100}

	for _, size := range sizes {
		data := make([]byte, size)
		if _, err := rand.Read(data); err != nil {
			t.Fatal(err)
		}

		var buf bytes.Buffer
		if err := binary.EncodeMKV(&buf, bytes.NewReader(data), int64(size), res); err != nil {
			t.Fatalf("EncodeMKV failed for size %d: %v", size, err)
		}

		decoder, err := binary.NewMKVDecoder(&buf, int64(size))
		if err != nil {
			t.Fatalf("NewMKVDecoder failed for size %d: %v", size, err)
		}
		defer decoder.Close()

		decoded, err := io.ReadAll(decoder)
		if err != nil {
			t.Fatalf("ReadAll failed for size %d: %v", size, err)
		}

		if len(decoded) != size {
			t.Fatalf("Decoded length mismatch: got %d, want %d", len(decoded), size)
		}

		if !bytes.Equal(data, decoded) {
			t.Fatalf("Decoded content mismatch for size %d", size)
		}
	}
}

func TestHighLevelEncodeDecode(t *testing.T) {
	data := []byte("Hello, world! This is a test binary file for gphotosmobile.")
	h := sha1.Sum(data)
	expectedSHA1 := hex.EncodeToString(h[:])

	media, err := binary.Encode(bytes.NewReader(data), "test.txt", int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	defer media.Close()

	if media.Details.SHA1 != expectedSHA1 {
		t.Fatalf("SHA1 mismatch: got %s, want %s", media.Details.SHA1, expectedSHA1)
	}

	decoder, err := binary.DecodeStream(media.File, media.MediaFilename, media.Details.Size)
	if err != nil {
		t.Fatal(err)
	}
	defer decoder.Close()

	decoded, err := io.ReadAll(decoder)
	if err != nil {
		t.Fatal(err)
	}

	if !bytes.Equal(data, decoded) {
		t.Fatalf("Decoded data mismatch: got %q, want %q", string(decoded), string(data))
	}
}

func TestExceedsMaxFileSize(t *testing.T) {
	fakeData := bytes.NewReader([]byte{})
	_, err := binary.Encode(fakeData, "giant.iso", binary.MaxFileBytes+1)
	if err == nil {
		t.Fatal("expected error for file exceeding 50 GB, got nil")
	}
}

func TestMKVFFprobeCompatibility(t *testing.T) {
	tempFile, err := os.CreateTemp(t.TempDir(), "test_*.mkv")
	if err != nil {
		t.Fatal(err)
	}
	defer tempFile.Close()

	data := make([]byte, 1024*1024)
	res := binary.VideoResolution{Width: 426, Height: 240}
	if err := binary.EncodeMKV(tempFile, bytes.NewReader(data), int64(len(data)), res); err != nil {
		t.Fatal(err)
	}
	_ = tempFile.Close()

	if _, err := exec.LookPath("ffprobe"); err == nil {
		out, err := exec.Command("ffprobe", "-v", "error", "-show_streams", tempFile.Name()).CombinedOutput()
		if err != nil {
			t.Fatalf("ffprobe failed on generated MKV: %v, output: %s", err, string(out))
		}
	}
}
