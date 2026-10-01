package hls

import (
	"bufio"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/stupside/castor/internal/source/timeline"
)

// listing is a media playlist read line by line, each segment carrying every tag that applies to it.
type listing struct {
	segments []timeline.Segment
	closed   bool
	start    *timeline.Start
	// relaxed is a segment or init resource named with an extension a default reader rejects.
	relaxed bool
}

var errNotPlaylist = errors.New("not an HLS playlist")

// scanMedia reads a media playlist whose references resolve against base, where it arrived from.
func scanMedia(body string, base *url.URL) (listing, error) {
	if !strings.HasPrefix(strings.TrimLeft(body, "\uFEFF \t\r\n"), signature) {
		return listing{}, errNotPlaylist
	}
	var (
		out      listing
		sequence int64
		pending  timeline.Segment
		key      timeline.Key
		explicit bool
		init     *timeline.Map
		gap      bool
		seam     bool
		// ends is where each resource's last byte range ended, for a range that states no offset.
		ends = map[string]int64{}
	)
	lines := bufio.NewScanner(strings.NewReader(body))
	lines.Buffer(make([]byte, 0, 64<<10), len(body)+1)
	for lines.Scan() {
		line := strings.TrimSpace(strings.TrimPrefix(lines.Text(), "\uFEFF"))
		tag, value, _ := strings.Cut(line, ":")
		switch {
		case line == "" || (strings.HasPrefix(line, "#") && !strings.HasPrefix(line, "#EXT")):
		case tag == timeline.TagMediaSequence:
			n, err := strconv.ParseInt(value, 10, 64)
			if err != nil {
				return listing{}, fmt.Errorf("reading %s: %w", line, err)
			}
			sequence = n
		case tag == timeline.TagEndList:
			out.closed = true
		case tag == timeline.TagStart:
			a := attributes(value)
			offset, err := strconv.ParseFloat(a["TIME-OFFSET"], 64)
			if err != nil {
				return listing{}, fmt.Errorf("reading %s: %w", line, err)
			}
			out.start = &timeline.Start{Offset: seconds(offset), Precise: a["PRECISE"] == "YES"}
		case tag == timeline.TagInf:
			duration, _, _ := strings.Cut(value, ",")
			d, err := strconv.ParseFloat(duration, 64)
			if err != nil {
				return listing{}, fmt.Errorf("reading %s: %w", line, err)
			}
			pending.Duration = seconds(d)
		case tag == timeline.TagByteRange:
			pending.Range = byteRange(value)
		case tag == timeline.TagDiscontinuity:
			seam = true
		case tag == timeline.TagGap:
			gap = true
		case tag == timeline.TagKey:
			a := attributes(value)
			key, explicit = timeline.Key{}, false
			if method := a["METHOD"]; method != "" && !strings.EqualFold(method, "NONE") {
				uri, err := base.Parse(a["URI"])
				if err != nil {
					return listing{}, fmt.Errorf("reading %s: %w", line, err)
				}
				key = timeline.Key{Method: method, URI: uri.String(), IV: a["IV"], Format: a["KEYFORMAT"]}
				explicit = key.IV != ""
			}
		case tag == timeline.TagMap:
			a := attributes(value)
			uri, err := base.Parse(a["URI"])
			if err != nil {
				return listing{}, fmt.Errorf("reading %s: %w", line, err)
			}
			// An init range that states no offset starts at the resource's first byte; no segment precedes it.
			span := byteRange(a["BYTERANGE"])
			span.Offset = max(span.Offset, 0)
			// A key in force at the MAP encrypts the init section too, and names its IV outright.
			init = &timeline.Map{URI: uri.String(), Range: span, Key: key}
			out.relaxed = out.relaxed || requiresRelaxedHLSInput(a["URI"])
		case strings.HasPrefix(line, "#"):
		default:
			uri, err := base.Parse(line)
			if err != nil {
				return listing{}, fmt.Errorf("reading segment %q: %w", line, err)
			}
			pending.URI = uri.String()
			if pending.Range.Length > 0 && pending.Range.Offset < 0 {
				pending.Range.Offset = ends[pending.URI]
			}
			ends[pending.URI] = pending.Range.Offset + pending.Range.Length
			pending.Key, pending.Map = key, init
			// Without an IV the sequence number is the IV, and castor renumbers: it must travel stated.
			if key != (timeline.Key{}) && !explicit {
				pending.Key.IV = fmt.Sprintf("0x%032x", sequence)
			}
			pending.Place = timeline.Place{Start: sequence, End: sequence + 1}
			pending.Seam = seam
			out.relaxed = out.relaxed || requiresRelaxedHLSInput(line)
			// A gap lists media that does not exist, so what follows it is past a break the origin declared.
			if gap {
				seam = true
			} else {
				out.segments = append(out.segments, pending)
				seam = false
			}
			sequence++
			pending, gap = timeline.Segment{}, false
		}
	}
	return out, lines.Err()
}

// byteRange reads length[@offset]; an absent offset is marked negative, to continue the resource's last range.
func byteRange(value string) timeline.Range {
	if value == "" {
		return timeline.Range{}
	}
	length, offset, stated := strings.Cut(value, "@")
	n, _ := strconv.ParseInt(length, 10, 64)
	if !stated {
		return timeline.Range{Offset: -1, Length: n}
	}
	o, _ := strconv.ParseInt(offset, 10, 64)
	return timeline.Range{Offset: o, Length: n}
}

// attributes reads an attribute list, quoted values keeping their commas.
func attributes(list string) map[string]string {
	out := map[string]string{}
	for list != "" {
		name, rest, ok := strings.Cut(list, "=")
		if !ok {
			break
		}
		var value string
		if strings.HasPrefix(rest, `"`) {
			end := strings.Index(rest[1:], `"`)
			if end < 0 {
				end = len(rest) - 1
			}
			value, rest = rest[1:end+1], rest[min(end+2, len(rest)):]
		} else {
			value, rest, _ = strings.Cut(rest, ",")
		}
		out[strings.TrimSpace(name)] = value
		list = strings.TrimLeft(rest, ", ")
	}
	return out
}

func seconds(s float64) time.Duration { return time.Duration(s * float64(time.Second)) }
