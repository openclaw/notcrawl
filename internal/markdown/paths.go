package markdown

import (
	"context"

	"github.com/openclaw/notcrawl/internal/notiontext"
	"github.com/openclaw/notcrawl/internal/store"
	"github.com/openclaw/notcrawl/internal/tableexport"
)

type pathResolver struct {
	spaces       map[string]string
	teams        map[string]string
	blocks       map[string]store.ParentRef
	collections  map[string]store.ParentRef
	properties   map[string]map[string]propertySpec
	propertyRefs tableexport.ReferenceLabels
}

func newPathResolver(ctx context.Context, st *store.Store) (pathResolver, error) {
	spaces, err := st.SpaceNames(ctx)
	if err != nil {
		return pathResolver{}, err
	}
	teams, err := st.TeamNames(ctx)
	if err != nil {
		return pathResolver{}, err
	}
	blocks, err := st.BlockParents(ctx)
	if err != nil {
		return pathResolver{}, err
	}
	collections, err := st.CollectionParents(ctx)
	if err != nil {
		return pathResolver{}, err
	}
	collectionRows, err := st.Collections(ctx)
	if err != nil {
		return pathResolver{}, err
	}
	properties := make(map[string]map[string]propertySpec, len(collectionRows))
	for _, collection := range collectionRows {
		properties[collection.ID] = propertySpecs(collection.SchemaJSON)
	}
	users, err := st.UserNames(ctx)
	if err != nil {
		return pathResolver{}, err
	}
	pages, err := st.PageTitles(ctx)
	if err != nil {
		return pathResolver{}, err
	}
	return pathResolver{
		spaces:      spaces,
		teams:       teams,
		blocks:      blocks,
		collections: collections,
		properties:  properties,
		propertyRefs: tableexport.ReferenceLabels{
			Users: users,
			Pages: pages,
		},
	}, nil
}

func (r pathResolver) spaceName(id string) string {
	if id == "" {
		return "default"
	}
	if name := r.spaces[id]; name != "" {
		return name
	}
	return "space-" + notiontext.ShortID(id)
}

func (r pathResolver) teamName(id string) string {
	if id == "" {
		return ""
	}
	if name := r.teams[id]; name != "" {
		return name
	}
	return "team-" + notiontext.ShortID(id)
}

func (r pathResolver) pageTeamID(page store.Page) string {
	return r.resolveTeamID(page.ParentTable, page.ParentID, page.CollectionID, map[string]bool{page.ID: true})
}

func (r pathResolver) resolveTeamID(table, id, collectionID string, seen map[string]bool) string {
	if table == "team" {
		return id
	}
	if table == "collection" && id == "" {
		id = collectionID
	}
	if id == "" || seen[table+":"+id] {
		return ""
	}
	seen[table+":"+id] = true
	switch table {
	case "block":
		parent := r.blocks[id]
		return r.resolveTeamID(parent.Table, parent.ID, "", seen)
	case "collection", "database", "data_source":
		parent := r.collections[id]
		return r.resolveTeamID(parent.Table, parent.ID, "", seen)
	default:
		return ""
	}
}
