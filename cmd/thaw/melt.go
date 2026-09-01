package main

import (
	"fmt"
	"os"
	"strings"
	"time"
)

// meltFrames — an ice block thawing into water. Each frame is exactly
// meltRows lines so the redraw can move the cursor up a fixed amount.
const meltRows = 3

var meltFrames = [][meltRows]string{
	{
		`  ❄ ▓▓▓▓▓▓▓▓▓▓ ❄`,
		`    ▓▓▓▓▓▓▓▓▓▓`,
		`    ▓▓▓▓▓▓▓▓▓▓`,
	},
	{
		`    ▒▒▓▓▓▓▓▓▒▒`,
		`    ▓▓▓▓▓▓▓▓▓▓ ❄`,
		`    ▓▓▓▓▓▓▓▓▓▓`,
	},
	{
		`     ░▒▒▒▒▒▒░`,
		`    ▒▓▓▓▓▓▓▓▓▒`,
		`  ~ ▓▓▓▓▓▓▓▓▓▓ ~`,
	},
	{
		`      ░░░░░░`,
		`   ~ ▒▒▒▒▒▒▒▒ ~`,
		`  ~ ~ ▓▓▓▓▓▓ ~ ~`,
	},
	{
		``,
		`     ~ ░░░░ ~`,
		` ~ ~ ~ ~~~~ ~ ~ ~`,
	},
	{
		``,
		``,
		`  ~ ~ ~ ~ ~ ~ ~ ~`,
	},
}

// playMelt renders the thawing animation on stdout. TTY-only, ~1s total,
// disabled by THAW_NO_MELT=1 or TERM=dumb. Cleans up after itself so normal
// restore output starts on an untouched line.
func playMelt() {
	if !isTTY(os.Stdout) || os.Getenv("THAW_NO_MELT") != "" || os.Getenv("TERM") == "dumb" {
		return
	}

	const (
		ice   = "\033[96m" // bright cyan
		water = "\033[94m" // blue
		reset = "\033[0m"
		hide  = "\033[?25l"
		show  = "\033[?25h"
	)

	fmt.Print(hide)
	defer fmt.Print(show)

	for i, frame := range meltFrames {
		if i > 0 {
			fmt.Printf("\033[%dF", meltRows) // cursor up to frame start
		}
		color := ice
		if i >= len(meltFrames)/2 {
			color = water
		}
		for _, line := range frame {
			fmt.Printf("\033[K%s%s%s\n", color, line, reset)
		}
		time.Sleep(160 * time.Millisecond)
	}

	// Erase the puddle — restore output takes over from a clean line.
	fmt.Printf("\033[%dF", meltRows)
	fmt.Print(strings.Repeat("\033[K\n", meltRows))
	fmt.Printf("\033[%dF", meltRows)
}
