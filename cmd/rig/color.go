package main

import (
	"fmt"
	"os"
)

var colorEnabled = isTTY(os.Stdout)

func isTTY(f *os.File) bool {
	fi, err := f.Stat()
	if err != nil {
		return false
	}
	return fi.Mode()&os.ModeCharDevice != 0
}

const (
	ansiReset   = "\033[0m"
	ansiBold    = "\033[1m"
	ansiDim     = "\033[2m"
	ansiRed     = "\033[31m"
	ansiGreen   = "\033[32m"
	ansiYellow  = "\033[33m"
	ansiCyan    = "\033[36m"
)

// serviceColorFor returns a true-color ANSI code for a service, evenly
// distributing hues around the spectrum based on total service count.
// Starts at 210° (blue) to avoid red/yellow/green/cyan which are used
// for semantic coloring (status codes, methods, errors).
func serviceColorFor(index, total int) string {
	if total <= 0 {
		total = 1
	}
	hue := (210.0 + float64(index)*(360.0/float64(total)))
	for hue >= 360 {
		hue -= 360
	}
	r, g, b := hslToRGB(hue/360.0, 0.65, 0.65)
	return fmt.Sprintf("\033[38;2;%d;%d;%dm", r, g, b)
}

func colorService(s string, index int) string {
	if !colorEnabled {
		return s
	}
	c := serviceColorFor(index, serviceColorTotal)
	return c + s + ansiReset
}

// serviceColorTotal is set before rendering so colorService knows
// how many services to distribute hues across.
var serviceColorTotal int

// hslToRGB converts HSL (all 0–1) to RGB (0–255).
func hslToRGB(h, s, l float64) (uint8, uint8, uint8) {
	if s == 0 {
		v := uint8(l * 255)
		return v, v, v
	}
	var q float64
	if l < 0.5 {
		q = l * (1 + s)
	} else {
		q = l + s - l*s
	}
	p := 2*l - q
	r := hueToRGB(p, q, h+1.0/3.0)
	g := hueToRGB(p, q, h)
	b := hueToRGB(p, q, h-1.0/3.0)
	return uint8(r * 255), uint8(g * 255), uint8(b * 255)
}

func hueToRGB(p, q, t float64) float64 {
	if t < 0 {
		t++
	}
	if t > 1 {
		t--
	}
	switch {
	case t < 1.0/6.0:
		return p + (q-p)*6*t
	case t < 1.0/2.0:
		return q
	case t < 2.0/3.0:
		return p + (q-p)*(2.0/3.0-t)*6
	default:
		return p
	}
}

func bold(s string) string {
	if !colorEnabled {
		return s
	}
	return ansiBold + s + ansiReset
}

func dim(s string) string {
	if !colorEnabled {
		return s
	}
	return ansiDim + s + ansiReset
}

func colorStatus(s string) string {
	if !colorEnabled || len(s) == 0 {
		return s
	}
	switch s[0] {
	case '2':
		return ansiGreen + s + ansiReset
	case '4':
		return ansiYellow + s + ansiReset
	case '5':
		return ansiRed + s + ansiReset
	}
	if s == "OK" {
		return ansiGreen + s + ansiReset
	}
	return s
}

func colorMethod(s string) string {
	if !colorEnabled {
		return s
	}
	return ansiCyan + s + ansiReset
}
