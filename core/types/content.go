// ❄️ FROZEN — Stable data contract. Types, fields and semantics must not change.
package types

// ContentType represents the type of content in a content part.
//
// ContentType 表示内容片段的类型。
type ContentType string

const (
	// ContentTypeText is the plain-text content type.
	// ContentTypeText 纯文本内容类型。
	ContentTypeText      ContentType = "text"
	// ContentTypeImageURL is the image content type referenced by URL.
	// ContentTypeImageURL 以 URL 引用的图片内容类型。
	ContentTypeImageURL  ContentType = "image_url"
	// ContentTypeImageData is the base64-encoded image content type.
	// ContentTypeImageData base64 编码的图片内容类型。
	ContentTypeImageData ContentType = "image_data"
	// ContentTypeAudioData is the base64-encoded audio content type.
	// ContentTypeAudioData base64 编码的音频内容类型。
	ContentTypeAudioData ContentType = "audio_data"
	// ContentTypeVideoData is the base64-encoded video content type.
	// ContentTypeVideoData base64 编码的视频内容类型。
	ContentTypeVideoData ContentType = "video_data"
	// ContentTypeFileData is the base64-encoded file content type.
	// ContentTypeFileData base64 编码的文件内容类型。
	ContentTypeFileData  ContentType = "file_data"
)

// ContentPart represents a single piece of content within a message.
// A message can have multiple content parts of different types.
// Backward compatibility: if a Message has ContentParts, it takes priority;
// otherwise the legacy Content string field is used as a single text part.
//
// ContentPart 表示消息中的单个内容片段。一个消息可以有多个不同类型的片段。
// 如果 Message 有 ContentParts 则优先使用，否则回退到 Content 字符串字段。
type ContentPart struct {
	Type      ContentType   `json:"type"`
	Text      string        `json:"text,omitempty"`
	ImageURL  string        `json:"image_url,omitempty"`
	ImageData *ImageContent `json:"image_data,omitempty"`
	AudioData *MediaContent `json:"audio_data,omitempty"`
	VideoData *MediaContent `json:"video_data,omitempty"`
	FileData  *FileContent  `json:"file_data,omitempty"`
}

// ImageContent holds image data for content parts (base64-encoded).
// ImageContent 保存图片内容（base64 编码）。
type ImageContent struct {
	Data     string `json:"data"`             // base64-encoded image data
	MIMEType string `json:"mime_type"`        // e.g. "image/png", "image/jpeg"
	Detail   string `json:"detail,omitempty"` // OpenAI: "auto", "low", "high"
}

// MediaContent holds binary media data (audio, video).
// MediaContent 保存二进制媒体数据（音频、视频）。
type MediaContent struct {
	Data     string `json:"data"`      // base64-encoded data
	MIMEType string `json:"mime_type"` // e.g. "audio/mpeg", "video/mp4"
}

// FileContent holds file data.
// FileContent 保存文件数据。
type FileContent struct {
	Data     string `json:"data"`           // base64-encoded data
	MIMEType string `json:"mime_type"`      // e.g. "application/pdf"
	Name     string `json:"name,omitempty"` // original filename
}

// ContentPart constructors.

// NewTextPart creates a text content part.
// NewTextPart 创建文本内容片段。
func NewTextPart(text string) ContentPart {
	return ContentPart{Type: ContentTypeText, Text: text}
}

// NewImageURLPart creates an image content part from a URL.
//
// NewImageURLPart 从 URL 创建图片内容片段。
func NewImageURLPart(url string) ContentPart {
	return ContentPart{
		Type:     ContentTypeImageURL,
		ImageURL: url,
	}
}

// NewImageDataPart creates an image content part from base64-encoded data.
// mimeType examples: "image/png", "image/jpeg", "image/webp".
//
// NewImageDataPart 从 base64 编码数据创建图片内容片段。
func NewImageDataPart(data, mimeType, detail string) ContentPart {
	return ContentPart{
		Type: ContentTypeImageData,
		ImageData: &ImageContent{
			Data:     data,
			MIMEType: mimeType,
			Detail:   detail,
		},
	}
}

