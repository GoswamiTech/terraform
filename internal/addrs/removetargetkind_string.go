// Code generated by "stringer -type RemoveTargetKind"; DO NOT EDIT.

package addrs

import "strconv"

func _() {
	// An "invalid array index" compiler error signifies that the constant values have changed.
	// Re-run the stringer command to generate them again.
	var x [1]struct{}
	_ = x[RemoveTargetModule-77]
	_ = x[RemoveTargetResource-82]
}

const (
	_RemoveTargetKind_name_0 = "RemoveTargetModule"
	_RemoveTargetKind_name_1 = "RemoveTargetResource"
)

func (i RemoveTargetKind) String() string {
	switch {
	case i == 77:
		return _RemoveTargetKind_name_0
	case i == 82:
		return _RemoveTargetKind_name_1
	default:
		return "RemoveTargetKind(" + strconv.FormatInt(int64(i), 10) + ")"
	}
}