package utils

import (
	"strings"
	"unicode/utf8"

	"golang.org/x/text/encoding/simplifiedchinese"
)

// DecodeText 把用户提供的文本Tập tin字节解码为 UTF-8：非法 UTF-8 时按 GB18030
//（GBK 超集）转码——网络流传的中文小说 txt 大量为 GBK 编码，直接当 UTF-8 读
// 全是乱码。非 GBK 的字节序列会被解码器替换为 U+FFFD（本就是乱码，由调用方的
// 零命中兜底报错引导用户）。最后剥离 UTF-8 BOM（否则行首匹配会带上它）。
func DecodeText(data []byte) string {
	if !utf8.Valid(data) {
		if decoded, err := simplifiedchinese.GB18030.NewDecoder().Bytes(data); err == nil {
			data = decoded
		}
	}
	return strings.TrimPrefix(string(data), "\uFEFF")
}
