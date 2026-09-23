package source

import "net/url"

// Parse reads a body in the first grammar that recognises it: its ladder, and what it names resolved against base.
func (fs Formats) Parse(body string, base *url.URL) (Ladder, []*url.URL) {
	for _, f := range fs {
		ladder, refs := f.Recognize(body)
		if ladder == LadderUnknown {
			continue
		}
		if base == nil {
			return ladder, nil
		}
		var names []*url.URL
		for _, ref := range refs {
			if resolved, err := base.Parse(ref); err == nil {
				names = append(names, resolved)
			}
		}
		return ladder, names
	}
	return LadderUnknown, nil
}
