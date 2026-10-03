package timeline

// The media playlist tags castor reads from an origin and writes for ffmpeg.
const (
	TagHeader        = "#EXTM3U"
	TagMediaSequence = "#EXT-X-MEDIA-SEQUENCE"
	TagStart         = "#EXT-X-START"
	TagInf           = "#EXTINF"
	TagByteRange     = "#EXT-X-BYTERANGE"
	TagDiscontinuity = "#EXT-X-DISCONTINUITY"
	TagGap           = "#EXT-X-GAP"
	TagKey           = "#EXT-X-KEY"
	TagMap           = "#EXT-X-MAP"
	TagEndList       = "#EXT-X-ENDLIST"
)
