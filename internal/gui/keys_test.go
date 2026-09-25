package gui

import "testing"

func TestFiresOnPressThenRepeatsAfterTheDelay(t *testing.T) {
	var got []int
	for d := 0; d <= repeatDelay+3*repeatInterval; d++ {
		if fires(d, true) {
			got = append(got, d)
		}
	}
	want := []int{1, repeatDelay + repeatInterval, repeatDelay + 2*repeatInterval, repeatDelay + 3*repeatInterval}
	if len(got) != len(want) {
		t.Fatalf("fired on ticks %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("fired on ticks %v, want %v", got, want)
		}
	}
}

func TestNonRepeatingKeysFireOnce(t *testing.T) {
	for d := 0; d < 200; d++ {
		if fires(d, false) != (d == 1) {
			t.Fatalf("tick %d: fires = %v", d, fires(d, false))
		}
	}
}
