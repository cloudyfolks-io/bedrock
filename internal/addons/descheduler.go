package addons

const evictionStrategySetting = "virt.eviction-strategy"

func DeschedulerCheck(facts Facts) Verdict {
	if facts.Settings[evictionStrategySetting] != "LiveMigrate" {
		return Verdict{State: VerdictFailed, Reason: "EvictionStrategyNotLiveMigrate", Message: "set the Setting virt.eviction-strategy to LiveMigrate, so that an eviction of a VM live-migrates it"}
	}
	return Verdict{State: VerdictPassed, Reason: "Passed", Message: "the KubeVirt eviction strategy is LiveMigrate"}
}
