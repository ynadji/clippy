//go:build libav

package main

import "github.com/ynadji/clippy/internal/clip/libav"

func init() { engines["libav"] = libav.Engine{} }
