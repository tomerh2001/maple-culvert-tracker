package commands

import (
	"context"
	"log"
	"sync"
	"time"

	"github.com/bwmarrin/discordgo"
	apihelpers "github.com/tomerh2001/maple-culvert-tracker/internal/api/helpers"
	"github.com/tomerh2001/maple-culvert-tracker/internal/apiredis"
	"github.com/tomerh2001/maple-culvert-tracker/internal/db"
)

// AddWeeklyRecaps starts one scheduler after Discord is ready. Checks align
// with UTC minute boundaries, including the Thursday 00:00 weekly reset.
// A startup check catches a missed reset; persistent per-week delivery records
// make reconnects/restarts safe. Failed deliveries retry on the next minute.
func AddWeeklyRecaps(ctx context.Context, s *discordgo.Session) {
	var started sync.Once
	s.AddHandler(func(s *discordgo.Session, _ *discordgo.Ready) {
		started.Do(func() {
			go func() {
				for ctx.Err() == nil {
					s.State.RLock()
					guildIDs := make([]string, 0, len(s.State.Guilds))
					for _, guild := range s.State.Guilds {
						guildIDs = append(guildIDs, guild.ID)
					}
					s.State.RUnlock()
					if err := apihelpers.AnnounceWeeklyRecaps(ctx, s, db.DB, apiredis.RedisDB, guildIDs, time.Now()); err != nil && ctx.Err() == nil {
						log.Printf("weekly recap: %v", err)
					}
					timer := time.NewTimer(time.Until(time.Now().UTC().Truncate(time.Minute).Add(time.Minute)))
					select {
					case <-ctx.Done():
						timer.Stop()
						return
					case <-timer.C:
					}
				}
			}()
		})
	})
}
