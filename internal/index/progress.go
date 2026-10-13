package index

// Event is one step of an index run. N is 1-based. Total is the number of
// walked files and is 0 before that count is known.
type Event struct {
	Action string
	Path   string
	N      int
	Total  int
}

func (s *Session) report(action, path string) {
	if s == nil || s.progress == nil {
		return
	}
	s.progress(Event{Action: action, Path: path, N: s.n, Total: s.total})
}

func reportOpt(opt Options, action string) {
	if opt.Progress == nil {
		return
	}
	opt.Progress(Event{Action: action})
}
