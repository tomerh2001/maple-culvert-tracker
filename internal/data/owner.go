package data

import (
	"os"
	"strings"
)

// EnvVarBotOwnerUserID optionally names the deployment operator's Discord
// user id. Empty disables the check.
const EnvVarBotOwnerUserID = "BOT_OWNER_USER_ID"

// IsBotOwner reports whether userID is the deployment operator.
func IsBotOwner(userID string) bool {
	id := strings.TrimSpace(os.Getenv(EnvVarBotOwnerUserID))
	return id != "" && id == userID
}
