package store

import (
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/voocel/ainovel-cli/internal/domain"
)

// MaterialsStore 管理项目级素材库（meta/materials.json）。
//
// 设计与 SimulationStore 类似：单文件 JSON、独立 mu 保护、os.IsNotExist 优雅降级。
// 不做缓存——Load 量小（典型几十条素材），每次工具调用读盘足够。
type MaterialsStore struct {
	io *IO
	mu sync.Mutex
}

func NewMaterialsStore(io *IO) *MaterialsStore {
	return &MaterialsStore{io: io}
}

const materialsPath = "meta/materials.json"

// Load 返回完整素材库。文件不存在时返回空库（不报错）——
// 调用方（novel_context / list_materials）需要稳定拿到 *MaterialLibrary，而非 nil。
func (s *MaterialsStore) Load() (*domain.MaterialLibrary, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.loadUnlocked()
}

// Add 追加单条素材，自动补 ID/AddedAt，返回带 ID 的完整条目。
// Category 空值归一化为 reference；Title 或 Content 空则报错——
// 这两项是后续检索与展示的关键，不能缺。
func (s *MaterialsStore) Add(item domain.MaterialItem) (domain.MaterialItem, error) {
	if item.Title == "" {
		return item, fmt.Errorf("material title is required")
	}
	if item.Content == "" {
		return item, fmt.Errorf("material content is required")
	}
	if item.Category == "" {
		item.Category = domain.MaterialCategoryReference
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	lib, err := s.loadUnlocked()
	if err != nil {
		return item, err
	}
	if item.ID == "" {
		item.ID = generateMaterialID(lib)
	}
	if item.AddedAt.IsZero() {
		item.AddedAt = time.Now()
	}
	lib.Items = append(lib.Items, item)
	if err := s.saveUnlocked(lib); err != nil {
		return item, err
	}
	return item, nil
}

// AddBatch 批量追加。比循环调 Add 少几次加锁/落盘，architect 一次性写多条素材时用。
// 任何一条 Title/Content 空都直接返回错误，整体不写入——避免半提交状态。
func (s *MaterialsStore) AddBatch(items []domain.MaterialItem) ([]domain.MaterialItem, error) {
	for i, it := range items {
		if it.Title == "" || it.Content == "" {
			return nil, fmt.Errorf("items[%d]: title and content are required", i)
		}
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	lib, err := s.loadUnlocked()
	if err != nil {
		return nil, err
	}
	out := make([]domain.MaterialItem, 0, len(items))
	for _, it := range items {
		if it.Category == "" {
			it.Category = domain.MaterialCategoryReference
		}
		if it.ID == "" {
			it.ID = generateMaterialID(lib)
		}
		if it.AddedAt.IsZero() {
			it.AddedAt = time.Now()
		}
		lib.Items = append(lib.Items, it)
		out = append(out, it)
	}
	if err := s.saveUnlocked(lib); err != nil {
		return nil, err
	}
	return out, nil
}

// Remove 按 ID 删除单条。不存在返回错误，调用方（remove_material 工具）转 friendly 提示。
func (s *MaterialsStore) Remove(id string) error {
	if id == "" {
		return fmt.Errorf("material id is empty")
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	lib, err := s.loadUnlocked()
	if err != nil {
		return err
	}
	for i, it := range lib.Items {
		if it.ID == id {
			lib.Items = append(lib.Items[:i], lib.Items[i+1:]...)
			return s.saveUnlocked(lib)
		}
	}
	return fmt.Errorf("material %q not found", id)
}

func (s *MaterialsStore) loadUnlocked() (*domain.MaterialLibrary, error) {
	var lib domain.MaterialLibrary
	if err := s.io.ReadJSONUnlocked(materialsPath, &lib); err != nil {
		if os.IsNotExist(err) {
			return &domain.MaterialLibrary{}, nil
		}
		return nil, err
	}
	return &lib, nil
}

func (s *MaterialsStore) saveUnlocked(lib *domain.MaterialLibrary) error {
	return s.io.WriteJSONUnlocked(materialsPath, lib)
}

// generateMaterialID 生成稳定递增的 mat-NNN ID。
// 不用 UUID 是为了让 LLM 在 remove_material(id=...) 时能直接报出可读 ID。
func generateMaterialID(lib *domain.MaterialLibrary) string {
	maxIdx := 0
	var n int
	for _, it := range lib.Items {
		if _, err := fmt.Sscanf(it.ID, "mat-%d", &n); err == nil && n > maxIdx {
			maxIdx = n
		}
	}
	return fmt.Sprintf("mat-%03d", maxIdx+1)
}
