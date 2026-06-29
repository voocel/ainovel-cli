package store

import (
	"os"

	"github.com/voocel/ainovel-cli/internal/domain"
)

// UsageStore 持久化 token / cost 累计用量到 meta/usage.json。
// 写入走 IO 的 atomic write（tmp + rename），Save Đường dẫn每次完整覆盖整个 state。
type UsageStore struct{ io *IO }

func NewUsageStore(io *IO) *UsageStore { return &UsageStore{io: io} }

// Load Đọc usage.json。Tập tin不存在或 schema Phiên bản不匹配时Quay lại (nil, nil)，
// 由调用方决定Có czy không走 session replay 一次性回填。
func (s *UsageStore) Load() (*domain.UsageState, error) {
	var state domain.UsageState
	if err := s.io.ReadJSON("meta/usage.json", &state); err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	if state.Schema != domain.UsageSchemaVersion {
		return nil, nil
	}
	return &state, nil
}

// Save 把 state 完整覆盖落盘。调用方负责 debounce / 节流。
func (s *UsageStore) Save(state domain.UsageState) error {
	state.Schema = domain.UsageSchemaVersion
	return s.io.WriteJSON("meta/usage.json", state)
}
