package knowledge

const (
	EntryTypeMenuNode  = "menu_node"
	EntryTypeKnowledge = "knowledge"
	EntryTypeChitchat  = "chitchat"
)

type Entry struct {
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
