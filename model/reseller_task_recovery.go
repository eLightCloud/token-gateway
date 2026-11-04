package model

import "gorm.io/gorm"

// Terminal status belongs to the original engine. No elapsed-time predicate
// participates in personal recovery or in deciding whether money is refundable.
func personalTerminalRequests(db *gorm.DB) *gorm.DB {
	tasks := db.Model(&Task{}).Select("id").Where("status IN ? AND usage_pending = ?", []TaskStatus{TaskStatusSuccess, TaskStatusFailure}, false)
	mj := db.Model(&Midjourney{}).Select("id").Where("status IN ?", []string{"SUCCESS", "FAILURE"})
	return db.Model(&ResellerRequest{}).Where("state IN ?", []string{"pending", "reserved", "outcome_unknown"}).Where("(entity_type = ? AND entity_id IN (?)) OR (entity_type = ? AND entity_id IN (?))", TaskSettlementEntityTask, tasks, TaskSettlementEntityMidjourney, mj)
}
func HasPendingPersonalTerminalTasks() bool {
	var id string
	return personalTerminalRequests(DB).Select("id").Limit(1).Scan(&id).Error == nil && id != ""
}
func ListPersonalTerminalRequests(after string, limit int) ([]ResellerRequest, error) {
	if limit < 1 || limit > 100 {
		return nil, ErrResellerInvalid
	}
	var requests []ResellerRequest
	err := personalTerminalRequests(DB).Where("id > ?", after).Order("id").Limit(limit).Find(&requests).Error
	return requests, err
}
