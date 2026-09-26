package colour

// PQ is HDR10's transfer (SMPTE ST 2084) on BT.2020 primaries.
type PQ struct{}

func (PQ) Name() string { return "pq" }
func (PQ) Filter() string {
	return "setparams=color_primaries=bt2020:color_trc=smpte2084:colorspace=bt2020nc"
}
