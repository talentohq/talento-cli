package tui_test

import "golang.org/x/sys/unix"

func ptyTerminalState(fd uintptr) (*unix.Termios, error) {
	state, err := unix.IoctlGetTermios(int(fd), unix.TCGETS)
	if err == nil {
		state.Lflag &^= unix.PENDIN
	}
	return state, err
}
