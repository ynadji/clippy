package main

import "github.com/ynadji/clippy/internal/clip"

// engines lists the clip engines compiled into this binary. The libav
// engine registers itself from engine_libav.go when built with -tags libav.
var engines = map[string]clip.Engine{"exec": clip.ExecEngine{}}

func selectEngine(name string) clip.Engine {
	if e, ok := engines[name]; ok {
		return e
	}
	return clip.ExecEngine{}
}
