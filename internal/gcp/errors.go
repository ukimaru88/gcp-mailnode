package gcp

import (
	"errors"
	"strings"

	"google.golang.org/api/googleapi"
)

// IsNotFound 判断错误是否为 404 NotFound（云端资源已不存在）。
// 用于容忍"用户已在 GCP 控制台手动删除"或"之前释放过"的情况，避免本地状态卡死。
func IsNotFound(err error) bool {
	if err == nil {
		return false
	}
	var gerr *googleapi.Error
	if errors.As(err, &gerr) && gerr.Code == 404 {
		return true
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "notfound") ||
		strings.Contains(msg, "was not found") ||
		strings.Contains(msg, "does not exist")
}

// IsQuotaExceeded 判断错误是否为 QUOTA_EXCEEDED（region 级配额耗尽，短期内重试无意义）。
// 用于 Stage A 的 region 软熔断：触发后停止该 region 的后续 IP 预留尝试，引导用户去提额。
func IsQuotaExceeded(err error) bool {
	if err == nil {
		return false
	}
	var gerr *googleapi.Error
	if errors.As(err, &gerr) && gerr.Code == 403 && strings.Contains(gerr.Message, "Quota") {
		return true
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "quota_exceeded") || strings.Contains(msg, "quota exceeded")
}
