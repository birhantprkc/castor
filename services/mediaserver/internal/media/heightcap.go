package media

// HeightCap is the ceiling the operator set on what may reach the device.
type HeightCap int

// Admits reports whether a picture of this height may reach the device.
func (c HeightCap) Admits(height int) bool { return height == 0 || height <= int(c) }
