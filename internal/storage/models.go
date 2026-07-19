package storage

import "time"

type GroupJoinRequest struct {
	ID          uint64 `gorm:"primaryKey"`
	RequestKey  string `gorm:"size:191;not null;uniqueIndex"`
	Flag        string `gorm:"size:512"`
	GroupID     int64  `gorm:"index"`
	UserID      int64  `gorm:"index"`
	StudentID   string `gorm:"size:64"`
	StudentName string `gorm:"size:64"`
	SubType     string `gorm:"size:32"`
	Comment     string `gorm:"type:text"`
	Status      string `gorm:"size:32;not null;index"`
	Source      string `gorm:"size:32;not null"`
	RawJSON     string `gorm:"type:mediumtext"`
	RequestedAt time.Time
	FirstSeenAt time.Time
	LastSeenAt  time.Time `gorm:"index"`
}

func (GroupJoinRequest) TableName() string {
	return "group_join_requests"
}
