// Package attachment is the media-validation layer over the core
// content-addressed attachment store (DSH attachment + attachment-local
// split): the core store owns immutable storage; this package owns the
// consumer-facing admission policy — image format validation (magic
// numbers), byte and pixel limits — and the read_image-facing surface.
//
// Package attachment 是 core 内容寻址附件存储之上的媒体校验层（DSH
// attachment + attachment-local 拆分）：core 存储拥有不可变存储；本包拥有
// 面向消费方的准入策略——图片格式校验（魔数）、字节与像素上限，以及
// read_image 面。
package attachment

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"

	coreattachment "github.com/go-gocel/gocel/core/attachment"
)

// Limits bounds the admitted image attachments.
// Limits 限制准入的图片附件。
type Limits struct {
	// MaxBytes caps one image's size (default 5 MiB).
	MaxBytes int64
	// MaxPixels caps the decoded pixel count (default 40 MP).
	MaxPixels int64
}

// DefaultLimits returns the shipped limits.
// DefaultLimits 返回随包限制。
func DefaultLimits() Limits {
	return Limits{MaxBytes: 5 << 20, MaxPixels: 40_000_000}
}

var (
	// ErrInvalidImage is returned when the bytes are not a recognized image.
	//
	// ErrInvalidImage 在字节不是可识别的图片时返回。
	ErrInvalidImage = errors.New("attachment: not a recognized image")
	// ErrImageTooLarge is returned when the byte or pixel limit is exceeded.
	//
	// ErrImageTooLarge 在超出字节或像素限制时返回。
	ErrImageTooLarge = errors.New("attachment: image exceeds the limit")
)

// Validator validates image bytes and computes pixel counts.
// Validator 校验图片字节并计算像素数。
type Validator interface {
	// MediaType returns the media type when the bytes are a recognized
	// image, or an error.
	MediaType(data []byte) (string, error)
	// Pixels estimates the pixel count of the decoded image.
	Pixels(data []byte) (int64, error)
}

// MagicValidator uses magic-number sniffing — dependency-free, deterministic,
// sufficient for admission (the store is immutable; consumers re-decode with
// their own library).
//
// MagicValidator 用魔数嗅探——零依赖、确定、对准入足够（存储不可变；
// 消费方用自己的库重新解码）。
type MagicValidator struct{}

// MediaType sniffs the image format.
//
// MediaType 嗅探图片格式。
func (MagicValidator) MediaType(data []byte) (string, error) {
	switch {
	case len(data) >= 8 && bytes.Equal(data[:8], []byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a}):
		return "image/png", nil
	case len(data) >= 3 && bytes.Equal(data[:3], []byte{0xff, 0xd8, 0xff}):
		return "image/jpeg", nil
	case len(data) >= 6 && (bytes.Equal(data[:6], []byte("GIF87a")) || bytes.Equal(data[:6], []byte("GIF89a"))):
		return "image/gif", nil
	case len(data) >= 12 && bytes.Equal(data[:4], []byte("RIFF")) && bytes.Equal(data[8:12], []byte("WEBP")):
		return "image/webp", nil
	default:
		return "", ErrInvalidImage
	}
}

