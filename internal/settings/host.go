package settings

const PlatformHostPatchCommand = `kubectl patch setting platform.host --type merge -p '{"spec":{"value":"<domain>"}}'`
