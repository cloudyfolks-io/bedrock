package backup

import "maps"

func WithHold(annotations map[string]string, restore string) map[string]string {
	held := maps.Clone(annotations)
	if held == nil {
		held = map[string]string{}
	}
	held[HoldAnnotation] = restore
	return held
}

func WithoutHold(annotations map[string]string, restore string) map[string]string {
	if annotations[HoldAnnotation] != restore {
		return maps.Clone(annotations)
	}
	released := maps.Clone(annotations)
	delete(released, HoldAnnotation)
	return released
}
