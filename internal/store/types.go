package store

type Space struct {
	ID       string
	Name     string
	RawJSON  string
	Source   string
	SyncedAt int64
}

type User struct {
	ID       string
	Name     string
	Email    string
	RawJSON  string
	Source   string
	SyncedAt int64
}

type Team struct {
	ID          string
	SpaceID     string
	ParentID    string
	ParentTable string
	Name        string
	RawJSON     string
	Source      string
	SyncedAt    int64
}

type Page struct {
	ID             string
	SpaceID        string
	ParentID       string
	ParentTable    string
	CollectionID   string
	Title          string
	URL            string
	Icon           string
	Cover          string
	PropertiesJSON string
	CreatedTime    int64
	LastEditedTime int64
	Alive          bool
	Source         string
	RawJSON        string
	SyncedAt       int64
}

type Block struct {
	ID             string
	PageID         string
	SpaceID        string
	ParentID       string
	ParentTable    string
	Type           string
	Text           string
	PropertiesJSON string
	ContentJSON    string
	FormatJSON     string
	DisplayOrder   int64
	CreatedTime    int64
	LastEditedTime int64
	Alive          bool
	Source         string
	RawJSON        string
	SyncedAt       int64
}

type BlockCoverage struct {
	Referenced int
	Missing    int
}

type Collection struct {
	ID          string
	SpaceID     string
	ParentID    string
	ParentTable string
	Name        string
	SchemaJSON  string
	FormatJSON  string
	RawJSON     string
	Source      string
	SyncedAt    int64
}

type Comment struct {
	ID             string
	PageID         string
	SpaceID        string
	ParentID       string
	Text           string
	CreatedByID    string
	CreatedTime    int64
	LastEditedTime int64
	Alive          bool
	RawJSON        string
	Source         string
	SyncedAt       int64
}

type RawRecord struct {
	Source      string
	RecordTable string
	RecordID    string
	ParentID    string
	SpaceID     string
	RawJSON     string
	SyncedAt    int64
}

type ParentRef struct {
	ID    string
	Table string
}

type SearchResult struct {
	Kind  string
	ID    string
	Title string
	Text  string
}
