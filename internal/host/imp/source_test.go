package imp

import (
	"testing"

	"golang.org/x/text/encoding/simplifiedchinese"
)

func TestDecodeSourceUTF8(t *testing.T) {
	d, err := decodeSource([]byte("第一章　风起\n正文"))
	if err != nil {
		t.Fatalf("utf-8: %v", err)
	}
	if d.encoding != encodingUTF8 || d.text != "第一章　风起\n正文" {
		t.Fatalf("utf-8 结果不符：%+v", d)
	}
}

func TestDecodeSourceUTF8BOM(t *testing.T) {
	raw := append(append([]byte{}, utf8BOM...), []byte("楔子")...)
	d, err := decodeSource(raw)
	if err != nil {
		t.Fatalf("bom: %v", err)
	}
	if d.encoding != encodingUTF8BOM || d.text != "楔子" {
		t.Fatalf("bom 结果不符：%+v", d)
	}
}

func TestDecodeSourceGB18030(t *testing.T) {
	want := "第一章　风起\n正文内容"
	gb, err := simplifiedchinese.GB18030.NewEncoder().Bytes([]byte(want))
	if err != nil {
		t.Fatalf("编码 GB18030 测试数据失败：%v", err)
	}
	d, err := decodeSource(gb)
	if err != nil {
		t.Fatalf("gb18030: %v", err)
	}
	if d.encoding != encodingGB18030 || d.text != want {
		t.Fatalf("gb18030 结果不符：%+v", d)
	}
}

func TestDecodeSourceBOMInvalidBodyFails(t *testing.T) {
	raw := append(append([]byte{}, utf8BOM...), []byte{0xFF, 0xFE}...)
	if _, err := decodeSource(raw); err == nil {
		t.Fatal("声明 BOM 但正文非法应失败")
	}
}

func TestNormalizeLineEndings(t *testing.T) {
	if got := normalize("a\r\nb\rc\nd"); got != "a\nb\nc\nd" {
		t.Fatalf("归一化不符：%q", got)
	}
	// 空行与缩进必须保留。
	if got := normalize("第一章\r\n\r\n\t正文"); got != "第一章\n\n\t正文" {
		t.Fatalf("空行/缩进未保留：%q", got)
	}
}
