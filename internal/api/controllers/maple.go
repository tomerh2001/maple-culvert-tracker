package controllers

import (
	"context"
	"fmt"
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
	thisWeekStr := thisWeek.Format(time.DateOnly)
	if body.IsNew {
		query := ""
		args := []any{}
		d := 1
		for _, v := range body.Payload {
			query += fmt.Sprintf("($%d,'%s',$%d),", d, thisWeekStr, d+1)
			d += 2
			args = append(args, v.CharacterID, v.Score)
		}
		query = "INSERT INTO character_culvert_scores (character_id, culvert_date, score) VALUES " + query[:len(query)-1]
		_, err = db.DB.Exec(query, args...)
	} else { //typically this should be a patch request
		tx, errtx := db.DB.BeginTx(context.Background(), nil)
		if errtx != nil {
			log.Println("DB ERROR tx POSTCulvert", err)
			c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{
				"error": "DB failed.",
			})
			return
		}
		for _, v := range body.Payload {
			_, err = tx.Exec("UPDATE character_culvert_scores SET score = $1 WHERE character_id = $2 AND culvert_date = $3", v.Score, v.CharacterID, thisWeekStr)
			if err != nil {
				log.Println("DB ERROR POSTCulvert", err)
				c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{
					"error": "DB failed.",
				})
				tx.Rollback()
				return
			}
		}
		err = tx.Commit()
	}
	if err != nil {
		log.Println("DB ERROR POSTCulvert", err)
		c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{
			"error": "DB failed.",
		})
		return
	}

	changedIDs := make([]int64, 0, len(body.Payload))
	for _, v := range body.Payload {
		changedIDs = append(changedIDs, v.CharacterID)
	}
	go helpers.AnnounceSubmission(DiscordSession, db.DB, apiredis.RedisDB, thisWeek, changedIDs)

	c.AbortWithStatusJSON(http.StatusOK, gin.H{})
}
