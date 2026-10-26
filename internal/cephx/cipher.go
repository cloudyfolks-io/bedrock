package cephx

import (
	"fmt"
	"strconv"
	"strings"
)

const (
	Aes       = "aes"
	Aes256k   = "aes256k"
	minKernel = 7
)

func KernelRule() string {
	return fmt.Sprintf("aes256k cephx keys for the CSI kernel mounts need Linux kernel %d.0 or newer", minKernel)
}

func KernelSupportsAes256k(kernel string) bool {
	major, _, _ := strings.Cut(strings.TrimSpace(kernel), ".")
	number, err := strconv.Atoi(major)
	return err == nil && number >= minKernel
}

func KernelProblem(hostname, kernel string) string {
	if KernelSupportsAes256k(kernel) {
		return ""
	}
	return fmt.Sprintf("%s runs kernel %q: %s", hostname, kernel, KernelRule())
}

func ForKernel(kernel string) string {
	if KernelSupportsAes256k(kernel) {
		return Aes256k
	}
	return Aes
}
