package apperr

import (
	"errors"
	"testing"
)

func TestCodeOf(t *testing.T) {
	err := Wrap(errors.New("boom"), CodeStoreReadFailed, "store.load", "读取失败")
	if got := CodeOf(err); got != CodeStoreReadFailed {
		t.Fatalf("CodeOf() = %s, want %s", got, CodeStoreReadFailed)
	}
}

func TestIsCode(t *testing.T) {
	err := New(CodeConfigInvalid, "bootstrap.validate", "配置无效")
	if !IsCode(err, CodeConfigInvalid) {
		t.Fatal("expected IsCode to match config invalid")
	}
	if IsCode(err, CodeStoreWriteFailed) {
		t.Fatal("unexpected store write code match")
	}
}
