package knowledge

const (
	EntryTypeMenuNode  = "menu_node"
	EntryTypeKnowledge = "knowledge"
	EntryTypeChitchat  = "chitchat"
)

type Entry struct {
	ID         string
	SourceKey  string
	Keyword    string
	EntryType  string
	Path       string
	Aliases    []string
	Category   string
	Answer     string
	Content    string
	Enabled    bool
	ExactReply bool
	AIEnabled  bool
}

type ParseIssueReason string

const (
	IssueMissingKeyword     ParseIssueReason = "missing_keyword"
	IssueMissingAnswer      ParseIssueReason = "missing_answer"
	IssueDuplicateIdentical ParseIssueReason = "duplicate_identical"
)

type ParseIssue struct {
	Row    int
	Reason ParseIssueReason
}

type ConflictType string

const (
	ConflictSourceKey ConflictType = "source_key"
	ConflictKeyword   ConflictType = "keyword"
	ConflictAlias     ConflictType = "alias"
)

type ParseConflict struct {
	Type     ConflictType
	Key      string
	EntryIDs []string
}

type ParseResult struct {
	// Entries includes every structurally valid row for management preview.
	Entries []Entry
	// ActiveEntries is the deterministic, conflict-safe runtime collection.
	ActiveEntries    []Entry
	Issues           []ParseIssue
	Conflicts        []ParseConflict
	DisabledEntryIDs []string
	AIOnlyEntryIDs   []string
	Warnings         []string
}
