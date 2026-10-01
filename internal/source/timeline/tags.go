package timeline

// The media playlist tags castor reads from an origin and writes for ffmpeg.
const (
	TagHeader                = "#EXTM3U"
	tagVersion               = "#EXT-X-VERSION"
	tagTargetDuration        = "#EXT-X-TARGETDURATION"
	TagMediaSequence         = "#EXT-X-MEDIA-SEQUENCE"
	tagDiscontinuitySequence = "#EXT-X-DISCONTINUITY-SEQUENCE"
	TagStart                 = "#EXT-X-START"
	TagInf                   = "#EXTINF"
	TagByteRange             = "#EXT-X-BYTERANGE"
	TagDiscontinuity         = "#EXT-X-DISCONTINUITY"
	TagGap                   = "#EXT-X-GAP"
	TagKey                   = "#EXT-X-KEY"
	TagMap                   = "#EXT-X-MAP"
	TagEndList               = "#EXT-X-ENDLIST"
)
