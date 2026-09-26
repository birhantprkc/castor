package file

var MP4 = Container{Called: "file-mp4", Ext: ".mp4", Muxer: "mp4", MIME: "video/mp4", Args: []string{"-movflags", "+faststart"}}

// MP4MoovLast leaves the index after the media, as a muxer without faststart does, so a reader needs the file's tail first.
var MP4MoovLast = Container{Called: "file-mp4-moov-last", Ext: ".mp4", Muxer: "mp4", MIME: "video/mp4"}
