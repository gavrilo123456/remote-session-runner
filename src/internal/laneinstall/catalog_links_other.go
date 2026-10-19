//go:build !darwin && !linux

package laneinstall

import "os"

func catalogFileHasSingleLink(os.FileInfo) bool { return false }
