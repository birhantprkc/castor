// Package colour is the transfer a picture is tagged with: SDR, or one of the two HDR transfers.
package colour

// SDR tags nothing, as almost every web source does.
type SDR struct{}

func (SDR) Name() string   { return "sdr" }
func (SDR) Filter() string { return "" }
