package api

import (
	"github.com/bwmarrin/discordgo"
	"github.com/gin-gonic/gin"
	"github.com/tomerh2001/maple-culvert-tracker/internal/api/controllers"
	"github.com/tomerh2001/maple-culvert-tracker/internal/api/middlewares"
)

var DiscordSession *discordgo.Session

func NewRouter() *gin.Engine {
	controllers.DiscordSession = DiscordSession
	router := gin.Default()
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