// Pixels estimates the pixel count from the image header.
//
// Pixels 从图片头部估算像素数。
func (MagicValidator) Pixels(data []byte) (int64, error) {
	mt, err := (MagicValidator{}).MediaType(data)
	if err != nil {
		return 0, err
	}
	switch mt {
	case "image/png":
		if len(data) < 24 {
			return 0, ErrInvalidImage
		}
		w := int64(binary.BigEndian.Uint32(data[16:20]))
		h := int64(binary.BigEndian.Uint32(data[20:24]))
		if w > 0 && h > 0 && w*h < 0 {
			// uint32 header values can overflow int64 (2^32×2^32 ≥ 2^63):
			// a negative product would bypass the pixel limit.
			return 0, ErrImageTooLarge
		}
		return w * h, nil
	case "image/jpeg":
		// SOF scan: parse segments for the first frame dimensions.
		if len(data) < 4 || data[0] != 0xff || data[1] != 0xd8 {
			return 0, ErrInvalidImage
		}
		i := 2
		for i+3 < len(data) {
			if data[i] != 0xff {
				i++
				continue
			}
			marker := data[i+1]
			if marker == 0xd8 || (marker >= 0xd0 && marker <= 0xd7) || marker == 0x01 {
				i += 2
				continue
			}
			if i+3 >= len(data) {
				break
			}
			segLen := int(binary.BigEndian.Uint16(data[i+2 : i+4]))
			if marker >= 0xc0 && marker <= 0xcf && marker != 0xc4 && marker != 0xc8 && marker != 0xcc {
				if i+9 < len(data) {
					h := int64(binary.BigEndian.Uint16(data[i+5 : i+7]))
					w := int64(binary.BigEndian.Uint16(data[i+7 : i+9]))
					return w * h, nil
				}
			}
			i += 2 + segLen
		}
		return 0, ErrInvalidImage
	case "image/gif":
		if len(data) < 10 {
			return 0, ErrInvalidImage
		}
		w := int64(binary.LittleEndian.Uint16(data[6:8]))
		h := int64(binary.LittleEndian.Uint16(data[8:10]))
		return w * h, nil
	case "image/webp":
		// VP8 / VP8L / VP8X — read the canvas size. Each sub-format has
		// its own header length; the length guard ALWAYS precedes the
		// data[12:16] slice (short headers must error, never panic).
		if len(data) >= 30 && bytes.Equal(data[12:16], []byte("VP8X")) {
			w := 1 + int64(uint32(data[24])|uint32(data[25])<<8|uint32(data[26])<<16)
			h := 1 + int64(uint32(data[27])|uint32(data[28])<<8|uint32(data[29])<<16)
			return w * h, nil
		}
		if len(data) >= 30 && bytes.Equal(data[12:16], []byte("VP8 ")) {
			// Key-frame header: frame tag (3) + start code 0x9d 0x01 0x2a
			// (3) + 14-bit width (2) + 14-bit height (2).
			w := int64(binary.LittleEndian.Uint16(data[26:28])) & 0x3fff
			h := int64(binary.LittleEndian.Uint16(data[28:30])) & 0x3fff
			return w * h, nil
		}
		if len(data) >= 25 && bytes.Equal(data[12:16], []byte("VP8L")) {
			b0 := uint32(data[21])
			b1 := uint32(data[22])
			b2 := uint32(data[23])
			b3 := uint32(data[24])
			w := int64(1 + ((b1&0x3f)<<8|b0))
			h := int64(1 + ((b3&0x0f)<<10|(b2)<<2|(b1>>6)))
			return w * h, nil
		}
		return 0, ErrInvalidImage
	default:
		return 0, ErrInvalidImage
	}
}

// Service is the consumer-facing admission + storage surface.
// Service 是面向消费方的准入 + 存储面。
type Service struct {
	store     coreattachment.Store
	validator Validator
	limits    Limits
}

// NewService builds the admission service over the core store.
// NewService 在 core 存储上构建准入服务。
func NewService(store coreattachment.Store, limits Limits) (*Service, error) {
	if store == nil {
		return nil, errors.New("attachment: nil store")
	}
	if limits.MaxBytes <= 0 {
		limits.MaxBytes = DefaultLimits().MaxBytes
	}
	if limits.MaxPixels <= 0 {
		limits.MaxPixels = DefaultLimits().MaxPixels
	}
	return &Service{store: store, validator: MagicValidator{}, limits: limits}, nil
}

// Store exposes the underlying core store (for direct Has/Retrieve).
// Store 暴露底层 core 存储（供直接 Has/Retrieve）。
func (s *Service) Store() coreattachment.Store { return s.store }

// SaveImage validates the bytes, then commits them immutably.
// SaveImage 校验字节后不可变提交。
func (s *Service) SaveImage(ctx context.Context, data []byte, name string) (coreattachment.Ref, error) {
	if int64(len(data)) > s.limits.MaxBytes {
		return coreattachment.Ref{}, fmt.Errorf("%w: %d bytes > %d", ErrImageTooLarge, len(data), s.limits.MaxBytes)
	}
	mt, err := s.validator.MediaType(data)
	if err != nil {
		return coreattachment.Ref{}, err
	}
	pixels, err := s.validator.Pixels(data)
	if err != nil {
		return coreattachment.Ref{}, err
	}
	if pixels > s.limits.MaxPixels {
		return coreattachment.Ref{}, fmt.Errorf("%w: %d pixels > %d", ErrImageTooLarge, pixels, s.limits.MaxPixels)
	}
	return s.store.Save(ctx, mt, data, name)
}

// ReadImage retrieves and verifies the image.
// ReadImage 取回并校验图片。
func (s *Service) ReadImage(ctx context.Context, id string) ([]byte, error) {
	return s.store.Retrieve(ctx, id)
}
