package readiness

import "strings"

const reasonNotChecked = "not checked"

type Result struct {
	Ready  bool
	Failed []Check
}

func Evaluate(checks []Check) Result {
	reported := make(map[CheckName]Check, len(checks))
	for _, c := range checks {
		reported[c.Name] = c
	}

	failed := make([]Check, 0, len(reported))
	for _, name := range Required() {
		c, ok := reported[name]
		switch {
		case !ok:
			failed = append(failed, Failed(name, reasonNotChecked))
		case !c.OK:
			failed = append(failed, c)
		}
	}
	return Result{Ready: len(failed) == 0, Failed: failed}
}

func (r Result) Reason() string {
	if r.Ready {
		return ""
	}
	reasons := make([]string, 0, len(r.Failed))
	for _, c := range r.Failed {
		reasons = append(reasons, string(c.Name)+": "+c.Reason)
	}
	return strings.Join(reasons, "; ")
}
