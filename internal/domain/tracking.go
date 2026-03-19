package domain

// StateChange 角色/实体状态变化记录。
type StateChange struct {
	Chapter  int    `json:"chapter"`
	Entity   string `json:"entity"`              // 角色名或实体名
	Field    string `json:"field"`               // 变化属性：realm/location/status/power/relation 等
	OldValue string `json:"old_value,omitempty"` // 变化前（首次出现可空）
	NewValue string `json:"new_value"`           // 变化后
	Reason   string `json:"reason,omitempty"`    // 变化原因
}
