package helpers

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/tomerh2001/maple-culvert-tracker/internal/data"
)

// rankingsClient bounds every official-rankings lookup so a slow Nexon API
// can never hang an interaction.
var rankingsClient = &http.Client{Timeout: 10 * time.Second}

func FetchCharacterData(name string, region string) (*data.PlayerRank, error) {
	if region != "na" && region != "eu" {
		region = "na"
	}
	req, err := http.NewRequest("GET", "https://www.nexon.com/api/maplestory/no-auth/ranking/v2/"+region, nil)
	if err != nil {
		return nil, err
	}
	q := req.URL.Query()
	q.Add("type", "overall")
	q.Add("id", "legendary")
	q.Add("reboot_index", "0")
	q.Add("page_index", "1")
	q.Add("character_name", name)
	req.URL.RawQuery = q.Encode()

	resp, err := rankingsClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	rawbody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	body := data.PlayerRankingResponse{}
	err = json.Unmarshal(rawbody, &body)
	if err != nil {
		return nil, err
	}

	if body.TotalCount != 1 {
		return nil, errors.New("character not found on official rankings")
	}

	return &body.Ranks[0], nil
}
