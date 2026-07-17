package model

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestEffectiveBillingStartFallsBackToJoinedAt 锁定账单归属起点的零值回退合同：
// BillingStartAt == 0（功能上线前的存量记录）必须回退到 JoinedAt，保证未回填时账单总额不变。
func TestEffectiveBillingStartFallsBackToJoinedAt(t *testing.T) {
	member := OrganizationMember{JoinedAt: 1000, BillingStartAt: 0}
	assert.Equal(t, int64(1000), effectiveBillingStart(member))

	member.BillingStartAt = 800
	assert.Equal(t, int64(800), effectiveBillingStart(member))
}

// TestIntervalsOverlap 锁定同组织窗口不相交校验的区间语义，防止同一条日志被两段
// 成员关系重复统计。半开区间 [s, l)，l == 0 表示开向 +∞。
func TestIntervalsOverlap(t *testing.T) {
	assert.True(t, intervalsOverlap(10, 20, 15, 30), "overlapping windows intersect")
	assert.False(t, intervalsOverlap(10, 20, 20, 30), "adjacent open intervals do not intersect")
	assert.False(t, intervalsOverlap(10, 20, 25, 30), "disjoint windows do not intersect")
	assert.True(t, intervalsOverlap(10, 0, 20, 30), "open-ended left intersects later window")
	assert.True(t, intervalsOverlap(10, 20, 5, 0), "open-ended right intersects earlier window")
	assert.False(t, intervalsOverlap(10, 20, 5, 10), "adjacent at start does not intersect")
}
