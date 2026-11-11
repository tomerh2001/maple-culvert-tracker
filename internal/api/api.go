package api

import (
	"context"
	"github.com/bwmarrin/discordgo"
	"github.com/gin-gonic/gin"
	"github.com/tomerh2001/maple-culvert-tracker/internal/api/controllers"
	"github.com/tomerh2001/maple-culvert-tracker/internal/api/middlewares"
	"github.com/tomerh2001/maple-culvert-tracker/internal/db"
	"github.com/tomerh2001/maple-culvert-tracker/internal/imagesubmissions"
	"log"
	"os"
)

var DiscordSession *discordgo.Session
var imageHandler = &imagesubmissions.Handler{}

// No configured grants means no worker and no admitted image requests.
func StartImageSubmissions(ctx context.Context) error {
	principals, err := imagesubmissions.ParsePrincipals(os.Getenv("CULVERT_IMAGE_INTEGRATIONS"))
	if err != nil {
		return err
	}
	store := imagesubmissions.Store{DB: db.DB}
	imageHandler = &imagesubmissions.Handler{Store: store, Principals: principals}
	if len(principals) > 0 {
		log.Println("Starting configured image submission worker")
		go imagesubmissions.NewProcessor(store, DiscordSession).Run(ctx)
	}
	return nil
}

func NewRouter() *gin.Engine {
	controllers.DiscordSession = DiscordSession
	router := gin.Default()
	imageHandler.Register(router)
	// The web admin panel is gone; this internal API only serves the bot's
	// own score-submission flow (JWT-authenticated, localhost).
	apiGroup := router.Group("/api")
	{
		apiGroup.Use(middlewares.AuthMiddleware())
		maple := controllers.MapleController{}
		apiGroup.POST("/maple/characters/culvert", maple.POSTCulvert)
	}
	return router
}
