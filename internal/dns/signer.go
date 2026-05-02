package dns

import (
	"crypto/hmac"
	"crypto/sha1"
	"encoding/base64"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
)

// BuildSignedURL 构建阿里云 DNS API 的签名请求 URL
func BuildSignedURL(accessKeyID, accessKeySecret, action string, params map[string]string) string {
	// 添加公共参数
	params["Format"] = "JSON"
	params["Version"] = "2015-01-09"
	params["AccessKeyId"] = accessKeyID
	params["SignatureMethod"] = "HMAC-SHA1"
	params["Timestamp"] = time.Now().UTC().Format("2006-01-02T15:04:05Z")
	params["SignatureVersion"] = "1.0"
	params["SignatureNonce"] = uuid.New().String()
	params["Action"] = action

	// 按 key 排序
	keys := make([]string, 0, len(params))
	for k := range params {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	// 构建排序后的查询字符串
	pairs := make([]string, 0, len(keys))
	for _, k := range keys {
		pairs = append(pairs, fmt.Sprintf("%s=%s", percentEncode(k), percentEncode(params[k])))
	}
	sortedQuery := strings.Join(pairs, "&")

	// 构建待签名字符串
	stringToSign := fmt.Sprintf("GET&%s&%s", percentEncode("/"), percentEncode(sortedQuery))
	signingKey := accessKeySecret + "&"

	// HMAC-SHA1 签名
	mac := hmac.New(sha1.New, []byte(signingKey))
	mac.Write([]byte(stringToSign))
	signature := base64.StdEncoding.EncodeToString(mac.Sum(nil))

	return fmt.Sprintf("https://alidns.aliyuncs.com/?%s&Signature=%s", sortedQuery, percentEncode(signature))
}

// percentEncode 阿里云风格的百分号编码
func percentEncode(s string) string {
	encoded := url.QueryEscape(s)
	encoded = strings.ReplaceAll(encoded, "+", "%20")
	encoded = strings.ReplaceAll(encoded, "*", "%2A")
	encoded = strings.ReplaceAll(encoded, "%7E", "~")
	return encoded
}
