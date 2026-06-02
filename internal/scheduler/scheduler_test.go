package scheduler

import (
	"context"
	"testing"
	"time"

	"auto-publisher/internal/model"
	"auto-publisher/internal/publisher"
)

func TestScheduler_New(t *testing.T) {
	sched, err := New(nil, 5*time.Minute, 3, 10*time.Minute, "Asia/Shanghai")
	if err != nil {
		t.Fatalf("创建调度器失败: %v", err)
	}

	if sched.interval != 5*time.Minute {
		t.Errorf("期望 interval=5m，实际 %v", sched.interval)
	}

	if sched.maxRetries != 3 {
		t.Errorf("期望 maxRetries=3，实际 %d", sched.maxRetries)
	}
}

func TestScheduler_Register(t *testing.T) {
	sched, _ := New(nil, 1*time.Minute, 1, 1*time.Minute, "UTC")

	pub := &mockPub{name: "test-pub", platform: publisher.PlatformZhihu}
	sched.Register("zhihu", pub)

	if _, ok := sched.publishers["zhihu"]; !ok {
		t.Error("发布器注册失败")
	}
}

func TestScheduler_SelectPublisher_Zhihu(t *testing.T) {
	sched, _ := New(nil, 1*time.Minute, 1, 1*time.Minute, "UTC")
	pub := &mockPub{name: "zhihu", platform: publisher.PlatformZhihu}
	sched.Register("zhihu", pub)

	result := sched.selectPublisher(model.PlatformZhihu)
	if result == nil {
		t.Error("知乎平台应返回发布器")
	}
}

func TestScheduler_SelectPublisher_Both(t *testing.T) {
	sched, _ := New(nil, 1*time.Minute, 1, 1*time.Minute, "UTC")
	pub := &mockPub{name: "zhihu", platform: publisher.PlatformZhihu}
	sched.Register("zhihu", pub)

	// both 平台应 fallback 到 zhihu
	result := sched.selectPublisher(model.PlatformBoth)
	if result == nil {
		t.Error("both 平台应 fallback 到 zhihu")
	}
}

func TestScheduler_SelectPublisher_Unregistered(t *testing.T) {
	sched, _ := New(nil, 1*time.Minute, 1, 1*time.Minute, "UTC")

	result := sched.selectPublisher(model.PlatformXiaohongshu)
	if result != nil {
		t.Error("未注册的平台应返回 nil")
	}
}

func TestFmtErr(t *testing.T) {
	err := fmtErr("test error")
	if err.Error() != "test error" {
		t.Errorf("期望 'test error'，实际 '%s'", err.Error())
	}
}

// mockPub 实现 publisher.Publisher 接口
type mockPub struct {
	name     string
	platform publisher.Platform
}

func (m *mockPub) Name() string                           { return m.name }
func (m *mockPub) Platform() publisher.Platform           { return m.platform }
func (m *mockPub) Validate(ctx context.Context) error      { return nil }
func (m *mockPub) IsAvailable(ctx context.Context) bool    { return true }
func (m *mockPub) Publish(ctx context.Context, c *model.Content) (*publisher.PublishResult, error) {
	return &publisher.PublishResult{Success: true}, nil
}