// NewAudioDataPart creates an audio content part from base64-encoded data.
// NewAudioDataPart 从 base64 编码数据创建音频内容片段。
func NewAudioDataPart(data, mimeType string) ContentPart {
	return ContentPart{
		Type: ContentTypeAudioData,
		AudioData: &MediaContent{
			Data:     data,
			MIMEType: mimeType,
		},
	}
}

// NewVideoDataPart creates a video content part from base64-encoded data.
// NewVideoDataPart 从 base64 编码数据创建视频内容片段。
func NewVideoDataPart(data, mimeType string) ContentPart {
	return ContentPart{
		Type: ContentTypeVideoData,
		VideoData: &MediaContent{
			Data:     data,
			MIMEType: mimeType,
		},
	}
}

// NewFileDataPart creates a file content part from base64-encoded data.
// NewFileDataPart 从 base64 编码数据创建文件内容片段。
func NewFileDataPart(data, mimeType, name string) ContentPart {
	return ContentPart{
		Type: ContentTypeFileData,
		FileData: &FileContent{
			Data:     data,
			MIMEType: mimeType,
			Name:     name,
		},
	}
}

// Helper functions for Message creation with multimodal content.

// NewUserMultimodalMessage creates a user message with content parts.
// NewUserMultimodalMessage 创建带内容片段的用户消息。
func NewUserMultimodalMessage(parts ...ContentPart) *Message {
	return &Message{
		Role:         RoleUser,
		ContentParts: parts,
	}
}

// NewSystemMultimodalMessage creates a system message with content parts.
// NewSystemMultimodalMessage 创建带内容片段的系统消息。
func NewSystemMultimodalMessage(parts ...ContentPart) *Message {
	return &Message{
		Role:         RoleSystem,
		ContentParts: parts,
	}
}

// HasMultimodalContent returns true if the message contains non-text content parts.
// HasMultimodalContent 返回消息是否包含非文本内容片段。
func (m *Message) HasMultimodalContent() bool {
	if m == nil || len(m.ContentParts) == 0 {
		return false
	}
	for _, p := range m.ContentParts {
		if p.Type != ContentTypeText {
			return true
		}
	}
	return false
}

// IsMultimodal returns true if the message has content parts (any type, including text).
// IsMultimodal 返回消息是否含有内容片段（任意类型，含文本）。
func (m *Message) IsMultimodal() bool {
	return m != nil && len(m.ContentParts) > 0
}

// cloneContentPart creates a deep copy of a ContentPart.
func cloneContentPart(p ContentPart) ContentPart {
	cp := ContentPart{Type: p.Type, Text: p.Text, ImageURL: p.ImageURL}
	if p.ImageData != nil {
		cp.ImageData = &ImageContent{
			Data:     p.ImageData.Data,
			MIMEType: p.ImageData.MIMEType,
			Detail:   p.ImageData.Detail,
		}
	}
	if p.AudioData != nil {
		cp.AudioData = &MediaContent{
			Data:     p.AudioData.Data,
			MIMEType: p.AudioData.MIMEType,
		}
	}
	if p.VideoData != nil {
		cp.VideoData = &MediaContent{
			Data:     p.VideoData.Data,
			MIMEType: p.VideoData.MIMEType,
		}
	}
	if p.FileData != nil {
		cp.FileData = &FileContent{
			Data:     p.FileData.Data,
			MIMEType: p.FileData.MIMEType,
			Name:     p.FileData.Name,
		}
	}
	return cp
}

// cloneContentParts creates a deep copy of a ContentPart slice.
func cloneContentParts(parts []ContentPart) []ContentPart {
	if parts == nil {
		return nil
	}
	result := make([]ContentPart, len(parts))
	for i, p := range parts {
		result[i] = cloneContentPart(p)
	}
	return result
}
