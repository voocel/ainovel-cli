package workbench

import "github.com/voocel/ainovel-cli/internal/infra/activity"

type ActivityFeed interface {
	Subscribe(projectID string) (<-chan struct{}, func())
	Snapshot(projectID string) (activity.Snapshot, bool)
}

// AttachActivityFeed 装配实时活动通道；nil 表示无实时通道，订阅方得到 ok=false。
func (s *Query) AttachActivityFeed(feed ActivityFeed) { s.activity = feed }

// SubscribeRunActivity 订阅作品的实时创作活动：返回合并唤醒信号与取消函数。
// 取消只结束订阅，绝不取消创作（页面设计 §4 交付契约 2）。
func (s *Query) SubscribeRunActivity(projectID string) (<-chan struct{}, func(), bool) {
	if s.activity == nil {
		return nil, func() {}, false
	}
	wake, cancel := s.activity.Subscribe(projectID)
	return wake, cancel, true
}

// RunActivity 整读作品当前活动快照；ok=false 表示无活动或未装配通道。
func (s *Query) RunActivity(projectID string) (activity.Snapshot, bool) {
	if s.activity == nil {
		return activity.Snapshot{}, false
	}
	return s.activity.Snapshot(projectID)
}
