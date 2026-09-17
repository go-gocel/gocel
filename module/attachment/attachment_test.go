package attachment

import (
	"bytes"
	"context"
	"errors"
	"testing"

	coreattachment "github.com/go-gocel/gocel/core/attachment"
)

// minimalPNG is a 1x1 transparent PNG.
var minimalPNG = []byte{
	0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a,
	0x00, 0x00, 0x00, 0x0d, 'I', 'H', 'D', 'R',
	0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x01,
	0x08, 0x06, 0x00, 0x00, 0x00,
	0x1f, 0x15, 0xc4, 0x89,
	0x00, 0x00, 0x00, 0x0a, 'I', 'D', 'A', 'T',
	0x78, 0x9c, 0x63, 0x00, 0x01, 0x00, 0x00, 0x05, 0x00, 0x01,
	0x0d, 0x0a, 0x2d, 0xb4,
	0x00, 0x00, 0x00, 0x00, 'I', 'E', 'N', 'D',
	0xae, 0x42, 0x60, 0x82,
}

// minimalGIF is a 2x2 GIF.
var minimalGIF = []byte("GIF89a" +
	"\x02\x00\x02\x00" + // width 2, height 2
	"\x80\x00\x00" +
	"\x00\x00\x00\x00\x00\x00" +
	"\x00\x00" + "\x00" +
	"\x2c\x00\x00\x00\x00\x02\x00\x02\x00\x00" +
	"\x02\x02\x44\x01\x00" +
	"\x3b")

// vp8Lossy is a 3x5 VP8 key-frame header in the real wire layout: frame tag
// (3) + start code 0x9d 0x01 0x2a (3) + 14-bit width (2) + 14-bit height (2);
// the frame body is truncated (Pixels only reads the header).
var vp8Lossy = []byte{
	'R', 'I', 'F', 'F', 0x00, 0x00, 0x00, 0x00, 'W', 'E', 'B', 'P',
	'V', 'P', '8', ' ', 0x0a, 0x00, 0x00, 0x00, // chunk fourcc + size
	0x10, 0x00, 0x00, // frame tag (key frame, show)
	0x9d, 0x01, 0x2a, // start code
	0x03, 0x00, // width 3
	0x05, 0x00, // height 5
	0x00, // truncated body
}

// vp8x is a 3x5 VP8X header: canvas size is stored minus one.
var vp8x = []byte{
	'R', 'I', 'F', 'F', 0x00, 0x00, 0x00, 0x00, 'W', 'E', 'B', 'P',
	'V', 'P', '8', 'X', 0x0a, 0x00, 0x00, 0x00, // chunk fourcc + size
	0x00, // flags
	0x00, 0x00, 0x00, // reserved
	0x02, 0x00, 0x00, // canvas width-1 = 2 → 3
	0x04, 0x00, 0x00, // canvas height-1 = 4 → 5
}

// vp8l is a 3x5 VP8L header: signature 0x2f then packed 14-bit dimensions.
var vp8l = []byte{
	'R', 'I', 'F', 'F', 0x00, 0x00, 0x00, 0x00, 'W', 'E', 'B', 'P',
	'V', 'P', '8', 'L', 0x05, 0x00, 0x00, 0x00, // chunk fourcc + size
	0x2f,       // signature
	0x02, 0x00, // width-1 = 2 → 3
	0x01, 0x00, // height-1 = 4 → 5
}

// jpegSOF0 is a 3x2 JPEG: SOI + APP0/JFIF then a SOF0 frame header.
var jpegSOF0 = []byte{
	0xff, 0xd8, // SOI
	0xff, 0xe0, 0x00, 0x10, // APP0 (len 16)
	'J', 'F', 'I', 'F', 0x00,
	0x01, 0x01, 0x00, 0x00, 0x01, 0x00, 0x01, 0x00, 0x00,
	0xff, 0xc0, 0x00, 0x11, // SOF0 (len 17)
	0x08,       // precision
	0x00, 0x02, // height 2
	0x00, 0x03, // width 3
	0x03,       // components
	0x01, 0x11, 0x00, 0x02, 0x11, 0x01, 0x03, 0x11, 0x01,
}

