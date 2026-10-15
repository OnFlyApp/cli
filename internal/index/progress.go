package index

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
	s.progress(Event{Action: action, Path: path, N: int(s.n.Load()), Total: s.total})
}

func reportOpt(opt Options, action string) {
	if opt.Progress == nil {
		return
	}
	opt.Progress(Event{Action: action})
}
