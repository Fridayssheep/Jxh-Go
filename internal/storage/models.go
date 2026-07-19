package storage

import "time"

type KnowledgeEntry struct {
	ID              uint64 `gorm:"primaryKey"`
	SourceKey       string `gorm:"size:255;not null;uniqueIndex"`
	Keyword         string `gorm:"size:255;not null"`
	EntryType       string `gorm:"size:32;not null"`
	Path            string `gorm:"size:512"`
	AliasesJSON     string `gorm:"type:json"`
	Category        string `gorm:"size:64"`
	TagsJSON        string `gorm:"type:json"`
	Answer          string `gorm:"type:text;not null"`
	Content         string `gorm:"type:mediumtext;not null"`
	Enabled         bool   `gorm:"not null"`
	ExactReply      bool   `gorm:"not null"`
	AIEnabled       bool   `gorm:"not null"`
	LastImportRunID uint64
	SourceUpdatedAt *time.Time
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

type KnowledgeImportRun struct {
	ID           uint64 `gorm:"primaryKey"`
	Source       string `gorm:"size:32;not null"`
	Status       string `gorm:"size:16;not null"`
	TotalRows    int
	ImportedRows int
	SkippedRows  int
	ErrorMessage string `gorm:"type:text"`
	StartedAt    time.Time
	FinishedAt   *time.Time
}

type Admin struct {
	GroupID       int64 `gorm:"primaryKey"`
	UserID        int64 `gorm:"primaryKey"`
	ManualGranted bool
	QQRole        string `gorm:"size:16;not null"`
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

type Blacklist struct {
	UserID    int64 `gorm:"primaryKey"`
	CreatedAt time.Time
}

type ScheduledJob struct {
	ID        uint64 `gorm:"primaryKey"`
	Type      string `gorm:"size:16;not null"`
	TimeHHMM  string `gorm:"size:5;not null"`
	GroupID   int64  `gorm:"not null"`
	Message   string `gorm:"type:text;not null"`
	Enabled   bool   `gorm:"not null"`
	LastRunAt *time.Time
	CreatedAt time.Time
	UpdatedAt time.Time
}

type ProcessedEvent struct {
	EventKey    string    `gorm:"size:128;primaryKey"`
	ProcessedAt time.Time `gorm:"index"`
}

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
