package media

import "testing"

func TestHeightCapAdmitsWhatWasNeverEstablished(t *testing.T) {
	const ceiling HeightCap = 1080
	for height, want := range map[int]bool{0: true, 1080: true, 1081: false} {
		if got := ceiling.Admits(height); got != want {
			t.Errorf("HeightCap(%d).Admits(%d) = %v, want %v", ceiling, height, got, want)
		}
	}
}
