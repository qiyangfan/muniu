package main

import "golang.org/x/sys/unix"

const ttyGetRequest = unix.TCGETS
const ttySetRequest = unix.TCSETS
