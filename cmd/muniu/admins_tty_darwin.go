package main

import "golang.org/x/sys/unix"

const ttyGetRequest = unix.TIOCGETA
const ttySetRequest = unix.TIOCSETA
