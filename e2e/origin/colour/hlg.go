package colour

// HLG is broadcast HDR's transfer (ARIB STD-B67) on BT.2020 primaries.
type HLG struct{}

func (HLG) Name() string { return "hlg" }
func (HLG) Filter() string {
	return "setparams=color_primaries=bt2020:color_trc=arib-std-b67:colorspace=bt2020nc"
}
