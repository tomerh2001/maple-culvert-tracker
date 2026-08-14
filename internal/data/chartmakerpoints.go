package data

type ChartMakerPoints = struct {
	Label   string `json:"label"`
	Score   int    `json:"score"`
	RawDate string `json:"-"`
}

type ChartMakeMultiplePoints = struct {
	Labels    []string   `json:"labels"`
	DataPlots []DataPlot `json:"dataPlots"`
}

// DataPlot is one named line in a multi-series chart. Scores align to
// ChartMakeMultiplePoints.Labels index-for-index; a nil entry serializes to
// JSON null and is drawn as a gap (chartmaker spans across it).
type DataPlot struct {
	CharacterName string `json:"characterName"`
	Scores        []*int `json:"scores"`
}