// TestMagicValidator_RecognizesFormats: PNG/GIF/WebP/JPEG magic sniffing.
func TestMagicValidator_RecognizesFormats(t *testing.T) {
	v := MagicValidator{}
	mt, err := v.MediaType(minimalPNG)
	if err != nil || mt != "image/png" {
		t.Fatalf("png = %q, %v", mt, err)
	}
	mt, err = v.MediaType(minimalGIF)
	if err != nil || mt != "image/gif" {
		t.Fatalf("gif = %q, %v", mt, err)
	}
	webp := append([]byte("RIFF\x00\x00\x00\x00WEBPVP8 "), make([]byte, 20)...)
	mt, err = v.MediaType(webp)
	if err != nil || mt != "image/webp" {
		t.Fatalf("webp = %q, %v", mt, err)
	}
	jpeg := []byte{0xff, 0xd8, 0xff, 0xe0, 0x00, 0x10}
	mt, err = v.MediaType(jpeg)
	if err != nil || mt != "image/jpeg" {
		t.Fatalf("jpeg = %q, %v", mt, err)
	}
	if _, err := v.MediaType([]byte("not an image")); !errors.Is(err, ErrInvalidImage) {
		t.Fatalf("junk = %v, want ErrInvalidImage", err)
	}
}

// TestMagicValidator_Pixels: pixel counts come from the headers.
func TestMagicValidator_Pixels(t *testing.T) {
	v := MagicValidator{}
	p, err := v.Pixels(minimalPNG)
	if err != nil || p != 1 {
		t.Fatalf("png pixels = %d, %v; want 1", p, err)
	}
	p, err = v.Pixels(minimalGIF)
	if err != nil || p != 4 {
		t.Fatalf("gif pixels = %d, %v; want 4", p, err)
	}
}

// TestMagicValidator_Pixels_WebP: all three WebP sub-formats report their
// canvas pixels from the real header layouts.
func TestMagicValidator_Pixels_WebP(t *testing.T) {
	v := MagicValidator{}
	if p, err := v.Pixels(vp8Lossy); err != nil || p != 15 {
		t.Fatalf("vp8 pixels = %d, %v; want 15", p, err)
	}
	if p, err := v.Pixels(vp8x); err != nil || p != 15 {
		t.Fatalf("vp8x pixels = %d, %v; want 15", p, err)
	}
	if p, err := v.Pixels(vp8l); err != nil || p != 15 {
		t.Fatalf("vp8l pixels = %d, %v; want 15", p, err)
	}
}

// TestMagicValidator_Pixels_JPEG: the SOF segment yields the frame
// dimensions; a JPEG without any SOF is rejected, not misreported.
func TestMagicValidator_Pixels_JPEG(t *testing.T) {
	v := MagicValidator{}
	if p, err := v.Pixels(jpegSOF0); err != nil || p != 6 {
		t.Fatalf("jpeg pixels = %d, %v; want 6", p, err)
	}
	if _, err := v.Pixels([]byte{0xff, 0xd8, 0xff, 0xd9}); !errors.Is(err, ErrInvalidImage) {
		t.Fatalf("no-SOF jpeg = %v, want ErrInvalidImage", err)
	}
}

// TestService_SaveImageValidatesAndStores: admission (format + limits) then
// immutable storage.
func TestService_SaveImageValidatesAndStores(t *testing.T) {
	store, err := coreattachment.NewFileStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	svc, err := NewService(store, DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	ref, err := svc.SaveImage(ctx, minimalPNG, "dot.png")
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	if ref.MediaType != "image/png" {
		t.Fatalf("media type = %q, want image/png", ref.MediaType)
	}
	got, err := svc.ReadImage(ctx, ref.ID)
	if err != nil || !bytes.Equal(got, minimalPNG) {
		t.Fatalf("read = %v, %v", err, got)
	}
	// Same bytes deduplicate.
	ref2, err := svc.SaveImage(ctx, minimalPNG, "dot-copy.png")
	if err != nil || ref2.ID != ref.ID {
		t.Fatalf("dedup = %+v, %v", ref2, err)
	}
}

// TestService_RejectsJunkAndOversized: invalid bytes and oversize images
// are refused before any write.
func TestService_RejectsJunkAndOversized(t *testing.T) {
	store, _ := coreattachment.NewFileStore(t.TempDir())
	svc, _ := NewService(store, DefaultLimits())
	ctx := context.Background()

	if _, err := svc.SaveImage(ctx, []byte("junk"), "j.txt"); !errors.Is(err, ErrInvalidImage) {
		t.Fatalf("junk = %v, want ErrInvalidImage", err)
	}
	big := make([]byte, 6<<20) // 6 MiB > 5 MiB default
	if _, err := svc.SaveImage(ctx, big, "big.png"); !errors.Is(err, ErrImageTooLarge) {
		t.Fatalf("oversize = %v, want ErrImageTooLarge", err)
	}
	// A tiny limit rejects even a small image.
	svc2, _ := NewService(store, Limits{MaxBytes: 4, MaxPixels: 10})
	if _, err := svc2.SaveImage(ctx, minimalPNG, "tiny.png"); !errors.Is(err, ErrImageTooLarge) {
		t.Fatalf("tiny limit = %v, want ErrImageTooLarge", err)
	}
}
