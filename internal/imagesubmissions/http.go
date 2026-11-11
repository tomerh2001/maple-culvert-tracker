package imagesubmissions

import (
	"context"
	"database/sql"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/tomerh2001/maple-culvert-tracker/internal/data"
)

type Handler struct {
	Store      Store
	Principals []Principal
	Now        func() time.Time
}

func belongsToTenant(guild, tenant string) bool { return data.TenantID(guild) == tenant }
func (h *Handler) now() time.Time {
	if h.Now != nil {
		return h.Now()
	}
	return time.Now()
}

func (h *Handler) Register(router *gin.Engine) {
	router.POST("/api/v1/guild-score-submissions", h.post)
	router.GET("/api/v1/guild-score-submissions/by-capture/:capture", h.byCapture)
	router.GET("/api/v1/guild-score-submissions/:id", h.get)
}
func (h *Handler) principal(c *gin.Context) (Principal, bool) {
	c.Header("Cache-Control", "no-store")
	p, ok := authenticate(c.Request, h.Principals, h.now())
	if !ok {
		c.JSON(401, gin.H{"error": gin.H{"code": "invalid_credential"}})
		return p, false
	}
	if !belongsToTenant(p.DiscordGuildID, p.TenantID) {
		c.JSON(403, gin.H{"error": gin.H{"code": "mapping_mismatch"}})
		return p, false
	}
	return p, true
}
func replyError(c *gin.Context, err error) {
	if e, ok := isAPIError(err); ok {
		c.JSON(e.Status, gin.H{"error": gin.H{"code": e.Code}})
		return
	}
	c.JSON(503, gin.H{"error": gin.H{"code": "storage_unavailable"}})
}
func (h *Handler) post(c *gin.Context) {
	p, ok := h.principal(c)
	if !ok {
		return
	}
	// Server read timeout covers slow-body uploads; this deadline also bounds DB
	// work. The complete accepted batch remains durable if the response is lost.
	ctx, cancel := context.WithTimeout(c.Request.Context(), 90*time.Second)
	defer cancel()
	b, err := readBatch(c.Writer, c.Request, p, h.now())
	if err != nil {
		replyError(c, err)
		return
	}
	r, err := h.Store.Accept(ctx, p, b)
	if err != nil {
		replyError(c, err)
		return
	}
	c.Header("Location", r.StatusURL)
	c.JSON(http.StatusAccepted, r)
}
func (h *Handler) get(c *gin.Context) {
	p, ok := h.principal(c)
	if !ok {
		return
	}
	if !safeID.MatchString(c.Param("id")) {
		replyError(c, &APIError{404, "not_found"})
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 5*time.Second)
	defer cancel()
	r, err := h.Store.Get(ctx, p, c.Param("id"))
	if err != nil {
		replyError(c, err)
		return
	}
	c.JSON(200, r)
}
func (h *Handler) byCapture(c *gin.Context) {
	p, ok := h.principal(c)
	if !ok {
		return
	}
	if !safeID.MatchString(c.Param("capture")) {
		replyError(c, &APIError{404, "not_found"})
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 5*time.Second)
	defer cancel()
	var id string
	err := h.Store.DB.QueryRowContext(ctx, `SELECT id FROM image_submissions WHERE principal_id=$1 AND capture_run_id=$2 AND tenant_id=$3 AND discord_guild_id=$4 AND collector_id=$5 AND destination_id=$6 AND maple_guild=$7 AND actor_id=$8`, p.ID, c.Param("capture"), p.TenantID, p.DiscordGuildID, p.CollectorID, p.DestinationID, p.MapleGuild, p.ActorID).Scan(&id)
	if err == sql.ErrNoRows {
		replyError(c, &APIError{404, "not_found"})
		return
	}
	if err != nil {
		replyError(c, err)
		return
	}
	r, err := h.Store.Get(ctx, p, id)
	if err != nil {
		replyError(c, err)
		return
	}
	c.JSON(200, r)
}
