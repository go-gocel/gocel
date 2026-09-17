package llm

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/go-gocel/gocel/core/kernel"
)

// apiError classifies a non-2xx API response as a kernel.ModelError (C7):
// retryable for rate limits (429) and server-side failures (5xx), permanent
// otherwise (auth, validation, not found). Consumers decide via
// kernel.IsRetryableError — no more string matching on error text.
//
// apiError 把非 2xx API 响应归类为 kernel.ModelError（C7）：限流（429）与
// 服务端失败（5xx）可重试，其余（鉴权、校验、未找到）为永久错误。消费方
// 用 kernel.IsRetryableError 判定——不再对错误文本做字符串匹配。
func apiError(model string, status int, body []byte) error {
	msg := strings.TrimSpace(string(body))
	if len(msg) > 300 {
		msg = msg[:300]
	}
	return &kernel.ModelError{
		Code:      kernel.CodeModelError,
		Model:     model,
		Message:   fmt.Sprintf("HTTP %d: %s", status, msg),
		Retryable: status == http.StatusTooManyRequests || status >= 500,
	}
}

// apiErrorf is apiError with a formatted message (structured provider
// errors keep their detail while inheriting the status classification).
func apiErrorf(model string, status int, format string, args ...any) error {
	return &kernel.ModelError{
		Code:      kernel.CodeModelError,
		Model:     model,
		Message:   fmt.Sprintf(format, args...),
		Retryable: status == http.StatusTooManyRequests || status >= 500,
	}
}

// transportError wraps a client.Do failure (timeout, refused, reset) as a
// retryable ModelError — transport failures are transient by nature.
//
// transportError 把 client.Do 失败（超时、拒绝、重置）包装为可重试的
// ModelError——传输层失败本质上是瞬态的。
func transportError(model string, err error) error {
	return &kernel.ModelError{
		Code:      kernel.CodeModelError,
		Model:     model,
		Cause:     err,
		Message:   err.Error(),
		Retryable: true,
	}
}
