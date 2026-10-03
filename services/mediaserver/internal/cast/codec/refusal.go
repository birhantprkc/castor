package codec

// Reason is evidence that made stream-copying one axis unsafe.
type Reason string

const (
	reasonDeviceVideo     Reason = "device-video-incompatible"
	reasonDeviceAudio     Reason = "device-audio-incompatible"
	reasonContainerVideo  Reason = "container-video-incompatible"
	reasonContainerAudio  Reason = "container-audio-incompatible"
	reasonVideoCopyFailed Reason = "video-copy-failed"
	reasonAudioCopyFailed Reason = "audio-copy-failed"
	reasonHeightLimit     Reason = "height-limit"
	reasonHDRPolicy       Reason = "hdr-policy"
	reasonInterlaced      Reason = "interlaced"
	reasonRotated         Reason = "rotated"
	reasonSampleRate      Reason = "sample-rate"
	reasonSpliced         Reason = "spliced"
	reasonSubtitleBurnIn  Reason = "subtitle-burn-in"
)

// refusalRule is one row of an axis's copy-refusal table, with reason, prose, and when it applies.
type refusalRule struct {
	reason Reason
	// why is the same refusal in prose for the one line that states it, and is a function of the subject.
	why func(Inputs) string
	// when reports whether this row refuses this subject.
	when func(Inputs) bool
}

// says returns a constant why function for a row whose reasoning is the same sentence for every subject.
func says(why string) func(Inputs) string { return func(Inputs) string { return why } }

// Refusal is one fired row: why this axis is not copied.
type Refusal struct {
	Reason Reason
	Why    string
}

func refuse(table []refusalRule, in Inputs) []Refusal {
	var fired []Refusal
	for _, r := range table {
		if r.when(in) {
			fired = append(fired, Refusal{Reason: r.reason, Why: r.why(in)})
		}
	}
	return fired
}
