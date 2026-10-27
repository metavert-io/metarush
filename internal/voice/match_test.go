package voice

import (
	"math"
	"testing"
)

// axis is a unit vector along dimension i (32 dimensions).
func axis(i int) []float32 {
	v := make([]float32, 32)
	v[i] = 1
	return v
}

// at is a unit vector with cosine c to axis(i), the rest along axis(j).
func at(i, j int, c float64) []float32 {
	v := make([]float32, 32)
	v[i], v[j] = float32(c), float32(math.Sqrt(1-c*c))
	return v
}

func person(id string, i int) Profile {
	p := Profile{ID: id, Name: id}
	for k := 0; k < 3; k++ {
		p.Exemplars = append(p.Exemplars, ProfileExemplar{Source: "a/" + id, Vec: axis(i), Seconds: 3})
	}
	p.Centroid = axis(i)
	return p
}

func voiceAt(c float64, embedded float64) Cluster {
	return Cluster{Label: "speaker_00", Centroid: at(0, 9, c), Embedded: embedded}
}

// With only one or two known people, "beats the runner-up by 0.20" says
// nothing, so automatic labels need 0.75; between the suggestion threshold
// and that, the voice becomes an "Is this …?" question.
func TestFewPeopleNeedAHigherScoreForAutomaticLabels(t *testing.T) {
	maya := person("Maya", 0)
	th := DefaultThresholds
	cases := []struct {
		name    string
		people  []Profile
		score   float64
		seconds float64
		auto    bool
		suggest bool
	}{
		{"one person, 77%", []Profile{maya}, 0.77, 10, true, true},
		{"one person, 68% (a question)", []Profile{maya}, 0.68, 10, false, true},
		{"one person, short voice at 70%", []Profile{maya}, 0.70, 2, false, true},
		{"two people, 70%", []Profile{maya, person("Sam", 5)}, 0.70, 10, false, true},
		{"three people, 68% well ahead of the rest", []Profile{maya, person("Sam", 5), person("Ada", 7)}, 0.68, 10, true, true},
		{"three people, 58% (below the usual bar)", []Profile{maya, person("Sam", 5), person("Ada", 7)}, 0.58, 10, false, true},
		{"one person, 30%", []Profile{maya}, 0.30, 10, false, false},
	}
	for _, c := range cases {
		d := Decide(voiceAt(c.score, c.seconds), c.people, th)
		if (d.Auto != nil) != c.auto {
			t.Errorf("%s: automatic label = %v, want %v (%+v)", c.name, d.Auto != nil, c.auto, d)
		}
		if got := len(d.Suggestions) > 0 && d.Suggestions[0].PersonID == "Maya"; got != c.suggest {
			t.Errorf("%s: suggestion of Maya = %v, want %v", c.name, got, c.suggest)
		}
	}
	if got := th.AutoNeed(voiceAt(0.8, 10), 1); got != 0.75 {
		t.Errorf("one candidate needs %.2f, want 0.75", got)
	}
	if got := th.AutoNeed(voiceAt(0.8, 10), 3); got != th.Auto {
		t.Errorf("three candidates need %.2f, want %.2f", got, th.Auto)
	}
}
