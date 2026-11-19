package cli

import (
	"reflect"
	"testing"
)

func TestKubeletValuesAcceptsSupportedKeys(t *testing.T) {
	var values kubeletValues
	if err := values.Set("kubelet.eviction-memory-available=256Mi"); err != nil {
		t.Fatal(err)
	}
	if err := values.Set("kubelet.system-reserved-memory=512Mi"); err != nil {
		t.Fatal(err)
	}
	if err := values.Set("kubelet.max-pods=220"); err != nil {
		t.Fatal(err)
	}
	if err := values.Set("kubelet.resolv-conf=/run/systemd/resolve/resolv.conf"); err != nil {
		t.Fatal(err)
	}
	want := kubeletValues{
		"kubelet.eviction-memory-available": "256Mi", "kubelet.system-reserved-memory": "512Mi",
		"kubelet.max-pods": "220", "kubelet.resolv-conf": "/run/systemd/resolve/resolv.conf",
	}
	if !reflect.DeepEqual(values, want) {
		t.Fatalf("values %v", values)
	}
	objects := kubeletSettingObjects(values)
	if len(objects) != 4 || objects[0].Name != "kubelet.eviction-memory-available" || objects[0].Spec.Value != "256Mi" ||
		objects[1].Name != "kubelet.max-pods" || objects[1].Spec.Value != "220" ||
		objects[2].Name != "kubelet.resolv-conf" || objects[2].Spec.Value != "/run/systemd/resolve/resolv.conf" {
		t.Fatalf("objects %+v", objects)
	}
}

func TestKubeletValuesRejectsOtherKeys(t *testing.T) {
	var values kubeletValues
	if err := values.Set("platform.host=x"); err == nil {
		t.Fatal("expected an error for a key that is not a kubelet setting")
	}
	if err := values.Set("kubelet.eviction-memory-available=lots"); err == nil {
		t.Fatal("expected an error for a bad quantity")
	}
	for _, limit := range []string{"0", "-1", "2147483648", "12\n--other-flag"} {
		if err := values.Set("kubelet.max-pods=" + limit); err == nil {
			t.Errorf("accepted invalid Pod limit %q", limit)
		}
	}
	for _, path := range []string{"resolv.conf", "/", "/run/../resolv.conf", "/run/a b", "/run/resolv.conf\n"} {
		if err := values.Set("kubelet.resolv-conf=" + path); err == nil {
			t.Errorf("accepted invalid resolver path %q", path)
		}
	}
}
