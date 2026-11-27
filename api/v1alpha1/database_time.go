package v1alpha1

import (
	"encoding/json"
	"regexp"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

var databaseTimePrecision = regexp.MustCompile(`^[^.,]+([.,][0-9]{1,6}0*(Z|[+-][0-9]{2}:[0-9]{2}))?$`)

func HasDatabaseTimePrecision(raw string) bool {
	return databaseTimePrecision.MatchString(raw)
}

type restoreTimeWire struct {
	SourceUID  string  `json:"sourceUID"`
	TargetTime *string `json:"targetTime,omitempty"`
}

func databaseTimeText(at *metav1.Time) *string {
	if at == nil {
		return nil
	}
	text := at.UTC().Format(time.RFC3339Nano)
	return &text
}

func (r DatabaseRestore) MarshalJSON() ([]byte, error) {
	wire := restoreTimeWire{SourceUID: r.SourceUID, TargetTime: databaseTimeText(r.TargetTime)}
	if r.inexactTargetTime != "" {
		wire.TargetTime = &r.inexactTargetTime
	}
	return json.Marshal(wire)
}

func (r *DatabaseRestore) UnmarshalJSON(data []byte) error {
	var wire restoreTimeWire
	if err := json.Unmarshal(data, &wire); err != nil {
		return err
	}
	value := DatabaseRestore{SourceUID: wire.SourceUID}
	if wire.TargetTime != nil {
		at, err := time.Parse(time.RFC3339Nano, *wire.TargetTime)
		if err != nil {
			return err
		}
		value.TargetTime = &metav1.Time{Time: at.UTC()}
		if !HasDatabaseTimePrecision(*wire.TargetTime) {
			value.inexactTargetTime = *wire.TargetTime
		}
	}
	*r = value
	return nil
}

func (r DatabaseRestore) HasInexactTargetTime() bool {
	return r.inexactTargetTime != "" || r.TargetTime != nil && r.TargetTime.Nanosecond()%1000 != 0
}

func (s DatabaseBackupStatus) MarshalJSON() ([]byte, error) {
	type fields DatabaseBackupStatus
	return json.Marshal(struct {
		fields
		PITRFrom *string `json:"pitrFrom,omitempty"`
		PITRTo   *string `json:"pitrTo,omitempty"`
	}{fields: fields(s), PITRFrom: databaseTimeText(s.PITRFrom), PITRTo: databaseTimeText(s.PITRTo)})
}
