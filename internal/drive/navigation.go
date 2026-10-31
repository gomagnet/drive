package drive

import "github.com/gin-gonic/gin"

func (a *App) entryPath(c *gin.Context) {
	ent, ok := a.entry(c, false)
	if !ok {
		return
	}
	type part struct {
		ID   int64  `json:"id"`
		Name string `json:"name"`
	}
	list := []part{}
	for depth := 0; ; depth++ {
		if depth > 256 || ent.DeletedAt != nil {
			fail(c, 409, "Folder path unavailable")
			return
		}
		list = append(list, part{ent.ID, ent.Name})
		if ent.ParentID == nil {
			break
		}
		if e := a.db.GetContext(c, &ent, entrySelect+"WHERE e.id=? AND e.space_id=?", *ent.ParentID, ent.SpaceID); e != nil {
			fail(c, 404, "Parent folder unavailable")
			return
		}
	}
	for i, j := 0, len(list)-1; i < j; i, j = i+1, j-1 {
		list[i], list[j] = list[j], list[i]
	}
	c.JSON(200, list)
}
