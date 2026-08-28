package tui_test

import "golang.org/x/sys/unix"

func ptyTerminalState(fd uintptr) (*unix.Termios, error) {
	state, err := unix.IoctlGetTermios(int(fd), unix.TIOCGETA)
	if err == nil {
		// Darwin may set PENDIN when switching back to canonical mode with
		// unread input. This is transient kernel state, not a changed terminal
		// preference. Compare every other flag, control character, and speed.
		state.Lflag &^= unix.PENDIN
	}
	return state, err
}
