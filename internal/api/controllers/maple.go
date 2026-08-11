package controllers

import (
	"log"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/tomerh2001/maple-culvert-tracker/internal/api/helpers"
	"github.com/tomerh2001/maple-culvert-tracker/internal/apiredis"
	cmdhelpers "github.com/tomerh2001/maple-culvert-tracker/internal/commands/helpers"
	"github.com/tomerh2001/maple-culvert-tracker/internal/data"
	"github.com/tomerh2001/maple-culvert-tracker/internal/db"
)

type MapleController struct{}

// POSTCulvert upserts culvert scores for a week in one transaction. The
// body's isNew flag is ignored: inserts and updates are the same upsert
// (ON CONFLICT ... DO UPDATE), and an empty payload is a harmless no-op.
func (MapleController) POSTCulvert(c *gin.Context) {
	body := data.POSTCulvertBody{}
	if err := c.BindJSON(&body); err != nil {
		c.AbortWithError(http.StatusBadRequest, err)
		return
	}
	thisWeek := time.Now()
	var err error
	if body.Week != "" {
		thisWeek, err = time.Parse(time.DateOnly, body.Week)
		if err != nil || thisWeek.Weekday() != cmdhelpers.GetCulvertResetDay(thisWeek) {
			c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{
				"error": "Date incorrectly formatted or isn't a culvert reset day.",
			})
			return
		}
	}
	thisWeek = cmdhelpers.GetCulvertResetDate(thisWeek)

	changes := make([]cmdhelpers.ScoreChange, 0, len(body.Payload))
	changedIDs := make([]int64, 0, len(body.Payload))
	for _, v := range body.Payload {
		changes = append(changes, cmdhelpers.ScoreChange{CharacterID: v.CharacterID, Score: v.Score})
		changedIDs = append(changedIDs, v.CharacterID)
	}

	if err := cmdhelpers.UpsertCulvertScores(db.DB, thisWeek, changes); err != nil {
		log.Println("DB ERROR POSTCulvert", err)
		c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{
			"error": "DB failed.",
		})
		return
	}

	if len(changedIDs) > 0 {
		go helpers.AnnounceSubmission(DiscordSession, db.DB, apiredis.RedisDB, thisWeek, changedIDs)
	}

	c.AbortWithStatusJSON(http.StatusOK, gin.H{})
}
