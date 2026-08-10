package middlewares

import (
	"net/http"
	"os"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
	"github.com/tomerh2001/maple-culvert-tracker/internal/data"
)

func AuthMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		auth := c.Request.Header.Get("Authorization")
		if strings.HasPrefix(auth, "Bearer ") {
			auth = auth[7:]
		} else {
			c.AbortWithStatus(http.StatusUnauthorized)
			return
		}
		claims := &data.MCTClaims{}
		tkn, err := jwt.ParseWithClaims(auth, claims, func(token *jwt.Token) (any, error) {
			return []byte(os.Getenv(data.EnvVarJWTSecret)), nil
		})
		if err != nil {
			if err == jwt.ErrSignatureInvalid {
				c.AbortWithStatus(http.StatusUnauthorized)
				return
			}
			c.AbortWithStatus(http.StatusUnauthorized)
			return
		}
		if !tkn.Valid {
			c.AbortWithStatus(http.StatusUnauthorized)
			return
		}

		c.Set("discord_username", claims.DiscordUsername)
		c.Set("discord_server_id", claims.DiscordServerID)
		c.Set("discord_user_id", claims.DiscordUserID)

		c.Next()
	}
}
