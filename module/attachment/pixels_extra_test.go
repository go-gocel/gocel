package attachment

import (
	"encoding/binary"
	"errors"
	"testing"
)

// Regression: WEBP pixel parsing sliced data[12:16] before the length
// check — a 12-15 byte WEBP header panicked the process (C17).
func TestPixels_ShortWEBPNoPanic(t *testing.T) {
	for _, data := range [][]byte{
		[]byte("RIFF\x00\x00\x00\x00WEBP"),       // 12 bytes
		[]byte("RIFF\x00\x00\x00\x00WEBPVP8 "),   // 15 bytes
		[]byte("RIFF\x00\x00\x00\x00WEBPVP8L"),   // 15 bytes
		[]byte("RIFF\x00\x00\x00\x00WEBPVP8X"),   // 15 bytes
	} {
		if _, err := (MagicValidator{}).Pixels(data); err == nil {
			t.Fatalf("short webp (%d bytes): want error, got nil", len(data))
		}
	}
}

// Regression: PNG width/height come from uint32 header fields; the int64
// product overflows for claimed 4G×4G images, yielding a negative pixel
// count that bypassed the pixel limit.
func TestPixels_PNGOverflowRejected(t *testing.T) {
	data := make([]byte, 24)
	copy(data, []byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a})
	binary.BigEndian.PutUint32(data[16:20], 0xFFFFFFFF)
	binary.BigEndian.PutUint32(data[20:24], 0xFFFFFFFF)
	if _, err := (MagicValidator{}).Pixels(data); !errors.Is(err, ErrImageTooLarge) {
		t.Fatalf("overflowing PNG: want ErrImageTooLarge, got %v", err)
	}
}
